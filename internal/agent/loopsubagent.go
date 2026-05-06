package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/coordinator"
)

// RunSubAgent runs a nested agent loop with the given prompt as the sole user
// message. Used by callers that explicitly need the foreground model.
// The sub-agent inherits the same tools and system prompt but starts
// with a fresh single-turn history. Returns the concatenated text from the
// final assistant message.
//
// When coordinator mode is active, the result is wrapped in a
// <task-notification> XML block so the coordinator model can identify and
// process it correctly per its system prompt instructions.
func (l *Loop) RunSubAgent(ctx context.Context, prompt string) (string, error) {
	l.mu.RLock()
	model := l.cfg.Model
	l.mu.RUnlock()
	return l.runSubAgentWithModel(ctx, prompt, model)
}

// RunBackgroundAgent runs a nested agent loop on the configured background
// model. The foreground chat model is not changed.
func (l *Loop) RunBackgroundAgent(ctx context.Context, prompt string) (string, error) {
	return l.runSubAgentWithModel(ctx, prompt, l.BackgroundModel())
}

func (l *Loop) runSubAgentWithModel(ctx context.Context, prompt, model string) (string, error) {
	start := time.Now()
	msgs := []api.Message{
		{
			Role:    "user",
			Content: []api.ContentBlock{{Type: "text", Text: prompt}},
		},
	}
	l.mu.RLock()
	childClient := l.client
	childCfg := l.cfg
	l.mu.RUnlock()

	// Sub-agents must not fire parent-session side effects. Strip callbacks
	// and notifications so a sub-agent end_turn doesn't send desktop pings
	// or re-trigger memory extraction / session-memory updates.
	childCfg.NotifyOnComplete = false
	childCfg.OnEndTurn = nil
	childCfg.OnCompact = nil
	childCfg.OnFileAccess = nil

	child := &Loop{client: childClient, reg: l.reg, cfg: childCfg}
	if strings.TrimSpace(model) != "" {
		child.cfg.Model = model
	}
	history, err := child.Run(ctx, msgs, func(LoopEvent) {})

	agentID := fmt.Sprintf("agent-%x", start.UnixNano()&0xffffff)

	if coordinator.IsActive() {
		var result string
		if err != nil {
			notif := coordinator.TaskNotification(
				agentID, "failed",
				fmt.Sprintf("Agent failed: %v", err),
				"", 0, 0, time.Since(start).Milliseconds(),
			)
			return notif, nil
		}
		toolUses := countToolUses(history)
		result = extractLastAssistantText(history)
		notif := coordinator.TaskNotification(
			agentID, "completed",
			"Agent completed",
			result, 0, toolUses, time.Since(start).Milliseconds(),
		)
		return notif, nil
	}

	if err != nil {
		return "", err
	}
	return extractLastAssistantText(history), nil
}

// extractLastAssistantText returns the concatenated text from the final
// assistant message in a history slice.
func extractLastAssistantText(history []api.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" {
			var sb strings.Builder
			for _, block := range history[i].Content {
				if block.Type == "text" && block.Text != "" {
					if sb.Len() > 0 {
						sb.WriteByte('\n')
					}
					sb.WriteString(block.Text)
				}
			}
			return sb.String()
		}
	}
	return ""
}

// countToolUses counts tool_use blocks across all assistant messages.
func countToolUses(history []api.Message) int {
	n := 0
	for _, msg := range history {
		if msg.Role == "assistant" {
			for _, block := range msg.Content {
				if block.Type == "tool_use" {
					n++
				}
			}
		}
	}
	return n
}
