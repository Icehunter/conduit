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
	"strings"
	"sync"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/hooks"
	"github.com/icehunter/conduit/internal/microcompact"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/ratelimit"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
)

// maxConcurrentTools is the worker pool size for parallel tool execution.
// Mirrors the coordinator's concurrency limit in src/coordinator/coordinatorMode.ts.
const maxConcurrentTools = 4

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
	RetryAttempt int
	RetryDelay   time.Duration
	RetryErr     error

	// EventPartial — fired before a stream error bubbles up so callers
	// can persist whatever assistant content was streamed before the
	// failure. The blocks here are already filtered (empty text/truncated
	// tool_use dropped by buildContentBlocks).
	PartialBlocks []api.ContentBlock
	PartialErr    error
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
	// "always allow" rules to <cwd>/.claude/settings.local.json.
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

	// OnEndTurn fires after each end_turn (no tool_use) with the up-to-date
	// message history. Mirrors CC's post-Stop extractMemories trigger. The
	// caller is expected to single-flight any background work — Loop fires
	// this synchronously before returning so the caller can choose between
	// blocking or detaching to a goroutine.
	OnEndTurn func(history []api.Message)

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
}

// Loop drives the agentic query cycle.
type Loop struct {
	mu     sync.RWMutex
	client *api.Client
	reg    *tool.Registry
	cfg    LoopConfig
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

// SetModel updates the model used for new requests (from /model slash command).
func (l *Loop) SetModel(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg.Model = name
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

	// Snapshot mutable fields under the read lock so that concurrent Set*
	// calls from the TUI goroutine cannot race with this turn's reads.
	l.mu.RLock()
	model := l.cfg.Model
	system := l.cfg.System
	thinkingBudget := l.cfg.ThinkingBudget
	client := l.client
	l.mu.RUnlock()

	// Fire SessionStart hooks once before the first turn.
	if l.cfg.Hooks != nil && len(l.cfg.Hooks.SessionStart) > 0 {
		hooks.RunSessionStart(ctx, l.cfg.Hooks.SessionStart, l.cfg.SessionID)
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
	for {
		if ctx.Err() != nil {
			return msgs, ctx.Err()
		}
		if l.cfg.MaxTurns > 0 && turn >= l.cfg.MaxTurns {
			return msgs, nil
		}
		turn++

		// Time-based microcompact: after a long idle (cache expired), shrink
		// older tool_results to a placeholder so the re-cache is cheaper.
		if l.cfg.MicroCompact && !lastAssistantTime.IsZero() {
			gap := l.cfg.MicroCompactGap
			if gap == 0 {
				gap = microcompact.DefaultThreshold
			}
			keep := l.cfg.MicroCompactKeep
			if keep == 0 {
				keep = microcompact.DefaultKeepRecent
			}
			if r := microcompact.Apply(msgs, lastAssistantTime, gap, keep); r.Triggered {
				msgs = r.Messages
			}
		}

		req := &api.MessageRequest{
			Model:     model,
			MaxTokens: l.cfg.MaxTokens,
			System:    system,
			Messages:  msgs,
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

		streamCtx := api.WithRetryHandler(ctx, func(ev api.RetryEvent) bool {
			// Fire on a goroutine so a slow/blocked TUI channel can't stall
			// the retry sleep. EventAPIRetry is informational; order doesn't matter.
			go handler(LoopEvent{
				Type:         EventAPIRetry,
				RetryAttempt: ev.Attempt,
				RetryDelay:   ev.Delay,
				RetryErr:     ev.Err,
			})
			// Short 429s are usually transient. Long retry-after values are
			// plan/quota exhaustion; surfacing the error is clearer than
			// leaving the user watching a spinner for minutes or hours.
			return ev.Delay <= 2*time.Minute
		})
		stream, err := client.StreamMessage(streamCtx, req)
		if err != nil {
			return msgs, fmt.Errorf("agent: stream: %w", err)
		}

		// Emit rate-limit info from response headers before draining.
		if rlInfo := ratelimit.Parse(stream.ResponseHeader); rlInfo.HasData() {
			handler(LoopEvent{
				Type:             EventRateLimit,
				RateLimitInfo:    rlInfo,
				RateLimitWarning: rlInfo.WarningMessage(),
			})
		}

		assistantBlocks, stopReason, inputTokens, err := l.drainStream(ctx, stream, handler)
		_ = stream.Close()
		if err != nil {
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
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return msgs, err
			}
			return msgs, fmt.Errorf("agent: drain: %w", err)
		}

		// Append the assistant message to history.
		msgs = append(msgs, api.Message{
			Role:    "assistant",
			Content: assistantBlocks,
		})
		lastAssistantTime = time.Now()

		if stopReason != "tool_use" {
			// end_turn or unknown — we're done.
			// Auto-compact check: if context is approaching capacity, compact
			// so future turns don't hit the limit. Non-fatal if it fails.
			if l.cfg.AutoCompact && l.cfg.MaxTokens > 0 && inputTokens > 0 {
				threshold := int(float64(l.cfg.MaxTokens) * 0.8)
				if inputTokens > threshold {
					if result, err := compact.CompactWithModel(ctx, l.client, l.BackgroundModel(), msgs, ""); err == nil {
						msgs = result.NewHistory
						if l.cfg.OnCompact != nil && result.Summary != "" {
							l.cfg.OnCompact(result.Summary)
						}
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
			return msgs, nil
		}

		// Execute tools, build a user message with all tool_results.
		toolResults, err := l.executeTools(ctx, assistantBlocks, handler)
		if err != nil {
			return msgs, fmt.Errorf("agent: execute tools: %w", err)
		}
		msgs = append(msgs, api.Message{
			Role:    "user",
			Content: toolResults,
		})

		// Auto-compact after tool_results are appended. By waiting until both
		// the assistant tool_use message and the user tool_result message are in
		// msgs, compact can summarize the complete pair — no orphaned IDs.
		if l.cfg.AutoCompact && l.cfg.MaxTokens > 0 && inputTokens > 0 {
			threshold := int(float64(l.cfg.MaxTokens) * 0.8)
			if inputTokens > threshold {
				if result, err := compact.CompactWithModel(ctx, l.client, l.BackgroundModel(), msgs, ""); err == nil {
					msgs = result.NewHistory
					if l.cfg.OnCompact != nil && result.Summary != "" {
						l.cfg.OnCompact(result.Summary)
					}
				}
			}
		}
	}
}
