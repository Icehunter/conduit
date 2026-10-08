package api

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// WireHints are the per-request x-claude-code-* client-hint headers Claude
// Code sends on /v1/messages (decoded-2.1.292: request headers built next to
// DEn() in 0618_e5o.js, compaction and tool-duration headers in 1640_tBt.js
// unn()/fnn()). Only the Anthropic Messages transport sends them.
type WireHints struct {
	// RequestClass is one of main, subagent, workflow, compaction, auxiliary.
	RequestClass string
	// AgentType names a sub-agent's kind: a built-in agent name such as
	// general-purpose, "custom" for registry/plugin agents, or "teammate".
	AgentType string
	// AgentID and ParentAgentID identify a sub-agent and the agent that
	// spawned it. Both are empty on the main loop.
	AgentID       string
	ParentAgentID string
	// PromptID is the UUID of the user prompt being served (the same value
	// as cc_prompt_id in the billing block).
	PromptID string
	// Compaction is set on a compaction request itself: auto or manual.
	Compaction string
	// ContextCompacted is set on the first main-loop request after a
	// compaction: auto, manual or reactive.
	ContextCompacted string
	// PrevToolDurations is FormatToolDurations output for the tools run
	// since the previous request in this turn.
	PrevToolDurations string
	// SessionID is the conduit session the request belongs to; sub-agents
	// carry their parent's. applyWireHints never sends it (CC has no such
	// header); Config.RequestHeaders hooks may, e.g. Copilot's
	// X-Interaction-Id.
	SessionID string
	// UserTurn is set on a main-loop request that carries something the user
	// just wrote: a prompt, or a steering message. It stays false for tool
	// follow-ups and for text conduit injected as a user turn (team mailbox
	// deliveries, task notifications). applyWireHints never sends it;
	// Config.RequestHeaders hooks may, e.g. Copilot's x-initiator.
	UserTurn bool
}

// ToolDuration is one tool execution's wall-clock time.
type ToolDuration struct {
	Name     string
	Duration time.Duration
}

// Limits from CC's RCt(): at most 32 entries and 4096 bytes.
const (
	maxToolDurationEntries = 32
	maxToolDurationsLen    = 4096
)

// FormatToolDurations renders durations as "Name=ms;Name=ms" for
// x-claude-code-prev-tool-durations, mirroring CC's RCt(). It returns "" when
// there is nothing to send.
func FormatToolDurations(durations []ToolDuration) string {
	var b strings.Builder
	n := 0
	for _, d := range durations {
		if n >= maxToolDurationEntries {
			break
		}
		ms := max(d.Duration.Round(time.Millisecond).Milliseconds(), 0)
		entry := escapeHintValue(d.Name, ";=, ") + "=" + strconv.FormatInt(ms, 10)
		sep := 0
		if n > 0 {
			sep = 1
		}
		if b.Len()+sep+len(entry) > maxToolDurationsLen {
			break
		}
		if sep == 1 {
			b.WriteByte(';')
		}
		b.WriteString(entry)
		n++
	}
	return b.String()
}

// escapeHintValue percent-encodes '%', every byte outside printable ASCII,
// and any byte in extra, the way CC applies encodeURIComponent per character
// (Shr() for agent ids/types, Wro() with extra ";=, " for tool names).
// Invalid UTF-8 becomes U+FFFD first, as JS's toWellFormed() would.
func escapeHintValue(s, extra string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '%' || c < 0x20 || c > 0x7e || strings.IndexByte(extra, c) >= 0 {
			fmt.Fprintf(&b, "%%%02X", c)
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// hintHeadersEnabled reports whether the x-claude-code-* hint headers go to
// baseURL. CLAUDE_CODE_GATEWAY_HINT_HEADERS forces them on or off, parsed as
// CC's triBool (1/true/yes/on, 0/false/no/off, case-insensitive; anything
// else is ignored), as in DEn(); otherwise they go only to the first-party API.
func hintHeadersEnabled(baseURL string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CLAUDE_CODE_GATEWAY_HINT_HEADERS"))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return isFirstPartyBaseURL(baseURL)
}

// isFirstPartyBaseURL reports whether baseURL targets the first-party
// Anthropic API (CC's Ky(): an empty base URL or host api.anthropic.com).
func isFirstPartyBaseURL(baseURL string) bool {
	if baseURL == "" {
		return true
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return u.Host == "api.anthropic.com"
}

// applyWireHints sets the client-hint headers. The agent id headers go to any
// Anthropic endpoint. The legacy x-cc-* compaction pair follows CC's yl()
// (first-party base URL only, no env override); the x-claude-code-* set
// follows DEn() (hintHeadersEnabled).
func applyWireHints(h http.Header, hints *WireHints, firstPartyURL, hintsEnabled bool) {
	if hints == nil {
		return
	}
	if hints.AgentID != "" {
		h.Set("x-claude-code-agent-id", escapeHintValue(hints.AgentID, ""))
	}
	if hints.ParentAgentID != "" {
		h.Set("x-claude-code-parent-agent-id", escapeHintValue(hints.ParentAgentID, ""))
	}
	if firstPartyURL {
		if hints.ContextCompacted != "" {
			h.Set("x-cc-context-compacted", hints.ContextCompacted)
		}
		if hints.Compaction != "" {
			h.Set("x-cc-compaction-request", hints.Compaction)
		}
	}
	if !hintsEnabled {
		return
	}
	if hints.RequestClass != "" {
		h.Set("x-claude-code-request-class", hints.RequestClass)
	}
	if hints.AgentType != "" {
		h.Set("x-claude-code-agent-type", escapeHintValue(hints.AgentType, ""))
	}
	if isUUID(hints.PromptID) {
		h.Set("x-claude-code-prompt-id", hints.PromptID)
	}
	if hints.ContextCompacted != "" {
		h.Set("x-claude-code-context-compacted", hints.ContextCompacted)
	}
	if hints.Compaction != "" {
		h.Set("x-claude-code-compaction", hints.Compaction)
	}
	if hints.PrevToolDurations != "" {
		h.Set("x-claude-code-prev-tool-durations", hints.PrevToolDurations)
	}
}

// isUUID reports whether s is a canonical 8-4-4-4-12 hex UUID.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
			if !isHex {
				return false
			}
		}
	}
	return true
}
