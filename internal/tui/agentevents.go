package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
)

// isCancelError reports whether err represents a user-initiated cancellation.
// Covers context.Canceled, context.DeadlineExceeded, and the network-level
// "use of closed network connection" that appears when the HTTP response body
// is torn down mid-read (which doesn't wrap context.Canceled directly).
func isCancelError(err error) bool {
	if err == nil {
		return false
	}
	// An idle timeout tears the stream down by cancelling its context, so its
	// chain holds context.Canceled; it is a failure, not the user's cancel.
	if errors.Is(err, api.ErrStreamIdleTimeout) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "request canceled")
}

// flushThinking turns the reasoning streamed so far into a collapsed
// RoleThinking row. Whitespace-only reasoning (redacted or empty signed
// blocks) produces nothing.
func (m *Model) flushThinking() {
	text := strings.TrimSpace(m.thinking)
	m.thinking = ""
	if text == "" {
		return
	}
	m.messages = append(m.messages, Message{Role: RoleThinking, Content: text})
}

func (m Model) applyAgentEvent(ev agent.LoopEvent) Model {
	switch ev.Type {
	case agent.EventThinking:
		if !m.running {
			break // late delta from a cancelled turn
		}
		m.apiRetryStatus = ""
		if m.streaming != "" {
			// Reasoning after answer text: commit the text first so order holds.
			m.messages = append(m.messages, m.assistantMessage(m.streaming))
			m.streaming = ""
		}
		wasBlank := strings.TrimSpace(m.thinking) == ""
		m.thinking += ev.Text
		if wasBlank || ev.Text == "" {
			m.refreshViewport()
		}

	case agent.EventText:
		m.apiRetryStatus = ""
		m.flushThinking()
		m.streaming += ev.Text
		// refreshViewport's sticky-bottom logic preserves the user's
		// scroll position when they've scrolled up to read history mid-
		// stream. GotoBottom only fires when they're already pinned to
		// the bottom.
		m.refreshViewport()

	case agent.EventToolStart:
		m.apiRetryStatus = ""
		// Tool block started streaming — show the row immediately.
		// ToolInput arrives later via EventToolUse.
		m.flushThinking()
		if m.streaming != "" {
			m.messages = append(m.messages, m.assistantMessage(m.streaming))
			m.streaming = ""
		}
		m.messages = append(m.messages, Message{
			Role:        RoleTool,
			ToolName:    ev.ToolName,
			ToolID:      ev.ToolID,
			ToolStarted: time.Now(),
			Content:     "running…",
		})
		m.refreshViewport()

	case agent.EventToolUse:
		// Block complete — update the existing row with the full input.
		found := false
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role == RoleTool && m.messages[i].ToolID == ev.ToolID {
				m.messages[i].ToolInput = string(ev.ToolInput)
				found = true
				break
			}
		}
		if !found {
			// Defensive: EventToolStart was missed; create the row now.
			m.flushThinking()
			if m.streaming != "" {
				m.messages = append(m.messages, m.assistantMessage(m.streaming))
				m.streaming = ""
			}
			m.messages = append(m.messages, Message{
				Role:        RoleTool,
				ToolName:    ev.ToolName,
				ToolID:      ev.ToolID,
				ToolInput:   string(ev.ToolInput),
				ToolStarted: time.Now(),
				Content:     "running…",
			})
		}
		m.refreshViewport()

	case agent.EventToolResult:
		if !(ev.IsError && ev.ResultText == agent.DanglingToolMessage) {
			// A finished tool round: what follows belongs to a new response.
			m.attemptStart, m.attemptSet = len(m.messages), true
		}
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role == RoleTool && (m.messages[i].ToolID == ev.ToolID || (m.messages[i].ToolID == "" && m.messages[i].Content == "running…")) {
				content := truncateToolResult(ev.ResultText, 500)
				m.messages[i].Content = content
				m.messages[i].ToolError = ev.IsError
				if !m.messages[i].ToolStarted.IsZero() {
					m.messages[i].ToolDuration = time.Since(m.messages[i].ToolStarted).Round(time.Second)
				}
				break
			}
		}
		m.refreshViewport()

	case agent.EventRateLimit:
		m.rateLimitWarning = ev.RateLimitWarning
		m.syncLive()

	case agent.EventAPIRetry:
		// Only a real rate limit says anything about the plan's usage; a 502
		// or a dropped connection with a retry-after must not fill the meter.
		if ev.RetryReason == api.ReasonRateLimit {
			m = m.applyAPIRateLimitToPlanUsage(ev)
		}
		if ev.RetryStream {
			// The response restarts from the top: drop the failed attempt's
			// text so the retry does not append to it.
			m.streaming = ""
			m.thinking = ""
			m.dropFailedAttemptRows()
		}
		m.apiRetryStatus = retryStatusText(ev)
		m.refreshViewport()

	case agent.EventPartial:
		// No-op. The partial assistant text is appended to the history Run
		// returns and persisted from there when the turn ends; writing it here
		// as well saved it twice, out of order.

	case agent.EventNotice:
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: ev.Notice})
		m.refreshViewport()

	case agent.EventProviderFailover:
		m.streaming = "" // the new provider restarts the response
		m.thinking = ""
		m.messages = append(m.messages, Message{
			Role:    RoleSystem,
			Content: "Switched provider \u2192 " + ev.FailoverProvider,
		})
		m.refreshViewport()

	case agent.EventTTSR:
		m.streaming = "" // the response restarts with the correction
		m.thinking = ""
		m.messages = append(m.messages, Message{
			Role:    RoleSystem,
			Content: fmt.Sprintf("Rule %q fired \u2014 restarting the response with a correction.", ev.TTSRRule),
		})
		m.refreshViewport()

	case agent.EventCompacted:
		// The loop goroutine already appended the compaction to the session
		// file, so everything in ev.History is on disk. The history the turn
		// ends with is built on top of it.
		m.history = m.annotateTurnProvider(ev.History)
		if !m.compactUnsaved {
			// (While the rewrite fallback runs the count stays at what the
			// file holds; its result sets it.)
			persisted, _ := persistableCompaction(m.history, ev.CompactTailLen)
			m.persistedCount = len(persisted)
		}
		m.messages = append(m.messages, Message{
			Role:    RoleSystem,
			Content: compactedRowText(ev),
		})
		m.tallyTokens()
		m.refreshViewport()

	case agent.EventCost:
		// Running cost estimate during streaming. Update the model so the
		// working animation can display it.
		m.streamingCostUSD = ev.CostUSD
	}
	return m
}

// retryStatusText is the banner shown while a request waits to be retried:
// what went wrong, then when the retry happens and which attempt it is.
func retryStatusText(ev agent.LoopEvent) string {
	var label string
	switch ev.RetryReason {
	case api.ReasonRateLimit:
		label = "Rate limited"
	case api.ReasonOverloaded:
		label = "API overloaded"
	case api.ReasonServerError:
		label = "Server error"
		if ev.RetryStatus > 0 {
			label = fmt.Sprintf("Server error (%d)", ev.RetryStatus)
		}
	case api.ReasonNetwork:
		label = "Connection error"
	case api.ReasonIdleTimeout:
		label = "Stream stalled"
	case api.ReasonStreamTruncated, api.ReasonStreamError:
		label = "Response interrupted"
	case api.ReasonToolFlow:
		label = "Repairing conversation history"
	case api.ReasonPrefixLock:
		label = "Dropping stale thinking blocks"
	default:
		label = "Request failed"
	}
	when := " \u00b7 retrying"
	if ev.RetryDelay > 0 {
		delay := max(ev.RetryDelay.Round(time.Second), time.Second)
		when = fmt.Sprintf(" \u00b7 retrying in %s", delay)
	}
	if ev.RetryAttempt > 0 && ev.RetryMax > 0 {
		when += fmt.Sprintf(" (%d/%d)", ev.RetryAttempt, ev.RetryMax)
	}
	return label + when
}

// friendlyAgentError is the text of a failed turn, with a hint at the fix
// when the error class has one.
func friendlyAgentError(err error) string {
	text := err.Error()
	c := api.Classify(err)
	switch c.Class {
	case api.ClassAuth:
		return text + "\nSign in again with /login."
	case api.ClassContextOverflow:
		return text + "\nThe conversation no longer fits the model's context window. Run /compact to summarize it, or /clear to start over."
	case api.ClassRateLimited:
		if !c.ResetAt.IsZero() {
			return fmt.Sprintf("%s\nThe limit resets at %s.", text, c.ResetAt.Local().Format("15:04"))
		}
		if c.RetryAfter > 0 {
			return fmt.Sprintf("%s\nTry again in %s.", text, c.RetryAfter.Round(time.Second))
		}
	}
	return text
}

func (m Model) applyAPIRateLimitToPlanUsage(ev agent.LoopEvent) Model {
	if !m.usageStatusEnabled {
		return m
	}
	if _, ok := m.planUsageProviderSettings(); !ok {
		return m
	}
	reset := ev.RateLimitResetAt
	if reset.IsZero() && ev.RetryAfter > 0 {
		reset = time.Now().Add(ev.RetryAfter)
	}
	m.planUsage.FiveHour.Utilization = 100
	if !reset.IsZero() {
		m.planUsage.FiveHour.ResetsAt = reset
	}
	m.planUsageCachedAt = time.Now()
	m.planUsageErr = ""
	return m
}

func (m *Model) appendAssistantInfo(turnCostDelta float64) {
	duration := time.Duration(0)
	if !m.turnStarted.IsZero() {
		duration = time.Since(m.turnStarted).Round(time.Second)
	}
	modelName := m.effectiveAssistantModelName()
	if duration <= 0 && turnCostDelta <= 0 && modelName == "" {
		return
	}
	m.messages = append(m.messages, Message{
		Role:              RoleAssistantInfo,
		AssistantModel:    shortModelName(modelName),
		AssistantDuration: duration,
		AssistantCost:     turnCostDelta,
	})
}

func truncateToolResult(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	return string(runes[:maxRunes-1]) + "…"
}

// compactedRowText is the transcript row for a compaction.
func compactedRowText(ev agent.LoopEvent) string {
	kind := ev.CompactKind
	if kind == "" {
		kind = "auto"
	}
	by := ev.CompactModel
	if by == "" {
		by = "unknown model"
	}
	return fmt.Sprintf("Context compacted (%s) \u2014 kept %d recent messages; summary by %s", kind, ev.CompactTailLen, by)
}

// dropFailedAttemptRows removes the transcript rows the failed attempt of a
// retried stream committed: the assistant text it flushed before a tool row.
// Its tool rows stay (the loop closed them with an error result) so no call
// vanishes from the transcript. The attempt began after the last finished tool
// round, or after the turn's prompt.
func (m *Model) dropFailedAttemptRows() {
	start := m.attemptStart
	if !m.attemptSet {
		start = 0
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role == RoleUser {
				start = i + 1
				break
			}
		}
	}
	if start > len(m.messages) {
		return
	}
	kept := m.messages[:start:start]
	for _, row := range m.messages[start:] {
		if row.Role != RoleAssistant {
			kept = append(kept, row)
		}
	}
	m.messages = kept
}
