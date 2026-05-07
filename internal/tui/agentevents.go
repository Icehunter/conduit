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
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "request canceled")
}

func (m Model) applyAgentEvent(ev agent.LoopEvent) Model {
	switch ev.Type {
	case agent.EventText:
		m.apiRetryStatus = ""
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
		delay := ev.RetryDelay.Round(time.Second)
		if delay < time.Second {
			delay = time.Second
		}
		m.apiRetryStatus = fmt.Sprintf("Rate limited · retrying in %s", delay)

	case agent.EventPartial:
		// Conversation recovery: persist the partial assistant message to
		// the session JSONL so /resume can pick up from where we left off.
		// FilterUnresolvedToolUses runs at load time to drop orphan
		// tool_use blocks that never got a tool_result.
		if m.cfg.Session != nil && len(ev.PartialBlocks) > 0 {
			_ = m.cfg.Session.AppendMessage(api.Message{
				Role:    "assistant",
				Content: ev.PartialBlocks,
			})
		}
	}
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
