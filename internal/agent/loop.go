// Package agent implements the M2 query loop — the streaming agentic
// turn cycle that drives tool dispatch and multi-turn conversation.
//
// The loop mirrors src/query.ts's queryLoop() but with M5 additions:
// permission gate checks and PreToolUse/PostToolUse hook runners around
// each tool execution.
//
// Loop behaviour:
//  1. POST /v1/messages with current conversation history.
//  2. Stream SSE events; collect text deltas and tool_use blocks.
//  3. If the stop_reason is "tool_use":
//     a. Check permissions gate for each tool.
//     b. Run PreToolUse hooks.
//     c. Execute each tool in sequence (serial; concurrency in M4).
//     d. Run PostToolUse hooks.
//     e. Append assistant message + user tool_result message to history.
//     f. Go to 1 (unless MaxTurns exceeded).
//  4. If stop_reason is "end_turn": return.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/healthcheck"
	"github.com/icehunter/conduit/internal/hooks"
	"github.com/icehunter/conduit/internal/microcompact"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/ratelimit"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
)

// maxConcurrentTools is the worker pool size for parallel tool execution.
// Mirrors the coordinator's concurrency limit in src/coordinator/coordinatorMode.ts.
const maxConcurrentTools = 4
const maxToolUseRecoveries = 2

// DefaultSubAgentMaxTurns is applied to child loops whose MaxTurns was 0
// (unbounded). The parent loop's own limit (50 in mainrepl.go) is inherited
// via the copied config, so this only affects loops that never set a cap
// (e.g. background reviewers, council members, tests).
const DefaultSubAgentMaxTurns = 50

// ErrMaxTurnsExceeded is returned by Run when the loop exhausts its MaxTurns
// budget before reaching a natural end_turn. Callers should treat it as a
// soft limit rather than a fatal error: the conversation history in the first
// return value reflects the state at the cap. Foreground callers may surface a
// soft notice; subagent callers should mark the result as potentially incomplete.
var ErrMaxTurnsExceeded = errors.New("agent: max turns exceeded")

// EventType identifies what kind of loop event the caller receives.
type EventType int

const (
	EventText       EventType = iota // a text delta streamed from the model
	EventToolStart                   // tool_use block started streaming; name known, input pending
	EventToolUse                     // tool_use block complete; tool is about to run
	EventToolResult                  // tool execution finished
	EventRateLimit                   // rate-limit headers received; RateLimitWarning may be non-empty
	EventAPIRetry                    // API returned 429 and the client is backing off
	EventPartial                     // stream errored mid-turn; PartialBlocks holds what was received
	EventUsage                       // per-API-turn usage from SSE (input + output + cache fields)
	EventCost                        // running cost estimate (emitted on each message_delta with output tokens)
	EventCompacted                   // auto-compact ran; CompactedInputTokens and CompactedThreshold carry context
)

// LoopEvent is emitted to the caller's handler on each significant event.
type LoopEvent struct {
	Type EventType

	// EventText
	Text string

	// EventToolUse
	ToolName  string
	ToolID    string
	ToolInput json.RawMessage

	// EventToolResult
	ResultText string
	IsError    bool

	// EventRateLimit
	RateLimitWarning string // non-empty when quota is running low
	RateLimitInfo    ratelimit.Info

	// EventAPIRetry
	RetryAttempt     int
	RetryDelay       time.Duration
	RetryAfter       time.Duration
	RateLimitResetAt time.Time
	RetryErr         error

	// EventPartial — fired before a stream error bubbles up so callers
	// can persist whatever assistant content was streamed before the
	// failure. The blocks here are already filtered (empty text/truncated
	// tool_use dropped by buildContentBlocks).
	PartialBlocks []api.ContentBlock
	PartialErr    error

	// EventUsage carries the API-reported usage for one streamed turn.
	Usage api.Usage

	// EventCost carries the running cost estimate.
	CostUSD float64

	// EventCompacted
	CompactedInputTokens int
	CompactedThreshold   int
}

// LoopConfig controls the loop's behaviour.
type LoopConfig struct {
	Model     string
	MaxTokens int
	System    []api.SystemBlock
	Metadata  map[string]any
	// MaxTurns caps the number of API calls (tool-use follow-ups each count
	// as one turn). 0 means no limit (use carefully).
	MaxTurns int
	// Cwd is the working directory for the session. Used for persisting
	// "always allow" rules to <cwd>/.conduit/settings.local.json.
	Cwd string

	// Gate is the permission gate to consult before each tool call.
	// nil means no gate (all tools allowed).
	Gate *permissions.Gate

	// Hooks is the hooks configuration to run around tool calls.
	// nil means no hooks.
	Hooks *settings.HooksSettings

	// SessionID is used when invoking hooks (passed as session_id in hook input).
	SessionID string

	// AutoCompact enables automatic history compaction when input token usage
	// exceeds 80% of MaxTokens. Mirrors the auto-compact behavior in
	// src/services/compact/compact.ts and QueryEngine.ts.
	AutoCompact bool
	// ContextWindow overrides model-derived context window sizing for custom
	// providers whose model names do not carry enough information.
	ContextWindow int

	// ThinkingBudget, when > 0, sends thinking:{type:"enabled",budget_tokens:N}
	// in each API request. Requires the interleaved-thinking-2025-05-14 beta header.
	// Set via /effort command or CLAUDE_THINKING_BUDGET env var.
	ThinkingBudget int

	// NotifyOnComplete, when true, fires a desktop notification after each
	// end_turn (not after tool-use turns). Mirrors the notifs hook behavior.
	NotifyOnComplete bool

	// AskPermission is called when a tool needs interactive approval.
	// It blocks until the user responds. Returns (allow, alwaysAllow).
	// nil means DecisionAsk → allow through silently.
	AskPermission func(ctx context.Context, toolName, toolInput string) (allow, alwaysAllow bool)

	// OnFileAccess is called after each file tool execution with the operation
	// ("read" or "write") and the file path. Used to populate /files output.
	OnFileAccess func(op, path string)

	// OnSubAgentUsage is called after a sub-agent loop completes with the model
	// name and aggregated token usage across all turns of that sub-agent. Used
	// to record sub-agent cost events in the session JSONL so that Task tool
	// charges appear in ledger output. Runs in the agent-loop goroutine; callers
	// must not assume TUI goroutine context.
	OnSubAgentUsage func(model string, usage api.Usage)

	// OnEndTurn fires after each end_turn (no tool_use) with the up-to-date
	// message history. Mirrors CC's post-Stop extractMemories trigger. The
	// caller is expected to single-flight any background work — Loop fires
	// this synchronously before returning so the caller can choose between
	// blocking or detaching to a goroutine.
	OnEndTurn func(history []api.Message)

	// OnToolBatchComplete fires after each batch of tool results is appended
	// to history, before the next API request. pendingEdits is the number of
	// staged-but-not-yet-flushed file edits. If the callback returns true, the
	// loop treats the current turn as if end_turn was reached: it calls
	// OnEndTurn, then returns the current history. The caller is responsible
	// for re-submitting if it wants the agent to continue.
	//
	// Only fires for the foreground loop; loopsubagent.go zeros this field
	// for every child loop (same pattern as OnEndTurn).
	OnToolBatchComplete func(pendingEdits int) bool

	// OnCompact fires when auto-compaction runs and provides the summary text.
	// Used by the TUI to persist the summary to the session transcript.
	OnCompact func(summary string)

	// MicroCompact, when true, runs time-based microcompaction before each
	// request (mirrors src/services/compact/microCompact.ts time-based path).
	// When the gap since the last assistant message exceeds MicroCompactGap,
	// older tool_results are replaced with a placeholder. The cache is
	// expired anyway past that gap, so this shrinks what gets re-cached
	// without changing functional context.
	MicroCompact     bool
	MicroCompactGap  time.Duration // default 60m if zero
	MicroCompactKeep int           // default 5 if zero
	// LastAssistantTime seeds the gap calculation on resume. If zero, the
	// loop initializes from the first assistant response.
	LastAssistantTime time.Time

	// BackgroundModel returns the model used for helper calls such as
	// compaction, memory extraction, and Task sub-agents. Empty means use Model.
	BackgroundModel func() string
	// IsOAuthSubscription, when true, enables the dynamic per-request billing
	// suffix computation (SHA256 of the first user message). Set for Claude.ai
	// Max subscription accounts only; leave false for Console API key and
	// OpenAI-compatible providers so their billing block is unchanged.
	IsOAuthSubscription bool

	// BackgroundReviewer, when non-nil, is called after each end_turn to
	// trigger periodic background memory and skill reviews. It is nil by
	// default so existing code paths and sub-agents are unaffected.
	// The reviewer runs fire-and-forget goroutines internally; the loop
	// does not need to manage their lifetimes.
	BackgroundReviewer interface {
		OnEndTurn(ctx context.Context)
	}
}

// Loop drives the agentic query cycle.
type Loop struct {
	mu     sync.RWMutex
	client *api.Client
	reg    *tool.Registry
	cfg    LoopConfig
	// consecutiveCompactFails tracks how many times auto-compact has failed in a
	// row. Mirrors the MAX_CONSECUTIVE_AUTOCOMPACT_FAILURES circuit breaker in
	// autoCompact.ts. Stored on the struct so it persists across Run() calls.
	consecutiveCompactFails int
	// steerMsg holds an optional user steering message to inject between
	// tool-call rounds. Written by InjectSteerMessage (TUI goroutine), read
	// and cleared by Run (loop goroutine) via atomic.Value so no extra lock
	// is needed.
	steerMsg atomic.Value // stores string
	// session-start reminder is computed once per Loop lifetime (not once per
	// Run call) to avoid re-injecting identical startup context on every user
	// message.
	sessionStartMu       sync.Mutex
	sessionStartDone     bool
	sessionReminderUsed  bool
	sessionReminderBlock *api.SystemBlock
	// lastCachedPrefixHash is a FNV-1a hash of the cached-prefix content sent
	// in the previous turn, used to detect silent cache busts.
	lastCachedPrefixHash uint64
}

// NewLoop constructs a Loop.
func NewLoop(client *api.Client, reg *tool.Registry, cfg LoopConfig) *Loop {
	l := &Loop{client: client, reg: reg, cfg: cfg}
	// Wire the background runner into the hooks package so prompt/agent hooks
	// can spawn helper LLM calls. Stored on DefaultAsyncGroup to avoid a
	// package-level mutable global.
	if hooks.DefaultAsyncGroup != nil {
		hooks.DefaultAsyncGroup.SubAgentRunner = l.RunBackgroundAgent
	}
	return l
}

// InjectSteerMessage queues a user steering message to be injected into the
// conversation between the current tool-call batch and the next API request.
// Safe to call from any goroutine. Only the most recent call takes effect per
// batch — if the user types multiple messages quickly, the last one wins
// (earlier ones were superseded). The loop clears the slot after consuming it.
func (l *Loop) InjectSteerMessage(text string) {
	l.steerMsg.Store(text)
}

// SetBackgroundReviewer wires a background review scheduler into the loop.
// It is safe to call after NewLoop (e.g., after the sub-agent runner is
// available). A nil value disables background reviews.
func (l *Loop) SetBackgroundReviewer(r interface{ OnEndTurn(ctx context.Context) }) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg.BackgroundReviewer = r
}

// SetModel updates the model used for new requests (from /model slash command).
func (l *Loop) SetModel(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg.Model = name
}

func (l *Loop) SetContextWindow(tokens int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg.ContextWindow = tokens
}

// Model returns the model configured for new requests.
func (l *Loop) Model() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.cfg.Model
}

// BackgroundModel returns the helper/background model for new secondary calls.
func (l *Loop) BackgroundModel() string {
	l.mu.RLock()
	bgModel := l.cfg.BackgroundModel
	model := l.cfg.Model
	l.mu.RUnlock()
	if bgModel != nil {
		if m := strings.TrimSpace(bgModel()); m != "" {
			return m
		}
	}
	return model
}

// SetThinkingBudget updates the thinking budget for subsequent requests.
// Set to 0 to disable thinking. Used by /effort command.
func (l *Loop) SetThinkingBudget(budget int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg.ThinkingBudget = budget
}

// GetThinkingBudget returns the current thinking budget.
func (l *Loop) GetThinkingBudget() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.cfg.ThinkingBudget
}

// SetSystem replaces the system blocks for subsequent requests.
func (l *Loop) SetSystem(blocks []api.SystemBlock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg.System = blocks
}

// SetClient swaps the API client (e.g. after a fresh login reloads credentials).
func (l *Loop) SetClient(client *api.Client) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.client = client
}

// SetAskPermission installs the interactive permission callback.
// Called from the TUI after the Bubble Tea program is created.
func (l *Loop) SetAskPermission(fn func(ctx context.Context, toolName, toolInput string) (allow, alwaysAllow bool)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg.AskPermission = fn
}

// SetSessionID updates the active session identifier. When it changes, reset
// the one-time SessionStart reminder state so startup context is recomputed
// once for the new session.
func (l *Loop) SetSessionID(id string) {
	l.mu.Lock()
	changed := l.cfg.SessionID != id
	l.cfg.SessionID = id
	l.mu.Unlock()
	if !changed {
		return
	}
	l.sessionStartMu.Lock()
	l.sessionStartDone = false
	l.sessionReminderUsed = false
	l.sessionReminderBlock = nil
	l.sessionStartMu.Unlock()
}

func (l *Loop) sessionStartReminderBlock(ctx context.Context) (*api.SystemBlock, error) {
	l.sessionStartMu.Lock()
	if l.sessionStartDone {
		block := l.sessionReminderBlock
		l.sessionStartMu.Unlock()
		return block, nil
	}
	l.sessionStartMu.Unlock()

	l.mu.RLock()
	cwd := l.cfg.Cwd
	sessionID := l.cfg.SessionID
	hooksCfg := l.cfg.Hooks
	l.mu.RUnlock()

	var additionalCtxParts []string
	// Run pre-flight health checks (git status, deps, etc.) once.
	if cwd != "" {
		if result := healthcheck.Run(ctx, cwd, healthcheck.DefaultTimeout); result.HasIssue {
			if ctx := result.FormatContext(); ctx != "" {
				additionalCtxParts = append(additionalCtxParts, ctx)
			}
		}
	}
	// Run SessionStart hooks once.
	if hooksCfg != nil && len(hooksCfg.SessionStart) > 0 {
		if addlCtx := hooks.RunSessionStart(ctx, hooksCfg.SessionStart, sessionID); addlCtx != "" {
			additionalCtxParts = append(additionalCtxParts, addlCtx)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var block *api.SystemBlock
	if len(additionalCtxParts) > 0 {
		block = &api.SystemBlock{
			Type: "text",
			Text: "<system-reminder>\n" + strings.Join(additionalCtxParts, "\n\n") + "\n</system-reminder>",
		}
	}

	l.sessionStartMu.Lock()
	defer l.sessionStartMu.Unlock()
	if l.sessionStartDone {
		return l.sessionReminderBlock, nil
	}
	l.sessionStartDone = true
	l.sessionReminderBlock = block
	return l.sessionReminderBlock, nil
}

func (l *Loop) consumeSessionReminderBlock() *api.SystemBlock {
	l.sessionStartMu.Lock()
	defer l.sessionStartMu.Unlock()
	if l.sessionReminderUsed {
		return nil
	}
	l.sessionReminderUsed = true
	return l.sessionReminderBlock
}

func isToolUseFlowError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !(strings.Contains(msg, "invalid_request_error") || strings.Contains(msg, "bad_request_error") || strings.Contains(msg, "400")) {
		return false
	}
	return strings.Contains(msg, "tool_use") ||
		strings.Contains(msg, "tool use") ||
		strings.Contains(msg, "tool_result") ||
		strings.Contains(msg, "tool result")
}

func stripTrailingAssistantToolUse(msgs []api.Message) ([]api.Message, bool) {
	out := make([]api.Message, len(msgs))
	copy(out, msgs)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role != "assistant" {
			continue
		}
		filtered := make([]api.ContentBlock, 0, len(out[i].Content))
		removed := false
		for _, b := range out[i].Content {
			if b.Type == "tool_use" {
				removed = true
				continue
			}
			filtered = append(filtered, b)
		}
		if !removed {
			continue
		}
		if len(filtered) == 0 {
			out = append(out[:i], out[i+1:]...)
		} else {
			out[i].Content = filtered
		}
		return out, true
	}
	return msgs, false
}

func recoverToolUseHistory(msgs []api.Message) ([]api.Message, bool) {
	cleaned := session.FilterUnresolvedToolUses(msgs)
	if !reflect.DeepEqual(cleaned, msgs) {
		return cleaned, true
	}
	return stripTrailingAssistantToolUse(cleaned)
}

// Run executes the agentic loop starting with the given messages. handler is
// called synchronously for each event; it must not block.
//
// Returns the full accumulated message history (including all tool turns) and
// nil error on clean end_turn. On error, returns whatever history was built
// before the failure. Callers should replace their history slice with the
// returned messages to correctly track multi-turn tool use.
func (l *Loop) Run(ctx context.Context, messages []api.Message, handler func(LoopEvent)) ([]api.Message, error) {
	msgs := make([]api.Message, len(messages))
	copy(msgs, messages)
	msgs = session.FilterUnresolvedToolUses(msgs)

	// Snapshot mutable fields under the read lock so that concurrent Set*
	// calls from the TUI goroutine cannot race with this turn's reads.
	l.mu.RLock()
	model := l.cfg.Model
	system := l.cfg.System
	thinkingBudget := l.cfg.ThinkingBudget
	contextWindow := l.cfg.ContextWindow
	client := l.client
	l.mu.RUnlock()

	sessionReminderBlock, err := l.sessionStartReminderBlock(ctx)
	if err != nil {
		return msgs, err
	}
	defer func() {
		if l.cfg.Hooks != nil && len(l.cfg.Hooks.Stop) > 0 {
			// Use ctx (not Background()) so Ctrl-C cancels slow hooks promptly.
			// 2s cap: Stop hooks are advisory; don't block shutdown.
			stopCtx, stopCancel := context.WithTimeout(ctx, 2*time.Second)
			defer stopCancel()
			hooks.RunStop(stopCtx, l.cfg.Hooks.Stop, l.cfg.SessionID)
		}
	}()

	// Build tool definitions from registry.
	tools := buildToolDefs(l.reg)

	turn := 0
	lastAssistantTime := l.cfg.LastAssistantTime
	lastInputTokens := 0 // last reported input token count; 0 means unknown
	streamFailures := 0
	toolUseRecoveries := 0
	for {
		if ctx.Err() != nil {
			return msgs, ctx.Err()
		}
		if l.cfg.MaxTurns > 0 && turn >= l.cfg.MaxTurns {
			return msgs, ErrMaxTurnsExceeded
		}
		turn++
		msgs = session.FilterUnresolvedToolUses(msgs)

		// Micro-compact: two triggers — time-based (cache expired) or token-pressure
		// (lastInputTokens exceeds MicroCompactThresholdPct of context window).
		// Either condition alone is sufficient to compact.
		if l.cfg.MicroCompact {
			gap := l.cfg.MicroCompactGap
			if gap == 0 {
				gap = microcompact.DefaultThreshold
			}
			keep := l.cfg.MicroCompactKeep
			if keep == 0 {
				keep = microcompact.DefaultKeepRecent
			}
			triggerTime := !lastAssistantTime.IsZero() && time.Since(lastAssistantTime) >= gap
			triggerTokens := false
			if lastInputTokens > 0 {
				cw := contextWindow
				if cw <= 0 {
					cw = internalmodel.ContextWindowFor(model)
				}
				triggerTokens = lastInputTokens > cw*internalmodel.MicroCompactThresholdPct/100
			}
			if triggerTime || triggerTokens {
				if r := microcompact.Apply(msgs, lastAssistantTime, gap, keep); r.Triggered {
					msgs = r.Messages
				}
			}
		}

		reqSystem := system
		if turn == 1 && sessionReminderBlock != nil {
			if firstReminder := l.consumeSessionReminderBlock(); firstReminder != nil {
				reqSystem = append(append([]api.SystemBlock(nil), system...), *firstReminder)
			}
		}
		// For Claude.ai OAuth (Max subscription) accounts, replace the static
		// billing block (index 0) with a dynamically computed one using the
		// SHA256 of the first user message. Other account types are unaffected.
		if l.cfg.IsOAuthSubscription && len(reqSystem) > 0 {
			if firstMsg := firstUserMessageText(msgs); firstMsg != "" {
				updated := append([]api.SystemBlock(nil), reqSystem...)
				updated[0] = DynamicBillingBlock(firstMsg)
				reqSystem = updated
			}
		}
		// Add cache_control breakpoints on the last N non-system messages.
		// Anthropic allows 4 total cache breakpoints across system + tools + history.
		// Count how many are already consumed by system blocks and the tool list so
		// we don't exceed the limit. Work on a shallow-copy so the stored msgs slice
		// is not mutated.
		priorBP := countSystemBreakpoints(reqSystem, tools)
		reqMsgs := applyHistoryBreakpoints(msgs, priorBP)

		// Detect silent cache busts: warn if the cached-prefix content changes
		// between turns. This is advisory-only — the request always proceeds.
		if sum := hashCachedPrefix(reqSystem, tools, reqMsgs); sum != 0 {
			// lastCachedPrefixHash is zero on the first turn — no prior cache
			// exists, so skip the comparison to avoid a false-positive warning.
			if l.lastCachedPrefixHash != 0 && sum != l.lastCachedPrefixHash {
				log.Printf("agent: cached prefix changed between turns (potential cache miss)")
			}
			l.lastCachedPrefixHash = sum
		}

		req := &api.MessageRequest{
			Model:     model,
			MaxTokens: l.cfg.MaxTokens,
			System:    reqSystem,
			Messages:  reqMsgs,
			Stream:    true,
			Tools:     tools,
			Metadata:  l.cfg.Metadata,
		}
		if thinkingBudget > 0 {
			req.Thinking = &api.ThinkingConfig{
				Type:         "enabled",
				BudgetTokens: thinkingBudget,
			}
		}

		// Pre-flight token estimate: len(JSON bytes) / 4 is a cheap proxy for
		// token count. If it exceeds the model's context window, compact before
		// sending — a single-turn blowup would hit a hard API error otherwise.
		if l.cfg.MicroCompact {
			if raw, err := json.Marshal(req.Messages); err == nil {
				estimatedTokens := len(raw) / 4
				cw := contextWindow
				if cw <= 0 {
					cw = internalmodel.ContextWindowFor(model)
				}
				if estimatedTokens > cw {
					keep := l.cfg.MicroCompactKeep
					if keep == 0 {
						keep = microcompact.DefaultKeepRecent
					}
					// Seed a non-zero timestamp so Apply doesn't short-circuit,
					// and use a 0 gap so any elapsed time triggers compaction.
					seed := lastAssistantTime
					if seed.IsZero() {
						seed = time.Now().Add(-time.Hour)
					}
					if r := microcompact.Apply(msgs, seed, 0, keep); r.Triggered {
						msgs = r.Messages
						reqMsgs = applyHistoryBreakpoints(msgs, priorBP)
						req.Messages = reqMsgs
					}
				}
			}
		}

		streamCtx := api.WithRetryHandler(ctx, func(ev api.RetryEvent) bool {
			// Fire on a goroutine so a slow/blocked TUI channel can't stall
			// the retry sleep. EventAPIRetry is informational; order doesn't matter.
			go handler(LoopEvent{
				Type:             EventAPIRetry,
				RetryAttempt:     ev.Attempt,
				RetryDelay:       ev.Delay,
				RetryAfter:       ev.RetryAfter,
				RateLimitResetAt: ev.ResetAt,
				RetryErr:         ev.Err,
			})
			// Short 429s are usually transient. Long retry-after values are
			// plan/quota exhaustion; surfacing the error is clearer than
			// leaving the user watching a spinner for minutes or hours.
			return ev.Delay <= 2*time.Minute
		})
		stream, err := client.StreamMessage(streamCtx, req)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return msgs, err
			}
			if toolUseRecoveries < maxToolUseRecoveries && isToolUseFlowError(err) {
				if recovered, ok := recoverToolUseHistory(msgs); ok {
					toolUseRecoveries++
					msgs = recovered
					handler(LoopEvent{Type: EventAPIRetry, RetryAttempt: toolUseRecoveries, RetryErr: err})
					continue
				}
			}
			streamFailures++
			if streamFailures > 3 {
				return msgs, fmt.Errorf("agent: stream: %w", err)
			}
			// Compact before resending — avoids amplifying a large payload on each retry.
			msgs = retryCompact(msgs, lastAssistantTime, contextWindow, model)
			handler(LoopEvent{Type: EventAPIRetry, RetryAttempt: streamFailures, RetryErr: err})
			continue
		}
		streamFailures = 0
		toolUseRecoveries = 0

		// Emit rate-limit info from response headers before draining.
		if rlInfo := ratelimit.Parse(stream.ResponseHeader); rlInfo.HasData() {
			handler(LoopEvent{
				Type:             EventRateLimit,
				RateLimitInfo:    rlInfo,
				RateLimitWarning: rlInfo.WarningMessage(),
			})
		}

		assistantBlocks, _, usage, err := l.drainStream(ctx, stream, handler)
		inputTokens := usage.PromptInputTokens()
		if inputTokens > 0 {
			lastInputTokens = inputTokens
		}
		_ = stream.Close()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				// Conversation recovery: emit any partial assistant content the
				// caller can persist to the session JSONL before the error
				// propagates. On /resume the loaded history will pass through
				// FilterUnresolvedToolUses to drop any orphan tool_use blocks.
				if len(assistantBlocks) > 0 {
					handler(LoopEvent{
						Type:          EventPartial,
						PartialBlocks: assistantBlocks,
						PartialErr:    err,
					})
					msgs = append(msgs, api.Message{Role: "assistant", Content: assistantBlocks})
				}
				return msgs, err
			}
			// Non-cancellation drain error — retry up to 3 times before giving up.
			// Don't persist partial blocks; the next attempt may succeed cleanly.
			streamFailures++
			if streamFailures > 3 {
				if len(assistantBlocks) > 0 {
					handler(LoopEvent{
						Type:          EventPartial,
						PartialBlocks: assistantBlocks,
						PartialErr:    err,
					})
					msgs = append(msgs, api.Message{Role: "assistant", Content: assistantBlocks})
				}
				return msgs, fmt.Errorf("agent: drain: %w", err)
			}
			// Compact before resending — avoids amplifying a large payload on each retry.
			msgs = retryCompact(msgs, lastAssistantTime, contextWindow, model)
			handler(LoopEvent{Type: EventAPIRetry, RetryAttempt: streamFailures, RetryErr: err})
			continue
		}
		streamFailures = 0

		// Append the assistant message to history.
		msgs = append(msgs, api.Message{
			Role:    "assistant",
			Content: assistantBlocks,
		})
		lastAssistantTime = time.Now()

		if !hasToolUse(assistantBlocks) {
			// end_turn or unknown — we're done.
			// Auto-compact check: if context is approaching capacity, compact
			// so future turns don't hit the limit. Non-fatal if it fails.
			if l.cfg.AutoCompact && inputTokens > 0 && os.Getenv("DISABLE_AUTO_COMPACT") == "" {
				threshold := internalmodel.AutoCompactThresholdFor(model)
				if contextWindow > 0 {
					threshold = internalmodel.AutoCompactThresholdForWindow(contextWindow)
				}
				if inputTokens > threshold && l.consecutiveCompactFails < internalmodel.MaxConsecutiveCompactFail {
					if result, err := compact.CompactWithModel(ctx, l.client, model, msgs, ""); err == nil {
						msgs = result.NewHistory
						l.consecutiveCompactFails = 0
						if l.cfg.OnCompact != nil && result.Summary != "" {
							l.cfg.OnCompact(result.Summary)
						}
						handler(LoopEvent{Type: EventCompacted, CompactedInputTokens: inputTokens, CompactedThreshold: threshold})
					} else {
						l.consecutiveCompactFails++
					}
				}
			}
			// Desktop notification on turn complete. Title carries the
			// project name so users running multiple conduit windows can
			// tell which one finished. Body is short so the notification
			// renders fully on macOS Banner / Linux notify-send.
			if l.cfg.NotifyOnComplete {
				hooks.Notify("conduit · ready", "Your turn.")
			}
			// Post-Stop hook for memory extraction etc. Caller is responsible
			// for single-flighting / backgrounding — Loop just notifies.
			if l.cfg.OnEndTurn != nil {
				l.cfg.OnEndTurn(msgs)
			}
			// Periodic background memory/skill review. Fire-and-forget;
			// the reviewer manages its own goroutines and single-flight logic.
			if l.cfg.BackgroundReviewer != nil {
				l.cfg.BackgroundReviewer.OnEndTurn(ctx)
			}
			return msgs, nil
		}

		// Execute tools, build a user message with all tool_results.
		toolResults, stopTurn, err := l.executeTools(ctx, assistantBlocks, handler)
		if err != nil {
			return msgs, fmt.Errorf("agent: execute tools: %w", err)
		}
		msgs = append(msgs, api.Message{
			Role:    "user",
			Content: toolResults,
		})

		// A tool requested that control return to the user (e.g. ExitPlanMode
		// "discuss", AskUserQuestion dismiss). Treat it like end_turn: the
		// assistant tool_use + user tool_result are both in history, so the
		// conversation is valid. The TUI re-submits with the user's next message.
		if stopTurn {
			// Mirror the end_turn notification: StopTurn hands the turn back to the
			// user, so the "ready" signal is equally applicable here.
			if l.cfg.NotifyOnComplete {
				hooks.Notify("conduit · ready", "Your turn.")
			}
			if l.cfg.OnEndTurn != nil {
				l.cfg.OnEndTurn(msgs)
			}
			if l.cfg.BackgroundReviewer != nil {
				l.cfg.BackgroundReviewer.OnEndTurn(ctx)
			}
			return msgs, nil
		}

		// Mid-turn pause hook (acceptEditsLive mode). If the callback signals
		// that the user wants to review now, treat this as an end_turn: run
		// OnEndTurn then return. The TUI re-submits the updated history after
		// the review overlay closes and any follow-up message is enqueued.
		// pendingEdits is passed as 0 — the callback owns the pending-edits
		// table and reads Len() itself; the arg is reserved for future use.
		if l.cfg.OnToolBatchComplete != nil {
			if pause := l.cfg.OnToolBatchComplete(0); pause {
				if l.cfg.OnEndTurn != nil {
					l.cfg.OnEndTurn(msgs)
				}
				if l.cfg.BackgroundReviewer != nil {
					l.cfg.BackgroundReviewer.OnEndTurn(ctx)
				}
				return msgs, nil
			}
		}

		// Steering injection: if the user sent a message while tools were
		// running, append it as a user turn now so the model sees it before
		// the next API call — without interrupting the current turn.
		if v := l.steerMsg.Swap(""); v != nil {
			if text, _ := v.(string); text != "" {
				msgs = append(msgs, api.Message{
					Role:    "user",
					Content: []api.ContentBlock{{Type: "text", Text: text}},
				})
				handler(LoopEvent{Type: EventText, Text: ""}) // wake TUI to reflect history update
			}
		}

	}
}

func hasToolUse(blocks []api.ContentBlock) bool {
	for _, block := range blocks {
		if block.Type == "tool_use" {
			return true
		}
	}
	return false
}

// firstUserMessageText returns the text content of the first user message in
// the conversation. Used to compute the dynamic billing suffix for OAuth accounts.
func firstUserMessageText(msgs []api.Message) string {
	for _, m := range msgs {
		if m.Role != "user" {
			continue
		}
		for _, blk := range m.Content {
			if blk.Type == "text" && blk.Text != "" {
				return blk.Text
			}
		}
	}
	return ""
}

// maxCacheBreakpoints is Anthropic's hard limit on cache_control breakpoints
// per request, counting system blocks + tool list + message history combined.
const maxCacheBreakpoints = 4

// applyHistoryBreakpoints returns a shallow copy of msgs with cache_control
// breakpoints set on the last content block of the last N messages, where N
// is determined by the remaining budget after accounting for breakpoints already
// consumed by system blocks and the tool list.
//
// Anthropic allows 4 cache breakpoints per request total. System blocks and the
// tool list each consume some of that budget. If the total would exceed 4, a
// warning is logged and history breakpoints are trimmed from the oldest first
// (system block breakpoints take priority over history breakpoints).
//
// The function never mutates the input slice or any existing ContentBlock.
func applyHistoryBreakpoints(msgs []api.Message, priorBreakpoints int) []api.Message {
	if len(msgs) == 0 {
		return msgs
	}
	ephemeral := &api.CacheControl{Type: "ephemeral"}

	// How many history breakpoints can we place within the limit?
	const wantHistory = 2
	budget := maxCacheBreakpoints - priorBreakpoints
	if budget <= 0 {
		return msgs
	}
	allowed := min(budget, wantHistory)
	if priorBreakpoints+allowed > maxCacheBreakpoints {
		allowed = maxCacheBreakpoints - priorBreakpoints
	}

	// Find indices of the last `allowed` messages (in reverse).
	targets := make([]int, 0, allowed)
	for i := len(msgs) - 1; i >= 0 && len(targets) < allowed; i-- {
		if len(msgs[i].Content) > 0 {
			targets = append(targets, i)
		}
	}
	if len(targets) == 0 {
		return msgs
	}

	// Build a shallow copy of the slice.
	out := make([]api.Message, len(msgs))
	copy(out, msgs)

	for _, idx := range targets {
		m := out[idx]
		// Shallow-copy the content slice for this message.
		newContent := make([]api.ContentBlock, len(m.Content))
		copy(newContent, m.Content)
		// Set cache_control on the last block.
		last := len(newContent) - 1
		newContent[last].CacheControl = ephemeral
		out[idx].Content = newContent
	}
	return out
}

// retryCompact applies microcompact to the history before a retry so the
// re-sent payload is smaller than the payload that just failed. Only runs when
// the estimated message size exceeds a quarter of the context window; tiny
// histories are left unchanged.
func retryCompact(msgs []api.Message, seed time.Time, contextWindow int, model string) []api.Message {
	raw, err := json.Marshal(msgs)
	if err != nil {
		return msgs
	}
	estimated := len(raw) / 4
	cw := contextWindow
	if cw <= 0 {
		cw = internalmodel.ContextWindowFor(model)
	}
	if estimated < cw/4 {
		return msgs
	}
	if seed.IsZero() {
		seed = time.Now().Add(-time.Hour)
	}
	if r := microcompact.Apply(msgs, seed, 0, microcompact.DefaultKeepRecent); r.Triggered {
		return r.Messages
	}
	return msgs
}

// hashCachedPrefix computes a FNV-1a hash of the content that will be cached
// by Anthropic's prompt cache for this turn: system blocks with cache_control
// set, the tool definitions list, and history messages with cache_control set.
// Returns 0 if JSON marshalling fails so callers can safely skip on error.
func hashCachedPrefix(system []api.SystemBlock, tools []api.ToolDef, msgs []api.Message) uint64 {
	h := fnv.New64a()

	// Hash cached system blocks.
	for i := range system {
		if system[i].CacheControl == nil {
			continue
		}
		b, err := json.Marshal(system[i])
		if err != nil {
			return 0
		}
		h.Write(b)
	}

	// Hash tool definitions (always part of the cached prefix when non-empty).
	if len(tools) > 0 {
		b, err := json.Marshal(tools)
		if err != nil {
			return 0
		}
		h.Write(b)
	}

	// Hash cached history messages.
	for _, msg := range msgs {
		for _, block := range msg.Content {
			if block.CacheControl == nil {
				continue
			}
			b, err := json.Marshal(block)
			if err != nil {
				return 0
			}
			h.Write(b)
		}
	}

	return h.Sum64()
}

// countSystemBreakpoints returns the number of cache_control breakpoints set
// on the system blocks and tools list that will be sent in the request.
func countSystemBreakpoints(system []api.SystemBlock, tools []api.ToolDef) int {
	n := 0
	for i := range system {
		if system[i].CacheControl != nil {
			n++
		}
	}
	for i := range tools {
		if tools[i].CacheControl != nil {
			n++
		}
	}
	return n
}
