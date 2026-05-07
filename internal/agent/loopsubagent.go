package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/coordinator"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/subagent"
	"github.com/icehunter/conduit/internal/tools/automodetool"
	"github.com/icehunter/conduit/internal/tools/planmodetool"
)

// SubAgentSpec configures an optionally-specialised sub-agent run.
// Zero value is equivalent to RunBackgroundAgent (no extra system, no model
// override, no tool restriction).
type SubAgentSpec struct {
	// SystemPrompt is appended as an extra system block when non-empty.
	SystemPrompt string
	// Model overrides the child's model when non-empty.
	Model string
	// Tools is the tool allowlist. Empty/nil means inherit parent registry.
	// Callers pass the canonical registry-key names (already alias-resolved).
	Tools []string
	// Mode sets the initial permission mode for the child. "" means inherit
	// the parent gate's current mode.
	Mode permissions.Mode
}

// RunSubAgentTyped runs a nested agent loop with optional specialisation
// (extra system prompt, model override, tool allowlist). It uses the
// background model unless spec.Model overrides it.
// resolveModelAlias maps Claude Code plugin shorthand model names to the actual
// model the loop should use. Plugins from claude-plugins-official declare
// "model: sonnet/opus/haiku/inherit" in agent frontmatter; the CC runtime
// resolves these against its own model list. Conduit maps them to the
// closest configured equivalent rather than passing bare aliases to the API.
func (l *Loop) resolveModelAlias(alias string) string {
	switch strings.ToLower(strings.TrimSpace(alias)) {
	case "", "inherit", "background":
		return l.BackgroundModel()
	case "haiku", "fast":
		return l.BackgroundModel()
	case "sonnet", "opus":
		// Use the foreground main model — it's whatever the user configured as
		// their primary model, which is the closest thing to "a capable model".
		l.mu.RLock()
		m := l.cfg.Model
		l.mu.RUnlock()
		return m
	default:
		return alias // already a full model ID — pass through unchanged
	}
}

func (l *Loop) RunSubAgentTyped(ctx context.Context, prompt string, spec SubAgentSpec) (string, error) {
	model := l.BackgroundModel()
	if spec.Model != "" {
		model = l.resolveModelAlias(spec.Model)
	}

	l.mu.RLock()
	childCfg := l.cfg
	childClient := l.client
	parentReg := l.reg
	l.mu.RUnlock()

	// Strip side-effect callbacks — same as runSubAgentWithModel.
	childCfg.NotifyOnComplete = false
	childCfg.OnEndTurn = nil
	childCfg.OnCompact = nil
	childCfg.OnFileAccess = nil
	childCfg.Model = model

	// Append agent-specific system block when provided.
	if spec.SystemPrompt != "" {
		childCfg.System = append(append([]api.SystemBlock(nil), childCfg.System...), api.SystemBlock{
			Type: "text",
			Text: spec.SystemPrompt,
		})
	}

	// Build the child registry (restricted or full).
	childReg := parentReg
	if len(spec.Tools) > 0 {
		childReg = parentReg.Subset(spec.Tools)
	}

	// Clone the permission gate so child mutations don't affect the parent.
	var childGate *permissions.Gate
	if childCfg.Gate != nil {
		childGate = childCfg.Gate.Clone()
	} else {
		childGate = permissions.New("", nil, permissions.ModeDefault, nil, nil, nil)
	}
	if spec.Mode != "" {
		childGate.SetMode(spec.Mode)
	}
	childCfg.Gate = childGate

	// Sub-agents must not block on interactive permission prompts.
	childCfg.AskPermission = nil

	// Register with tracker.
	childID := fmt.Sprintf("sub-%d", time.Now().UnixNano())
	label := "<sub-agent>"
	if len(prompt) > 0 {
		label = strings.ReplaceAll(strings.TrimSpace(prompt), "\n", " ")
		if len(label) > 30 {
			label = label[:30]
		}
	}
	subagent.Default.Add(subagent.Entry{
		ID:        childID,
		Label:     label,
		Mode:      childGate.Mode(),
		StartedAt: time.Now(),
	})
	defer subagent.Default.Remove(childID)

	// Build scoped mode-change tools that update both the child gate and tracker.
	notifyMode := func(m permissions.Mode) {
		childGate.SetMode(m)
		subagent.Default.UpdateMode(childID, m)
	}
	childEnterPlan := &planmodetool.EnterPlanMode{
		SetMode:     notifyMode,
		CurrentMode: func() permissions.Mode { return childGate.Mode() },
		AskEnter:    nil,
	}
	childExitPlan := &planmodetool.ExitPlanMode{
		SetMode:    notifyMode,
		AskApprove: nil,
	}
	childEnterAuto := &automodetool.EnterAutoMode{
		SetMode:     notifyMode,
		CurrentMode: func() permissions.Mode { return childGate.Mode() },
		AskEnter:    nil,
	}
	childExitAuto := &automodetool.ExitAutoMode{
		SetMode: notifyMode,
	}
	childReg = childReg.WithOverrides(childEnterPlan, childExitPlan, childEnterAuto, childExitAuto)

	child := &Loop{client: childClient, reg: childReg, cfg: childCfg}

	start := time.Now()
	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: prompt}}},
	}
	history, err := child.Run(ctx, msgs, subAgentEventHandler(childID))

	agentID := fmt.Sprintf("agent-%x", start.UnixNano()&0xffffff)

	if coordinator.IsActive() {
		if err != nil {
			notif := coordinator.TaskNotification(
				agentID, "failed",
				fmt.Sprintf("Agent failed: %v", err),
				"", 0, 0, time.Since(start).Milliseconds(),
			)
			return notif, nil
		}
		toolUses := countToolUses(history)
		result := extractLastAssistantText(history)
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

// RunSubAgentWithTools is a convenience wrapper around RunSubAgentTyped that
// only restricts the tool set, keeping the model and system prompt unchanged.
func (l *Loop) RunSubAgentWithTools(ctx context.Context, prompt string, tools []string) (string, error) {
	return l.RunSubAgentTyped(ctx, prompt, SubAgentSpec{Tools: tools})
}

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
	return l.runSubAgentWithModel(ctx, prompt, model, "")
}

// RunBackgroundAgent runs a nested agent loop on the configured background
// model. The foreground chat model is not changed.
func (l *Loop) RunBackgroundAgent(ctx context.Context, prompt string) (string, error) {
	return l.runSubAgentWithModel(ctx, prompt, l.BackgroundModel(), permissions.ModeBypassPermissions)
}

func (l *Loop) runSubAgentWithModel(ctx context.Context, prompt, model string, mode permissions.Mode) (string, error) {
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

	// Clone the permission gate so child mutations don't affect the parent.
	var childGate *permissions.Gate
	if childCfg.Gate != nil {
		childGate = childCfg.Gate.Clone()
	} else {
		childGate = permissions.New("", nil, permissions.ModeDefault, nil, nil, nil)
	}
	if mode != "" {
		childGate.SetMode(mode)
	}
	childCfg.Gate = childGate

	// Sub-agents must not block on interactive permission prompts.
	childCfg.AskPermission = nil

	// Register with tracker.
	childID := fmt.Sprintf("sub-%d", time.Now().UnixNano())
	label := "<sub-agent>"
	if len(prompt) > 0 {
		label = strings.ReplaceAll(strings.TrimSpace(prompt), "\n", " ")
		if len(label) > 30 {
			label = label[:30]
		}
	}
	subagent.Default.Add(subagent.Entry{
		ID:         childID,
		Label:      label,
		Mode:       childGate.Mode(),
		StartedAt:  time.Now(),
		Background: true, // runSubAgentWithModel is always a system-initiated call
	})
	defer subagent.Default.Remove(childID)

	// Build scoped mode-change tools that update both the child gate and tracker.
	notifyMode := func(m permissions.Mode) {
		childGate.SetMode(m)
		subagent.Default.UpdateMode(childID, m)
	}
	childEnterPlan := &planmodetool.EnterPlanMode{
		SetMode:     notifyMode,
		CurrentMode: func() permissions.Mode { return childGate.Mode() },
		AskEnter:    nil,
	}
	childExitPlan := &planmodetool.ExitPlanMode{
		SetMode:    notifyMode,
		AskApprove: nil,
	}
	childEnterAuto := &automodetool.EnterAutoMode{
		SetMode:     notifyMode,
		CurrentMode: func() permissions.Mode { return childGate.Mode() },
		AskEnter:    nil,
	}
	childExitAuto := &automodetool.ExitAutoMode{
		SetMode: notifyMode,
	}
	childReg := l.reg.WithOverrides(childEnterPlan, childExitPlan, childEnterAuto, childExitAuto)

	child := &Loop{client: childClient, reg: childReg, cfg: childCfg}
	if strings.TrimSpace(model) != "" {
		child.cfg.Model = model
	}
	history, err := child.Run(ctx, msgs, subAgentEventHandler(childID))

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

// subAgentEventHandler returns a LoopEvent handler that forwards tool events
// to the subagent tracker for TUI drill-in display.
func subAgentEventHandler(childID string) func(LoopEvent) {
	started := map[string]time.Time{}
	return func(ev LoopEvent) {
		switch ev.Type {
		case EventToolUse:
			started[ev.ToolID] = time.Now()
			subagent.Default.AppendEvent(childID, subagent.ToolEvent{
				ToolID:    ev.ToolID,
				ToolName:  ev.ToolName,
				ToolInput: string(ev.ToolInput),
				Status:    "running",
				StartedAt: time.Now(),
			})
		case EventToolResult:
			var dur time.Duration
			if t, ok := started[ev.ToolID]; ok {
				dur = time.Since(t).Round(time.Second)
				delete(started, ev.ToolID)
			}
			subagent.Default.UpdateEvent(childID, ev.ToolID, ev.IsError, dur)
		}
	}
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
