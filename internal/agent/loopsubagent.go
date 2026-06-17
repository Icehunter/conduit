package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/coordinator"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/subagent"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/tools/automodetool"
	"github.com/icehunter/conduit/internal/tools/planmodetool"
)

// SubAgentResult is returned by RunSubAgentTyped.
type SubAgentResult struct {
	// Text is the concatenated text from the final assistant message.
	Text string
	// Usage is the aggregated token usage across all turns of the inner loop.
	Usage api.Usage
	// DurationMs is the wall-clock time spent in the child loop.
	DurationMs int64
	// ToolUses is the total number of tool calls made by the child loop.
	ToolUses int
}

// SubAgentSpec configures an optionally-specialised sub-agent run.
// Zero value is equivalent to RunBackgroundAgent (no extra system, no model
// override, no tool restriction).
type SubAgentSpec struct {
	// SystemPrompt is appended as an extra system block when non-empty.
	SystemPrompt string
	// Role names a configured provider role (e.g. "background", "planning",
	// "implement"). When set and Model is empty, the loop's RoleResolver
	// determines the model and optionally a separate API client. Falls back
	// to BackgroundModel() when the role is not configured.
	Role string
	// Model overrides the child's model when non-empty. Takes precedence over Role.
	Model string
	// Tools is the tool allowlist. Empty/nil means inherit parent registry.
	// Callers pass the canonical registry-key names (already alias-resolved).
	Tools []string
	// Mode sets the initial permission mode for the child. "" means inherit
	// the parent gate's current mode.
	Mode permissions.Mode
	// Client overrides the API client used by the child loop. nil = inherit
	// the parent loop's client. Use this to route council members through
	// their own provider accounts rather than the parent's active client.
	Client *api.Client
	// Background marks the sub-agent as system-initiated so it is hidden from
	// the user-visible agent log panel and working-row badge.
	Background bool
	// DisableModeTools prevents EnterPlanMode/ExitPlanMode/EnterAutoMode/ExitAutoMode
	// from being added to the child registry. Use for council members and other
	// sub-agents that must not switch permission modes mid-run.
	DisableModeTools bool
	// MaxTokens overrides the child's max_tokens budget when > 0. Use for
	// long-form sub-agent runs (council synthesis, summarisation) where the
	// inherited parent budget would truncate the response.
	MaxTokens int
	// ExtraTools are additional tools to register in the child's registry
	// on top of (or as overrides to) the parent registry. Applied after the
	// Tools allowlist filter, so a tool in ExtraTools is always available
	// regardless of the allowlist.
	ExtraTools []tool.Tool
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

func (l *Loop) RunSubAgentTyped(ctx context.Context, prompt string, spec SubAgentSpec) (SubAgentResult, error) {
	child, model := l.buildChildLoop(spec)
	childGate := child.cfg.Gate

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
		Background: spec.Background,
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
	if !spec.DisableModeTools {
		child.reg = child.reg.WithOverrides(childEnterPlan, childExitPlan, childEnterAuto, childExitAuto)
	}

	start := time.Now()
	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: prompt}}},
	}
	var totalUsage api.Usage
	baseHandler := subAgentEventHandler(childID)
	history, err := child.Run(ctx, msgs, func(ev LoopEvent) {
		baseHandler(ev)
		if ev.Type == EventUsage {
			totalUsage.InputTokens += ev.Usage.InputTokens
			totalUsage.OutputTokens += ev.Usage.OutputTokens
			totalUsage.CacheCreationInputTokens += ev.Usage.CacheCreationInputTokens
			totalUsage.CacheReadInputTokens += ev.Usage.CacheReadInputTokens
		}
	})
	durationMs := time.Since(start).Milliseconds()

	// Report sub-agent token cost to the parent session ledger.
	l.mu.RLock()
	onSubAgentUsage := l.cfg.OnSubAgentUsage
	l.mu.RUnlock()
	if onSubAgentUsage != nil && totalUsage.InputTokens+totalUsage.OutputTokens > 0 {
		onSubAgentUsage(model, totalUsage)
	}

	agentID := fmt.Sprintf("agent-%x", start.UnixNano()&0xffffff)

	if coordinator.IsActive() {
		if err != nil && !errors.Is(err, ErrMaxTurnsExceeded) {
			notif := coordinator.TaskNotification(
				agentID, "failed",
				fmt.Sprintf("Agent failed: %v", err),
				"", 0, 0, durationMs,
			)
			return SubAgentResult{Text: notif, DurationMs: durationMs}, nil
		}
		toolUses := countToolUses(history)
		text := extractLastAssistantText(history)
		summary := "Agent completed"
		if errors.Is(err, ErrMaxTurnsExceeded) {
			summary = "Agent reached turn limit; response may be incomplete"
			if text != "" {
				text += "\n\n[Note: agent reached its turn limit; response may be incomplete]"
			} else {
				text = "[Agent reached its turn limit without producing text output]"
			}
		}
		notif := coordinator.TaskNotification(
			agentID, "completed",
			summary,
			text, 0, toolUses, durationMs,
		)
		return SubAgentResult{Text: notif, Usage: totalUsage, ToolUses: toolUses, DurationMs: durationMs}, nil
	}

	if errors.Is(err, ErrMaxTurnsExceeded) {
		// The child ran to its turn cap without a clean end_turn. Return the
		// partial last-assistant text with an incomplete marker so the parent
		// model knows the subagent did not finish rather than guessing.
		text := extractLastAssistantText(history)
		if text != "" {
			text += "\n\n[Note: agent reached its turn limit; response may be incomplete]"
		} else {
			text = "[Agent reached its turn limit without producing text output]"
		}
		return SubAgentResult{
			Text:       text,
			Usage:      totalUsage,
			ToolUses:   countToolUses(history),
			DurationMs: durationMs,
		}, nil
	}
	if err != nil {
		return SubAgentResult{DurationMs: durationMs}, err
	}
	text := extractLastAssistantText(history)
	if text == "" {
		text = "[Subagent completed without producing text output]"
	}
	return SubAgentResult{
		Text:       text,
		Usage:      totalUsage,
		ToolUses:   countToolUses(history),
		DurationMs: durationMs,
	}, nil
}

// RunSubAgentWithTools is a convenience wrapper around RunSubAgentTyped that
// only restricts the tool set, keeping the model and system prompt unchanged.
func (l *Loop) RunSubAgentWithTools(ctx context.Context, prompt string, tools []string) (string, error) {
	r, err := l.RunSubAgentTyped(ctx, prompt, SubAgentSpec{Tools: tools})
	return r.Text, err
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

	// Cap subagent iterations at DefaultSubAgentMaxTurns unless the parent
	// explicitly configured a higher limit. The parent loop's own MaxTurns
	// (set to 50 in mainrepl.go) is inherited, so this only affects loops that
	// did not set a limit (e.g. bgreview, council, tests with MaxTurns=0).
	//
	// Note: there is no way to request an explicitly unbounded child loop.
	// MaxTurns=0 in a child LoopConfig is always replaced by DefaultSubAgentMaxTurns.
	// If an unbounded child is ever needed, introduce a dedicated sentinel value.
	if childCfg.MaxTurns == 0 {
		childCfg.MaxTurns = DefaultSubAgentMaxTurns
	}

	// Sub-agents must not fire parent-session side effects. Strip callbacks
	// and notifications so a sub-agent end_turn doesn't send desktop pings
	// or re-trigger memory extraction / session-memory updates.
	childCfg.NotifyOnComplete = false
	childCfg.OnEndTurn = nil
	childCfg.OnToolBatchComplete = nil
	childCfg.OnCompact = nil
	childCfg.OnFileAccess = nil
	childCfg.OnSubAgentUsage = nil    // child sub-agents report to their own parent
	childCfg.BackgroundReviewer = nil // sub-agents must not chain-trigger reviews

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
	var totalUsage api.Usage
	baseHandler := subAgentEventHandler(childID)
	history, err := child.Run(ctx, msgs, func(ev LoopEvent) {
		baseHandler(ev)
		if ev.Type == EventUsage {
			totalUsage.InputTokens += ev.Usage.InputTokens
			totalUsage.OutputTokens += ev.Usage.OutputTokens
			totalUsage.CacheCreationInputTokens += ev.Usage.CacheCreationInputTokens
			totalUsage.CacheReadInputTokens += ev.Usage.CacheReadInputTokens
		}
	})

	// Report sub-agent token cost to the parent session ledger.
	l.mu.RLock()
	onSubAgentUsage := l.cfg.OnSubAgentUsage
	l.mu.RUnlock()
	if onSubAgentUsage != nil && totalUsage.InputTokens+totalUsage.OutputTokens > 0 {
		onSubAgentUsage(model, totalUsage)
	}

	agentID := fmt.Sprintf("agent-%x", start.UnixNano()&0xffffff)

	if coordinator.IsActive() {
		if err != nil && !errors.Is(err, ErrMaxTurnsExceeded) {
			notif := coordinator.TaskNotification(
				agentID, "failed",
				fmt.Sprintf("Agent failed: %v", err),
				"", 0, 0, time.Since(start).Milliseconds(),
			)
			return notif, nil
		}
		toolUses := countToolUses(history)
		result := extractLastAssistantText(history)
		summary := "Agent completed"
		if errors.Is(err, ErrMaxTurnsExceeded) {
			summary = "Agent reached turn limit; response may be incomplete"
			if result != "" {
				result += "\n\n[Note: agent reached its turn limit; response may be incomplete]"
			} else {
				result = "[Agent reached its turn limit without producing text output]"
			}
		}
		notif := coordinator.TaskNotification(
			agentID, "completed",
			summary,
			result, 0, toolUses, time.Since(start).Milliseconds(),
		)
		return notif, nil
	}

	if errors.Is(err, ErrMaxTurnsExceeded) {
		// The child ran to its turn cap without a clean end_turn.
		text := extractLastAssistantText(history)
		if text != "" {
			text += "\n\n[Note: agent reached its turn limit; response may be incomplete]"
		} else {
			text = "[Agent reached its turn limit without producing text output]"
		}
		return text, nil
	}
	if err != nil {
		return "", err
	}
	text := extractLastAssistantText(history)
	if text == "" {
		text = "[Subagent completed without producing text output]"
	}
	return text, nil
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
