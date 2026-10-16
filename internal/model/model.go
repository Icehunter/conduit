// Package model centralises model selection for the agent loop.
//
// Priority order (mirrors getRuntimeMainLoopModel in the TS source):
//  1. Runtime /model override (highest)
//  2. ANTHROPIC_MODEL env var
//  3. CLAUDE_CODE_MODEL env var
//  4. settings.json "model" field (via SetDefault at startup)
//  5. Default constant
//
// The default matches the model shipped in Claude Code 2.1.173.
package model

import (
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

// runtimeOverride holds a model name set via /model at runtime.
// Uses atomic pointer so it's safe to read from concurrent goroutines.
var runtimeOverride atomic.Pointer[string]

// settingsDefault holds the model from settings.json, set once at startup.
var settingsDefault atomic.Pointer[string]

// SetOverride sets the runtime model override (from /model slash command).
func SetOverride(name string) {
	runtimeOverride.Store(&name)
}

// ClearOverride removes the runtime override (for testing).
func ClearOverride() {
	runtimeOverride.Store(nil)
}

// SetDefault sets the model from settings.json. Called once at startup before
// any goroutines read Resolve(). Lower priority than env vars.
func SetDefault(name string) {
	settingsDefault.Store(&name)
}

// Default is the hardcoded fallback model.
//
// Conduit tracks CC's default (claude-fable-5-1), the most capable
// widely-released model as of CC 2.1.259. Fable 5 (the prior point release)
// was previously restricted by US government policy but has since been
// re-enabled, and remains in the catalog as a selectable model.
const Default = "claude-fable-5-1"

// Fast is the faster/cheaper model used when /fast is active.
// Mirrors getSmallFastModel() — latest_per_family.sonnet in the CC 2.1.292 bundle.
const Fast = "claude-sonnet-5-5"

// MaxTokens is the default max_tokens value for /v1/messages.
// Real CC uses 16000 for the main loop; we match that.
const MaxTokens = 16000

// ThinkingBudgets maps effort levels to token budgets.
// Mirrors the thinking budget constants from the real CLI.
var ThinkingBudgets = map[string]int{
	"low":    1000,
	"normal": 0,
	"high":   8000,
	"max":    16000,
}

// Context window and auto-compact constants mirroring autoCompact.ts / context.ts.
const (
	ContextWindowDefault      = 200_000
	ContextWindow1M           = 1_000_000
	CompactReserveTokens      = 20_000 // MAX_OUTPUT_TOKENS_FOR_SUMMARY
	CompactBufferTokens       = 13_000 // AUTOCOMPACT_BUFFER_TOKENS
	MaxConsecutiveCompactFail = 3

	// MicroCompactThresholdPct triggers micro-compaction when usage exceeds
	// this percentage of usable context (before full compaction is needed).
	MicroCompactThresholdPct = 80

	// CompactThresholdPct triggers full compaction when usage exceeds this
	// percentage of usable context.
	CompactThresholdPct = 95
)

// reportedWindows holds per-model context windows reported by the provider's
// own model-listing API (Anthropic GET /v1/models max_input_tokens), keyed by
// lowercase model ID. Replaced wholesale by SetReportedWindows.
var reportedWindows atomic.Pointer[map[string]int]

// SetReportedWindows installs the provider-reported context window per model
// ID. Non-positive entries are dropped; nil or empty clears the table.
func SetReportedWindows(windows map[string]int) {
	m := make(map[string]int, len(windows))
	for id, w := range windows {
		if w > 0 {
			m[strings.ToLower(id)] = w
		}
	}
	reportedWindows.Store(&m)
}

// ContextWindowFor returns the context window for the named model.
//
// Resolution order: an explicit "[1m]" name suffix (1M), then the window the
// provider reported for the model ID (SetReportedWindows), then 200K.
//
// Conduit intentionally diverges from CC here: CC auto-promotes sonnet-4 and
// opus-4 to 1M regardless of what the account allows. Conduit only trusts the
// suffix or the provider-reported limit, and the compaction trigger stays
// capped (DefaultAutoCompactTriggerCap) so a 1M window does not delay
// compaction until ~800K tokens. See COMPATIBILITY.md § Intentional divergences.
//
// The CLAUDE_CODE_AUTO_COMPACT_WINDOW env var can further cap the returned value.
func ContextWindowFor(name string) int {
	n := strings.ToLower(name)
	window := ContextWindowDefault
	if strings.HasSuffix(n, "[1m]") {
		window = ContextWindow1M
	} else if t := reportedWindows.Load(); t != nil {
		if w, ok := (*t)[n]; ok {
			window = w
		}
	}
	if cap := contextWindowOverride(); cap > 0 && cap < window {
		return cap
	}
	return window
}

// ResolveContextWindow returns the window to size compaction against: the
// configured one when positive, else ContextWindowFor(name). The
// CLAUDE_CODE_AUTO_COMPACT_WINDOW cap applies to both.
func ResolveContextWindow(configured int, name string) int {
	if configured <= 0 {
		return ContextWindowFor(name)
	}
	if cap := contextWindowOverride(); cap > 0 && cap < configured {
		return cap
	}
	return configured
}

// AutoCompactThresholdFor returns the input-token count at which auto-compact
// should fire for the given model. Mirrors getAutoCompactThreshold in autoCompact.ts.
func AutoCompactThresholdFor(name string) int {
	return AutoCompactThresholdForWindow(ContextWindowFor(name))
}

// DefaultAutoCompactTriggerCap is the default ceiling on the auto-compact
// trigger. Windows of 1M would otherwise wait for ~967K tokens, by which point
// the conversation is slow, expensive and degraded. Override with
// compaction.maxTriggerTokens in conduit.json; 0 disables the cap.
const DefaultAutoCompactTriggerCap = 256_000

// AutoCompactThreshold returns the input-token count that triggers
// auto-compaction for a context window: window minus the output reserve and
// buffer, capped at cap when cap > 0.
func AutoCompactThreshold(window, cap int) int {
	t := AutoCompactThresholdForWindow(window)
	if cap > 0 && cap < t {
		return cap
	}
	return t
}

func AutoCompactThresholdForWindow(w int) int {
	if w <= 0 {
		w = ContextWindowDefault
	}
	effective := w - CompactReserveTokens
	t := effective - CompactBufferTokens
	// A small window (a local 32K model) would otherwise trigger at or below
	// zero and compact on every turn: never trigger before 75% of the window.
	if floor := w * 3 / 4; t < floor {
		t = floor
	}
	return t
}

// UsableContext returns the usable input token budget for the given model,
// accounting for reserved output buffer. Mirrors opencode's overflow.ts usable().
func UsableContext(name string, reservedOutput int) int {
	return UsableContextForWindow(ContextWindowFor(name), reservedOutput)
}

// UsableContextForWindow returns usable context for a given window size.
func UsableContextForWindow(window, reservedOutput int) int {
	if window <= 0 {
		window = ContextWindowDefault
	}
	if reservedOutput <= 0 {
		reservedOutput = CompactReserveTokens
	}
	usable := window - reservedOutput
	if usable < 0 {
		return 0
	}
	return usable
}

// OverflowLevel describes the current context usage level.
type OverflowLevel int

const (
	OverflowNone     OverflowLevel = iota // Below any threshold
	OverflowMicro                         // >= MicroCompactThresholdPct, time for micro-compact
	OverflowFull                          // >= CompactThresholdPct, time for full compact
	OverflowCritical                      // At or above 100%, must compact immediately
)

// CheckOverflow returns the overflow level for the given token count against
// the model's usable context. Mirrors opencode's overflow.ts isOverflow().
func CheckOverflow(name string, inputTokens, reservedOutput int) OverflowLevel {
	usable := UsableContext(name, reservedOutput)
	return CheckOverflowForUsable(usable, inputTokens)
}

// CheckOverflowForUsable returns the overflow level given usable context and current tokens.
func CheckOverflowForUsable(usable, inputTokens int) OverflowLevel {
	if usable <= 0 || inputTokens <= 0 {
		return OverflowNone
	}
	pct := (inputTokens * 100) / usable
	switch {
	case pct >= 100:
		return OverflowCritical
	case pct >= CompactThresholdPct:
		return OverflowFull
	case pct >= MicroCompactThresholdPct:
		return OverflowMicro
	default:
		return OverflowNone
	}
}

// contextWindowOverride reads CLAUDE_CODE_AUTO_COMPACT_WINDOW.
func contextWindowOverride() int {
	if s := os.Getenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// StripSuffix removes conduit-internal model suffixes from a model name before
// it is sent to the API. Suffixes like "[1m]" signal capabilities internally
// but are not valid model IDs on the wire.
func StripSuffix(name string) string {
	if strings.HasSuffix(strings.ToLower(name), "[1m]") {
		return name[:len(name)-4]
	}
	return name
}

// Resolve returns the model name to use, applying priority order.
func Resolve() string {
	if p := runtimeOverride.Load(); p != nil && *p != "" {
		return *p
	}
	if m := os.Getenv("ANTHROPIC_MODEL"); m != "" {
		return m
	}
	if m := os.Getenv("CLAUDE_CODE_MODEL"); m != "" {
		return m
	}
	if p := settingsDefault.Load(); p != nil && *p != "" {
		return *p
	}
	return Default
}
