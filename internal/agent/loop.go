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
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/healthcheck"
	"github.com/icehunter/conduit/internal/hooks"
	"github.com/icehunter/conduit/internal/microcompact"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/providerrotation"
	"github.com/icehunter/conduit/internal/ratelimit"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/tools/tasktool"
	"github.com/icehunter/conduit/internal/ttsr"
)

// maxConcurrentTools is the worker pool size for parallel tool execution.
// Mirrors the coordinator's concurrency limit in src/coordinator/coordinatorMode.ts.
const maxConcurrentTools = 4

// DefaultMainMaxTurns is the per-prompt turn cap for the primary interactive
// loop. Generous enough for complex multi-file tasks; still a runaway backstop.
const DefaultMainMaxTurns = 200

// DefaultSubAgentMaxTurns is applied to child loops whose MaxTurns was 0
// (unbounded). Sub-agents are background workers and are kept tighter.
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
	EventText             EventType = iota // a text delta streamed from the model
	EventToolStart                         // tool_use block started streaming; name known, input pending
	EventToolUse                           // tool_use block complete; tool is about to run
	EventToolResult                        // tool execution finished
	EventRateLimit                         // rate-limit headers received; RateLimitWarning may be non-empty
	EventAPIRetry                          // API returned 429 and the client is backing off
	EventPartial                           // stream errored mid-turn; PartialBlocks holds what was received
	EventUsage                             // per-API-turn usage from SSE (input + output + cache fields)
	EventCost                              // running cost estimate (emitted on each message_delta with output tokens)
	EventCompacted                         // history was compacted; Compact* fields and History carry the result
	EventProviderFailover                  // a provider returned 429/503/529 and the loop switched to the next one
	EventTTSR                              // a TTSR rule fired; TTSRRule and TTSRCorrection carry details
	EventNotice                            // informational message for the user; Notice carries the text
	EventThinking                          // a reasoning/thinking delta; Text carries it (never part of the answer)
	EventSessionTitle                      // a UserPromptSubmit hook renamed the session; Notice carries the title
	EventCompletion                        // optional verified-coding lifecycle; CompletionStatus carries the outcome
)

// LoopEvent is emitted to the caller's handler on each significant event.
type LoopEvent struct {
	Type              EventType
	CompletionStatus  CompletionStatus
	CompletionAttempt int

	// EventText, EventThinking
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
	// RetryReason is the api.Reason* tag of RetryErr; RetryStatus the HTTP
	// status (0 for a network or stream failure). RetryMax is the number of
	// retries the layer makes. RetryStream is true when the retry restarts a
	// response that had already begun streaming, so the caller must drop the
	// failed attempt's text.
	RetryReason string
	RetryStatus int
	RetryMax    int
	RetryStream bool

	// EventPartial — fired when the user cancels mid-stream, with whatever
	// assistant content had streamed. The same blocks are appended to the
	// history Run returns, so callers persist that history rather than this
	// event. It is no longer fired when retries run out.
	PartialBlocks []api.ContentBlock
	PartialErr    error

	// EventNotice
	Notice string

	// EventUsage carries the API-reported usage for one streamed turn.
	Usage api.Usage

	// EventCost carries the running cost estimate.
	CostUSD float64

	// EventCompacted. CompactedInputTokens and CompactedThreshold are set for
	// an auto compaction only. History is a copy of the replacement history
	// (summary message first, then the verbatim tail of CompactTailLen
	// messages); the handler persists it before the loop continues.
	CompactKind          string // compact.KindAuto or compact.KindReactive
	CompactSummary       string
	CompactModel         string // model that wrote the summary; "local" for the local fallback
	CompactTailLen       int
	CompactPreLen        int // length of the history that was compacted
	CompactedInputTokens int
	CompactedThreshold   int
	History              []api.Message

	// EventProviderFailover
	FailoverErr      error
	FailoverProvider string

	// EventTTSR — fired when a TTSR rule aborts the stream.
	TTSRRule       string // name of the TTSR rule that fired
	TTSRCorrection string // correction text injected as a <system-reminder>
}

// SubAgentPermVerdict is the outcome of one permission decision bubbled up
// from a sub-agent via AskSubAgentPermission.
type SubAgentPermVerdict int

const (
	// SubAgentPermDeny denies this one tool call.
	SubAgentPermDeny SubAgentPermVerdict = iota
	// SubAgentPermAllowOnce allows this one tool call only.
	SubAgentPermAllowOnce
	// SubAgentPermSwitchToAuto allows this call and switches the asking
	// sub-agent's own gate to bypassPermissions for the rest of its run,
	// subject to the same escalation ceiling permissions.Gate.SetMode always
	// enforces on a cloned gate.
	SubAgentPermSwitchToAuto
)

// LoopConfig controls the loop's behaviour.
type LoopConfig struct {
	Model     string
	MaxTokens int
	System    []api.SystemBlock
	// RebuildSystem regenerates System from current on-disk state (memory,
	// skills, CLAUDE.md). Optional; when nil the blocks never change.
	//
	// Run snapshots System once, so without this anything written during a
	// session — notably memdir.RunExtract — stays invisible to the running
	// conversation. Called after compaction, and by RefreshSystem.
	RebuildSystem func() []api.SystemBlock
	// WaitForTools, when set, runs at the start of every Run, before the
	// system prompt and the tool list are snapshotted, so tool sources still
	// starting in the background (MCP servers connecting at startup) can make
	// it into the prompt. It must return promptly once they have settled or
	// its own budget runs out; an error ends the Run.
	WaitForTools func(ctx context.Context) error
	Metadata     map[string]any
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

	// TranscriptPath, when set, returns the session file path for the
	// transcript_path field of hook input.
	TranscriptPath func() string

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
	// nil (always true for sub-agents — see runSubAgentWithModel) falls back
	// to AskSubAgentPermission if set, then to silent-allow (except Plan mode,
	// which denies writes) if not.
	AskPermission func(ctx context.Context, toolName, toolInput string) (allow, alwaysAllow bool)

	// AskSubAgentPermission bubbles a permission decision from a sub-agent —
	// which never has its own AskPermission — up to whichever ancestor loop
	// does have an interactive session attached. label identifies which
	// sub-agent is asking (see Loop.subAgentLabel). Inherited unchanged by
	// child LoopConfigs, so it chains through nested sub-agents automatically.
	// nil means no ancestor can answer (e.g. headless/print mode): falls back
	// to the pre-existing silent-allow-except-plan-mode behavior.
	AskSubAgentPermission func(ctx context.Context, label, toolName, toolInput string) SubAgentPermVerdict

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
	// Completion is an optional machine-enforced completion gate. Nil retains
	// legacy streaming and completion behavior. Never inherited by helpers.
	Completion *CompletionConfig

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

	// Sleep waits for retry backoff, in both the API layer and the loop. nil
	// means a real sleep that returns early when ctx is done. Tests inject a
	// recorder so retries do not wait.
	Sleep func(ctx context.Context, d time.Duration) error

	// AutoCompactTriggerCap returns the ceiling on the auto-compaction trigger
	// in tokens; 0 or less means no cap. nil uses
	// internalmodel.DefaultAutoCompactTriggerCap. Called on every end_turn, so
	// edits to conduit.json apply without a restart.
	AutoCompactTriggerCap func() int

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

	// RoleResolver resolves a named role (e.g. "planning", "implement") to a
	// model string and an optional API client. The client may be nil when the
	// role maps to the same provider as the parent loop — in that case the
	// parent's client is reused. Returns ok=false when the role is not
	// configured; the caller falls back to BackgroundModel().
	RoleResolver func(role string) (model string, client *api.Client, ok bool)
	// ResolveRole is strict role resolution, preferred over RoleResolver for
	// explicit child roles. Errors prevent dispatch rather than inheriting a
	// different provider's client. A successful result requires model+client.
	ResolveRole func(role string) (model string, client *api.Client, err error)
	// RoleContextWindow returns the context window configured for the role's
	// provider, 0 when unknown. It sizes the summarizer's input when the
	// background role's own client writes the summary.
	RoleContextWindow func(role string) int
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
		OnEndTurn(ctx context.Context, history []api.Message)
	}

	// TTSRRules are mid-stream interruption rules. When a rule's pattern matches
	// the model's streaming text output, the stream is aborted and the rule's
	// correction is injected as a user <system-reminder> before the next attempt.
	// Rules are evaluated per-turn; fire counts reset at the start of each turn.
	TTSRRules []ttsr.Rule

	// RepetitionGuard, when it returns true (nil means true), interrupts a
	// response whose text or thinking has degenerated into a repeating loop.
	// Read once per Run.
	RepetitionGuard func() bool

	// RedactSecrets, when it returns false, turns off masking of high-confidence
	// secrets in tool results (nil means on). Read once per tool batch.
	RedactSecrets func() bool

	// TaskStore is the task store used by SpawnTeammate for self-claim logic.
	// nil uses tasktool.GlobalStore(). Set in tests to isolate task state.
	TaskStore *tasktool.Store
}

// Loop drives the agentic query cycle.
type Loop struct {
	mu sync.RWMutex

	// systemOverlay is appended after cfg.System on every request; see
	// SetSystemOverlay. Guarded by mu.
	systemOverlay []api.SystemBlock
	// modeDirective is the plan/council directive alone. Children inherit it,
	// only while their own gate is in plan or council mode; the output style
	// and the rest of systemOverlay never reach them. Guarded by mu.
	modeDirective []api.SystemBlock
	client        *api.Client
	reg           *tool.Registry
	cfg           LoopConfig
	// subAgentLabel identifies this loop when it is itself a sub-agent — set
	// by every child-construction site (runSubAgentWithModel,
	// runSubAgentOnce, SpawnTeammate), each right after building the child
	// via a *Loop literal or buildChildLoop. Used when bubbling one of this
	// loop's own children's Ask decisions via cfg.AskSubAgentPermission.
	// Empty for the root loop.
	subAgentLabel string
	// promptID is the cc_prompt_id for the user prompt currently being
	// served: a fresh UUID per Run() on a root loop, or the parent's at spawn
	// time for a child (parentPromptID). lastRequestID is the `request-id`
	// response header from the most recent successful stream, sent back as
	// cc_prev_req. Both live in the billing system block for OAuth accounts.
	promptID       string
	parentPromptID string
	lastRequestID  string
	// Wire identity for the x-claude-code-* request hint headers (see
	// api.WireHints). All empty on the root loop, which sends class "main".
	// Set once at child construction, before the child runs.
	agentID       string
	parentAgentID string
	agentType     string
	requestClass  string
	// pendingContextCompacted ("auto" or "manual") marks the next root-loop
	// prompt's first request as the first after a compaction. Guarded by mu.
	pendingContextCompacted string
	// Provider failover: chainResolver returns the ordered provider chain for a
	// given role; newClientFor builds a fresh API client from a provider.
	// Both are nil when failover is not configured (default behaviour unchanged).
	chainResolver func(role string) []settings.ActiveProviderSettings
	newClientFor  func(settings.ActiveProviderSettings) (*api.Client, error)
	// providerRotation tracks per-provider cooldowns for this Loop's lifetime.
	// Non-nil only when chainResolver is set.
	providerRotation *providerrotation.State
	// role is the role used to look up the provider chain.
	role string
	// compactFails counts compactions that failed in a row for a reason other
	// than a transient one (network, throttling, cancel). It trips the
	// MAX_CONSECUTIVE_AUTOCOMPACT_FAILURES breaker in autoCompact.ts and
	// persists across Run() calls. Reset on success, SetModel and SetClient.
	compactFails atomic.Int32
	// compactStuck is the prompt-token estimate an auto-compaction left above
	// the trigger (0 when none did). Compacting again at the next end_turn
	// would only repeat itself, so it waits for the prompt to grow past it or
	// to fall under the trigger. Reset by SetModel, SetClient and SetSessionID.
	compactStuck atomic.Int64
	// steerMsg holds an optional user steering message to inject between
	// tool-call rounds. Written by InjectSteerContent (TUI goroutine), read
	// and cleared by Run (loop goroutine) via atomic.Pointer so no extra lock
	// is needed. Semantics: last-write-wins (earlier messages are superseded).
	steerMsg atomic.Pointer[[]api.ContentBlock]
	// child marks a loop built for a sub-agent, teammate or side question. It
	// never runs UserPromptSubmit hooks: its prompt is not the user's.
	child bool
	// msgQueue is a per-loop ordered queue for programmatic injection (e.g.
	// agent team mailbox delivery). All messages are preserved in insertion
	// order and delivered together at the next turn boundary. Guarded by
	// msgMu; separate from steerMsg so human-steering semantics are unchanged.
	msgQueue []string
	msgMu    sync.Mutex
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
	// toolDefs, when set, are sent instead of the registry's definitions. A
	// /btw side question uses it to carry the parent's tools while its own
	// registry is empty, so the request prefix matches the parent's and the
	// history's tool_use blocks have their definitions. Set before Run.
	toolDefs []api.ToolDef
	// ttsrMem remembers TTSR rule fires across Runs for Once/Gap gating.
	ttsrMem ttsr.Memory
}

// NewLoop constructs a Loop.
func NewLoop(client *api.Client, reg *tool.Registry, cfg LoopConfig) *Loop {
	l := &Loop{client: client, reg: reg, cfg: cfg}
	// Wire the background runner into the hooks package so prompt/agent hooks
	// can spawn helper LLM calls. Stored on DefaultAsyncGroup to avoid a
	// package-level mutable global.
	if hooks.DefaultAsyncGroup != nil {
		hooks.DefaultAsyncGroup.SubAgentRunner = l.RunBackgroundAgent
		hooks.DefaultAsyncGroup.AgentHookRunner = l.RunBackgroundSubAgent
	}
	return l
}

// InjectSteerMessage queues a text-only user steering message. See
// InjectSteerContent.
func (l *Loop) InjectSteerMessage(text string) {
	l.InjectSteerContent([]api.ContentBlock{{Type: "text", Text: text}})
}

// InjectSteerContent queues a user steering message to be injected into the
// conversation between the current tool-call batch and the next API request.
// Content may carry images and documents alongside text. Safe to call from
// any goroutine. Only the most recent call takes effect per batch — if the
// user sends multiple messages quickly, the last one wins (earlier ones were
// superseded). The loop clears the slot after consuming it.
func (l *Loop) InjectSteerContent(content []api.ContentBlock) {
	if len(content) == 0 {
		return
	}
	l.steerMsg.Store(&content)
}

// InjectMessage appends text to the per-loop programmatic delivery queue.
// Unlike InjectSteerMessage (last-write-wins), all injected messages are
// preserved in insertion order and delivered together at the next turn
// boundary. Use this for agent-team mailbox delivery — every message must
// arrive. Safe to call from any goroutine.
func (l *Loop) InjectMessage(text string) {
	l.msgMu.Lock()
	l.msgQueue = append(l.msgQueue, text)
	l.msgMu.Unlock()
}

// DrainInjected removes and returns the messages InjectMessage queued that no
// turn has delivered yet. A host calls it when the loop is idle to start a
// turn for notifications that arrived after the last delivery point.
func (l *Loop) DrainInjected() []string {
	l.msgMu.Lock()
	defer l.msgMu.Unlock()
	queued := l.msgQueue
	l.msgQueue = nil
	return queued
}

// HasInjected reports whether InjectMessage has queued messages waiting.
func (l *Loop) HasInjected() bool {
	l.msgMu.Lock()
	defer l.msgMu.Unlock()
	return len(l.msgQueue) > 0
}

// SetBackgroundReviewer wires a background review scheduler into the loop.
// It is safe to call after NewLoop (e.g., after the sub-agent runner is
// available). A nil value disables background reviews.
func (l *Loop) SetBackgroundReviewer(r interface {
	OnEndTurn(ctx context.Context, history []api.Message)
}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg.BackgroundReviewer = r
}

// SetModel updates the model used for new requests (from /model slash command).
func (l *Loop) SetModel(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg.Model = name
	l.compactFails.Store(0)
	l.compactStuck.Store(0)
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

// SetMaxTurns updates the per-prompt turn cap. 0 means unlimited.
func (l *Loop) SetMaxTurns(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg.MaxTurns = n
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

// SetSystemOverlay sets blocks appended after the base system blocks on every
// request. The base is rebuilt each Run by RefreshSystem; the overlay is not,
// so caller-owned blocks (output style, plan-mode directive) survive. The
// overlay must stay after the base: the Claude Code fingerprint requires
// system[0..3] in exact order.
func (l *Loop) SetSystemOverlay(blocks []api.SystemBlock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.systemOverlay = append([]api.SystemBlock(nil), blocks...)
}

// SetModeDirective sets the plan/council directive child loops inherit when
// their own gate is in plan or council mode.
func (l *Loop) SetModeDirective(blocks []api.SystemBlock) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.modeDirective = append([]api.SystemBlock(nil), blocks...)
}

// inheritedModeBlocks returns the directive blocks a child with the given gate
// mode inherits: none unless the child itself is in plan or council mode.
func inheritedModeBlocks(directive []api.SystemBlock, mode permissions.Mode) []api.SystemBlock {
	if mode == permissions.ModePlan || mode == permissions.ModeCouncil {
		return directive
	}
	return nil
}

// ModeDirective returns a copy of the directive children may inherit.
func (l *Loop) ModeDirective() []api.SystemBlock {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return append([]api.SystemBlock(nil), l.modeDirective...)
}

// SystemOverlay returns a copy of the current overlay blocks.
func (l *Loop) SystemOverlay() []api.SystemBlock {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return append([]api.SystemBlock(nil), l.systemOverlay...)
}

// RefreshSystem rebuilds the system blocks from current on-disk state and
// installs them, returning the new blocks. Returns nil when no RebuildSystem is
// configured, leaving the existing blocks untouched.
//
// Call it after anything that changes what the system prompt should say: a
// memory written by memdir.RunExtract, a skill created mid-session, an edited
// CLAUDE.md. Without it those writes are invisible to the running conversation,
// because Run snapshots the blocks once and never re-reads them.
func (l *Loop) RefreshSystem() []api.SystemBlock {
	l.mu.RLock()
	rebuild := l.cfg.RebuildSystem
	l.mu.RUnlock()
	if rebuild == nil {
		return nil
	}
	// Rebuilt outside the lock: it reads the filesystem and may be slow.
	blocks := rebuild()
	if blocks == nil {
		return nil
	}
	l.mu.Lock()
	l.cfg.System = blocks
	l.mu.Unlock()
	return blocks
}

// SetClient swaps the API client (e.g. after a fresh login reloads credentials).
func (l *Loop) SetClient(client *api.Client) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.client = client
	l.compactFails.Store(0)
	l.compactStuck.Store(0)
}

// SetProviderChain configures provider failover for this loop. chainResolver
// returns the ordered provider list for the given role; newClientFor constructs
// a fresh API client from a provider settings value. Both must be non-nil.
// Safe to call before Run().
func (l *Loop) SetProviderChain(
	role string,
	chainResolver func(string) []settings.ActiveProviderSettings,
	newClientFor func(settings.ActiveProviderSettings) (*api.Client, error),
) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.role = role
	l.chainResolver = chainResolver
	l.newClientFor = newClientFor
	l.providerRotation = providerrotation.New()
}

// SetTTSRRules replaces the active mid-stream interruption rules.
// Safe to call concurrently with Run; the new rules take effect on the next turn.
func (l *Loop) SetTTSRRules(rules []ttsr.Rule) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg.TTSRRules = rules
}

// SetAskPermission installs the interactive permission callback.
// Called from the TUI after the Bubble Tea program is created.
func (l *Loop) SetAskPermission(fn func(ctx context.Context, toolName, toolInput string) (allow, alwaysAllow bool)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg.AskPermission = fn
}

// SetAskSubAgentPermission installs the callback used to bubble a sub-agent's
// permission decisions up to an interactive session. Only meaningful on the
// root loop — child loops inherit it via LoopConfig copying in
// runSubAgentWithModel.
func (l *Loop) SetAskSubAgentPermission(fn func(ctx context.Context, label, toolName, toolInput string) SubAgentPermVerdict) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.cfg.AskSubAgentPermission = fn
}

// SetTranscriptPath sets the callback that reports the session file for hook
// input; it follows the active session after /resume.
func (l *Loop) SetTranscriptPath(fn func() string) {
	l.mu.Lock()
	l.cfg.TranscriptPath = fn
	l.mu.Unlock()
}

// SetSessionID updates the active session identifier. When it changes, reset
// the one-time SessionStart reminder state so startup context is recomputed
// once for the new session.
func (l *Loop) SetSessionID(id string) {
	l.mu.Lock()
	changed := l.cfg.SessionID != id
	l.cfg.SessionID = id
	if changed {
		l.pendingContextCompacted = ""
		l.compactStuck.Store(0)
	}
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
	return l.RunPrompt(ctx, UserPrompt{}, messages, handler)
}

// RunPrompt is Run for a turn that starts with a prompt the user typed:
// prompt names that text, and the UserPromptSubmit hooks screen it once the
// SessionStart context is settled and before any request. A zero prompt runs
// no hooks. A hook that blocks or stops the prompt ends the run with a
// *PromptBlockedError and a history without the prompt.
func (l *Loop) RunPrompt(ctx context.Context, prompt UserPrompt, messages []api.Message, handler func(LoopEvent)) ([]api.Message, error) {
	msgs := make([]api.Message, len(messages))
	copy(msgs, messages)
	msgs = session.FilterUnresolvedToolUses(msgs)
	ctx, completionCancel, completion, completionErr := l.beginCompletion(ctx)
	defer completionCancel()
	if completionErr != nil {
		return msgs, completionErr
	}
	defer func() {
		if completion != nil && !completion.finished {
			status := CompletionIncomplete
			if ctx.Err() != nil {
				status = CompletionCancelled
			}
			completion.emit(handler, status, "")
		}
	}()

	l.mu.RLock()
	waitForTools := l.cfg.WaitForTools
	l.mu.RUnlock()
	if waitForTools != nil {
		if err := waitForTools(ctx); err != nil {
			return msgs, err
		}
	}

	// Pick up anything written since the last turn — memories extracted by
	// memdir, skills the agent created, an edited CLAUDE.md. This is what makes
	// learning apply within a session rather than only after a restart. When
	// nothing changed the blocks are byte-identical, so the prompt cache is
	// unaffected.
	l.RefreshSystem()

	// Snapshot mutable fields under the read lock so that concurrent Set*
	// calls from the TUI goroutine cannot race with this turn's reads.
	l.mu.Lock()
	// A root loop's Run is one user prompt; a child serves its parent's.
	if l.parentPromptID != "" {
		l.promptID = l.parentPromptID
	} else {
		l.promptID = api.NewUUID()
	}
	billingCtx := BillingContext{
		PromptID:      l.promptID,
		PrevRequestID: l.lastRequestID,
		IsSubAgent:    l.subAgentLabel != "",
	}
	rs := &runState{
		client:         l.client,
		model:          l.cfg.Model,
		window:         l.cfg.ContextWindow,
		system:         l.composeSystemLocked(),
		thinkingBudget: l.cfg.ThinkingBudget,
	}
	role := l.role
	chainResolver := l.chainResolver
	newClientFor := l.newClientFor
	providerRotation := l.providerRotation
	ttsrRules := l.cfg.TTSRRules
	repetitionGuard := l.cfg.RepetitionGuard
	l.mu.Unlock()

	// Build the failover chain once so we don't call chainResolver on every turn.
	var (
		failoverChain      []settings.ActiveProviderSettings
		currentProviderKey string
	)
	if chainResolver != nil {
		failoverChain = chainResolver(role)
		if len(failoverChain) > 0 {
			currentProviderKey = settings.ProviderKey(failoverChain[0])
		}
	}

	sessionReminderBlock, err := l.sessionStartReminderBlock(ctx)
	if err != nil {
		return msgs, err
	}
	if msgs, err = l.screenLastMessage(ctx, prompt, msgs, handler); err != nil {
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
	tools := buildToolDefsFor(l.reg, rs.model)
	if l.toolDefs != nil {
		tools = l.toolDefs
	}

	turn := 0
	lastAssistantTime := l.cfg.LastAssistantTime
	lastInputTokens := 0 // last reported input token count; 0 means unknown
	// rec counts recoveries spent on the request being sent; it resets after a
	// response drains cleanly. stop is handleStop's memory for this prompt.
	var rec requestRecovery
	var stop stopState
	// sameRequest is set by every path that resends the request just sent
	// (retry, repair, rotation), so it does not use up a turn.
	sameRequest := false
	// reminderPending keeps the session reminder on every attempt of the
	// prompt's first request, not just the first send.
	reminderPending := sessionReminderBlock != nil
	var reminder *api.SystemBlock
	fs := &failoverState{
		chain:         failoverChain,
		rotation:      providerRotation,
		newClientFor:  newClientFor,
		client:        &rs.client,
		providerKey:   &currentProviderKey,
		model:         &rs.model,
		contextWindow: &rs.window,
		run:           rs,
	}
	// ttsrFires counts total TTSR rule firings for the current user turn.
	// When it reaches maxTTSRFiresPerTurn the watcher is disabled (set to nil)
	// so the stream can complete without further interruptions.
	ttsrFires := 0
	const maxTTSRFiresPerTurn = 3
	turnWatcher := l.newTTSRWatcher(ttsrRules, repetitionGuard)
	// prevToolDurations carries the last tool round's timings to the next
	// request (x-claude-code-prev-tool-durations). Empty on a prompt's first
	// request, as in CC.
	var prevToolDurations string
	// contextCompacted rides on every attempt of the first request after a
	// compaction, including retries, and clears once a response drains (CC
	// consumes it once per request and re-adds it on each retry).
	rs.contextCompacted = l.takeContextCompacted()
	// userTurn marks requests whose trailing message the user wrote: the
	// prompt, and the request after a steering message. Retries of a request
	// keep it (a failed attempt may not have reached the server, so "user" is
	// right, and a rebill after a mid-stream failure is rare).
	userTurn := true
	for {
		if ctx.Err() != nil {
			return msgs, ctx.Err()
		}
		if !sameRequest {
			if l.cfg.MaxTurns > 0 && turn >= l.cfg.MaxTurns {
				return msgs, ErrMaxTurnsExceeded
			}
			turn++
		}
		sameRequest = false
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
				cw := rs.window
				if cw <= 0 {
					cw = internalmodel.ContextWindowFor(rs.model)
				}
				triggerTokens = lastInputTokens > cw*internalmodel.MicroCompactThresholdPct/100
			}
			if triggerTime || triggerTokens {
				// Compute the live-zone boundary from a preliminary breakpoint
				// application so we don't mutate messages that form the cached
				// prefix, which would bust the provider's KV cache.
				prelimBP := countSystemBreakpoints(rs.system, tools)
				prelimMsgs := applyHistoryBreakpoints(msgs, prelimBP)
				boundary := liveZoneBoundary(prelimMsgs)
				if r := microcompact.ApplyWithOptions(msgs, lastAssistantTime, gap, keep,
					microcompact.Options{LiveZoneBoundary: boundary}); r.Triggered {
					msgs = r.Messages
				}
			}
		}

		reqSystem := rs.system
		if reminderPending {
			if reminder == nil {
				reminder = l.consumeSessionReminderBlock()
			}
			if reminder != nil {
				reqSystem = append(append([]api.SystemBlock(nil), rs.system...), *reminder)
			}
		}
		// For Claude.ai OAuth (Max subscription) accounts, replace the static
		// billing block (index 0) with a dynamically computed one using the
		// SHA256 of the first user message. Other account types are unaffected.
		if l.cfg.IsOAuthSubscription && len(reqSystem) > 0 {
			if firstMsg := firstUserMessageText(msgs); firstMsg != "" {
				updated := append([]api.SystemBlock(nil), reqSystem...)
				updated[0] = DynamicBillingBlock(firstMsg, billingCtx)
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

		// Track the cached-prefix hash so future diagnostics can detect busts.
		if sum := hashCachedPrefix(reqSystem, tools, reqMsgs); sum != 0 {
			l.lastCachedPrefixHash = sum
		}

		req := &api.MessageRequest{
			Model:     rs.model,
			MaxTokens: rs.requestMaxTokens(l.cfg.MaxTokens),
			System:    reqSystem,
			Messages:  reqMsgs,
			Stream:    true,
			Tools:     tools,
			Metadata:  l.cfg.Metadata,
			Hints:     l.wireHints(billingCtx.PromptID, prevToolDurations, rs.contextCompacted, userTurn),
		}
		if rs.thinkingBudget > 0 {
			req.Thinking = &api.ThinkingConfig{
				Type:         "enabled",
				BudgetTokens: rs.thinkingBudget,
			}
		}

		// Pre-flight token estimate: len(JSON bytes) / 4 is a cheap proxy for
		// token count. If it exceeds the model's context window, compact before
		// sending — a single-turn blowup would hit a hard API error otherwise.
		if l.cfg.MicroCompact {
			if raw, err := json.Marshal(req.Messages); err == nil {
				estimatedTokens := len(raw) / 4
				cw := rs.window
				if cw <= 0 {
					cw = internalmodel.ContextWindowFor(rs.model)
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
					// Use reqMsgs (which has breakpoints applied) to determine the
					// live-zone boundary so we don't bust the cached prefix.
					boundary := liveZoneBoundary(reqMsgs)
					if r := microcompact.ApplyWithOptions(msgs, seed, 0, keep,
						microcompact.Options{LiveZoneBoundary: boundary}); r.Triggered {
						msgs = r.Messages
						reqMsgs = applyHistoryBreakpoints(msgs, priorBP)
						req.Messages = reqMsgs
					}
				}
			}
		}

		streamCtx := api.WithRetryHandler(l.apiCtx(ctx), func(ev api.RetryEvent) bool {
			handler(LoopEvent{
				Type:             EventAPIRetry,
				RetryAttempt:     ev.Attempt,
				RetryMax:         ev.MaxAttempts,
				RetryDelay:       ev.Delay,
				RetryAfter:       ev.RetryAfter,
				RateLimitResetAt: ev.ResetAt,
				RetryErr:         ev.Err,
				RetryReason:      ev.Reason,
				RetryStatus:      ev.Status,
			})
			// A rate limit with another provider to try is not worth waiting
			// on: stop retrying so the loop rotates.
			if ev.Reason == api.ReasonRateLimit && fs.canRotate() {
				rec.rotationAbort = true
				return false
			}
			// Short 429s are usually transient. Long retry-after values are
			// plan/quota exhaustion; surfacing the error is clearer than
			// leaving the user watching a spinner for minutes or hours.
			return ev.Delay <= maxRateLimitWait
		})
		rec.rotationAbort = false
		stream, err := rs.client.StreamMessage(streamCtx, req)
		if err != nil {
			if ctx.Err() != nil {
				return msgs, ctx.Err()
			}
			if api.Classify(err).Class == api.ClassCancelled {
				return msgs, err
			}
			recovered, retry := l.recoverConnect(ctx, err, msgs, &rec, fs, handler)
			if retry {
				msgs = recovered
				sameRequest = true
				continue
			}
			if ctx.Err() != nil {
				// Cancelled while waiting to resend: report the cancel.
				return msgs, ctx.Err()
			}
			return msgs, fmt.Errorf("agent: stream: %w", err)
		}

		// Remember this response's request-id so the next request in the
		// conversation can reference it as cc_prev_req.
		if rid := stream.ResponseHeader.Get("request-id"); rid != "" {
			billingCtx.PrevRequestID = rid
			l.mu.Lock()
			l.lastRequestID = rid
			l.mu.Unlock()
		}

		// Emit rate-limit info from response headers before draining.
		if rlInfo := ratelimit.Parse(stream.ResponseHeader); rlInfo.HasData() {
			handler(LoopEvent{
				Type:             EventRateLimit,
				RateLimitInfo:    rlInfo,
				RateLimitWarning: rlInfo.WarningMessage(),
			})
		}

		// Create a fresh TTSR watcher for this attempt. When the turn-level fire
		// budget is exhausted (ttsrFires >= maxTTSRFiresPerTurn), pass nil so the
		// stream is allowed to complete without further TTSR interruption.
		var ttsrWatcher *ttsr.Watcher
		if ttsrFires < maxTTSRFiresPerTurn && turnWatcher != nil {
			turnWatcher.StartAttempt()
			ttsrWatcher = turnWatcher
		}

		// Enabled completion keeps text provisional until its stopping reason
		// and verification result are known. Other events still stream normally.
		var candidateText strings.Builder
		streamHandler := handler
		if completion != nil {
			streamHandler = func(ev LoopEvent) {
				if ev.Type == EventText {
					candidateText.WriteString(ev.Text)
				} else {
					handler(ev)
				}
			}
		}
		res, err := l.drainStream(ctx, stream, streamHandler, ttsrWatcher)
		inputTokens := res.Usage.PromptInputTokens()
		if inputTokens > 0 {
			lastInputTokens = inputTokens
		}
		_ = stream.Close()
		if err != nil {
			// TTSR rule fired: discard partial blocks, inject correction as a
			// <system-reminder> user message, and restart the turn attempt.
			// This check comes BEFORE the cancel check because ErrMatch is a
			// custom error type — errors.Is(context.Canceled) won't match it.
			if ttsrErr, ok := errors.AsType[*ttsr.ErrMatch](err); ok {
				closeDanglingTools(handler, res.StartedTools, danglingToolMsg)
				if !ttsr.IsRepetition(ttsrErr.Rule) {
					ttsrFires++ // the repetition guard bounds itself
				}
				userTurn = false // the retry's trailing message is conduit's, not the user's
				correction := ttsrErr.Rule.Correction
				msgs = append(msgs, api.Message{
					Role: "user",
					Content: []api.ContentBlock{{
						Type: "text",
						Text: fmt.Sprintf("<system-reminder>\n%s\n</system-reminder>", correction),
					}},
				})
				handler(LoopEvent{
					Type:           EventTTSR,
					TTSRRule:       ttsrErr.Rule.Name,
					TTSRCorrection: correction,
				})
				// When the budget is exhausted (ttsrFires >= maxTTSRFiresPerTurn),
				// ttsrWatcher will be nil next iteration so the stream completes.
				continue
			}

			recovered, outcome, rerr := l.recoverDrain(ctx, err, res, msgs, &rec, fs, handler)
			msgs = recovered
			if outcome == drainRetry {
				sameRequest = true
				continue
			}
			return msgs, rerr
		}
		rec.reset()
		rs.contextCompacted = ""
		lastAssistantTime = time.Now()
		if ttsrWatcher != nil && hasToolUse(res.Blocks) {
			l.deliverTTSRWarnings(ttsrWatcher)
		}

		var act stopAction
		var stopErr error
		msgs, act, stopErr = l.handleStop(ctx, msgs, res, &stop, rs, handler)
		switch act {
		case stopFail:
			return msgs, stopErr
		case stopRetry:
			// The request is resent with a nudge merged in: no turn used, and
			// the trailing message is no longer purely the user's.
			sameRequest = true
			userTurn = false
			continue
		case stopContinue:
			if completion != nil {
				completion.text.WriteString(candidateText.String())
			}
			userTurn = false
			continue
		}
		// Past the retry check: an empty-response resend is still request 1.
		reminderPending = false

		if act == stopEnd {
			if completion != nil {
				if res.StopReason != "end_turn" || !hasContent(res.Blocks) {
					return msgs, completion.incomplete(handler, CompletionIncomplete, "The model stopped without a natural completion candidate.")
				}
				var retry bool
				completion.text.WriteString(candidateText.String())
				msgs, retry, err = completion.check(ctx, msgs, handler)
				if err != nil {
					return msgs, err
				}
				if retry {
					userTurn = false
					continue
				}
				if completion.text.Len() > 0 {
					handler(LoopEvent{Type: EventText, Text: completion.text.String()})
				}
			}
			// end_turn or unknown — we're done.
			// Auto-compact check: if context is approaching capacity, compact
			// so future turns don't hit the limit. Non-fatal if it fails.
			msgs = l.maybeAutoCompact(ctx, rs, msgs, inputTokens, handler)
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
				l.cfg.BackgroundReviewer.OnEndTurn(ctx, msgs)
			}
			return msgs, nil
		}

		// Execute tools, build a user message with all tool_results.
		if completion != nil {
			completion.text.WriteString(candidateText.String())
			if completion.text.Len() > 0 {
				handler(LoopEvent{Type: EventText, Text: completion.text.String()})
			}
			completion.text.Reset()
		}
		toolResults, toolDurations, stopTurn, err := l.executeTools(ctx, res.Blocks, res.InvalidTools, handler)
		prevToolDurations = api.FormatToolDurations(toolDurations)
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
			if completion != nil {
				return msgs, completion.incomplete(handler, CompletionYielded, "Turn yielded to the user before verification.")
			}
			// Mirror the end_turn notification: StopTurn hands the turn back to the
			// user, so the "ready" signal is equally applicable here.
			if l.cfg.NotifyOnComplete {
				hooks.Notify("conduit · ready", "Your turn.")
			}
			if l.cfg.OnEndTurn != nil {
				l.cfg.OnEndTurn(msgs)
			}
			if l.cfg.BackgroundReviewer != nil {
				l.cfg.BackgroundReviewer.OnEndTurn(ctx, msgs)
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
				if completion != nil {
					return msgs, completion.incomplete(handler, CompletionYielded, "Edits require review before verification.")
				}
				if l.cfg.OnEndTurn != nil {
					l.cfg.OnEndTurn(msgs)
				}
				if l.cfg.BackgroundReviewer != nil {
					l.cfg.BackgroundReviewer.OnEndTurn(ctx, msgs)
				}
				return msgs, nil
			}
		}

		userTurn = false

		// Drain programmatic message queue (team mailbox delivery).
		// All messages preserved in insertion order; delivered before human
		// steering so teammate context arrives before the model's next response.
		// Per-loop field — no cross-loop contamination (token bleed protection).
		l.msgMu.Lock()
		queued := l.msgQueue
		l.msgQueue = nil
		l.msgMu.Unlock()
		for _, text := range queued {
			msgs = append(msgs, api.Message{
				Role:    "user",
				Content: []api.ContentBlock{{Type: "text", Text: text}},
			})
			handler(LoopEvent{Type: EventText, Text: ""})
		}

		// Steering injection: if the user sent a message while tools were
		// running, append it as a user turn now so the model sees it before
		// the next API call — without interrupting the current turn.
		// Last-write-wins: earlier steer messages are superseded by later ones.
		if p := l.steerMsg.Swap(nil); p != nil && len(*p) > 0 {
			msgs = append(msgs, api.Message{Role: "user", Content: *p})
			userTurn = true
			handler(LoopEvent{Type: EventText, Text: ""}) // wake TUI to reflect history update
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

// liveZoneBoundary returns the exclusive upper bound of the cached prefix in
// msgs — i.e., the smallest index such that all messages at index < boundary
// should be treated as byte-immutable to preserve the provider's KV cache.
//
// It scans all messages and finds the LAST one carrying a cache_control
// breakpoint (not the first): everything up to and including that message
// forms the cached prefix, because Anthropic caches the prefix up to each
// breakpoint. Returning first-breakpoint-index would leave messages between
// two breakpoints eligible for compaction, silently busting the cache on
// longer sessions.
//
// Returns 0 if no breakpoints exist (entire history is live).
func liveZoneBoundary(msgs []api.Message) int {
	last := -1
	for i, m := range msgs {
		for _, blk := range m.Content {
			if blk.CacheControl != nil {
				last = i
			}
		}
	}
	if last < 0 {
		return 0
	}
	return last + 1
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

	// Hash tool definitions. The full list is always hashed because buildToolDefs
	// places a cache_control breakpoint on the last element, so Anthropic caches
	// everything up to and including that tool — i.e. the entire list.
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
