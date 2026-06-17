package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/subagent"
	"github.com/icehunter/conduit/internal/team"
	"github.com/icehunter/conduit/internal/tools/automodetool"
	"github.com/icehunter/conduit/internal/tools/planmodetool"
	"github.com/icehunter/conduit/internal/tools/sendmessagetool"
)

// buildChildLoop constructs a child Loop from spec, inheriting the parent's client,
// config, and registry. Does NOT register with any tracker or add mode-change tools —
// callers do that separately so they can inject tracker IDs into notifyMode closures.
// Returns the child loop and the resolved model name.
func (l *Loop) buildChildLoop(spec SubAgentSpec) (child *Loop, model string) {
	model = l.BackgroundModel()
	if spec.Model != "" {
		model = l.resolveModelAlias(spec.Model)
	} else if spec.Role != "" {
		model = l.resolveModelAlias(spec.Role)
	}

	l.mu.RLock()
	childCfg := l.cfg
	childClient := l.client
	parentReg := l.reg
	l.mu.RUnlock()

	if spec.Role != "" && spec.Model == "" && childCfg.RoleResolver != nil {
		if roleModel, roleClient, ok := childCfg.RoleResolver(spec.Role); ok {
			if roleModel != "" {
				model = roleModel
			}
			if roleClient != nil {
				childClient = roleClient
			}
		}
	}
	if spec.Client != nil {
		childClient = spec.Client
	}

	if childCfg.MaxTurns == 0 {
		childCfg.MaxTurns = DefaultSubAgentMaxTurns
	}

	// Strip side-effect callbacks — children must not chain-trigger parent events.
	childCfg.NotifyOnComplete = false
	childCfg.OnEndTurn = nil
	childCfg.OnToolBatchComplete = nil
	childCfg.OnCompact = nil
	childCfg.OnFileAccess = nil
	childCfg.OnSubAgentUsage = nil
	childCfg.BackgroundReviewer = nil
	childCfg.Model = model
	if spec.MaxTokens > 0 {
		childCfg.MaxTokens = spec.MaxTokens
	}
	if spec.SystemPrompt != "" {
		childCfg.System = append(append([]api.SystemBlock(nil), childCfg.System...), api.SystemBlock{
			Type: "text",
			Text: spec.SystemPrompt,
		})
	}

	childReg := parentReg
	if len(spec.Tools) > 0 {
		childReg = parentReg.Subset(spec.Tools)
	}
	if len(spec.ExtraTools) > 0 {
		childReg = childReg.WithOverrides(spec.ExtraTools...)
	}

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
	childCfg.AskPermission = nil

	child = &Loop{client: childClient, reg: childReg, cfg: childCfg}
	return child, model
}

// runDeliveryPump reads incoming team messages from inbox and injects them into
// child as programmatic user messages. Exits when inbox is closed or done is closed.
//
// This is the delivery mechanism: teammate inbox → child.InjectMessage.
// Run this in a goroutine; signal done (close it) when the child loop returns
// so the pump does not block on an inbox that will never be re-read.
func runDeliveryPump(child *Loop, inbox <-chan team.Message, done <-chan struct{}) {
	for {
		select {
		case msg, ok := <-inbox:
			if !ok {
				return
			}
			child.InjectMessage(fmt.Sprintf("<team-message from=%q>%s</team-message>", msg.From, msg.Text))
		case <-done:
			return
		}
	}
}

// SpawnTeammate starts an async teammate loop registered with the given team.
// The teammate is built from spec (a SendMessage tool for tm is always added),
// tracked in the subagent roster, and run in a background goroutine.
//
// When the child loop finishes (for any reason), the teammate:
//  1. Is unregistered from tm.
//  2. Is removed from the subagent tracker.
//  3. Sends a KindCompletion message to the lead inbox.
//
// SpawnTeammate itself is non-blocking; errors during child.Run are reported via
// the completion message, not as a Go error. Returns the teammate's subagent ID
// and any registration error (duplicate name, shut-down team, etc.).
func (l *Loop) SpawnTeammate(ctx context.Context, name, prompt string, spec SubAgentSpec, tm *team.Team) (string, error) {
	tmCtx, cancel := context.WithCancel(ctx)
	member, err := tm.Register(name, cancel)
	if err != nil {
		cancel()
		return "", fmt.Errorf("spawn teammate %q: %w", name, err)
	}

	// Always provide SendMessage so the teammate can send messages back.
	spec.ExtraTools = append(spec.ExtraTools, sendmessagetool.NewFor(name, tm))

	child, _ := l.buildChildLoop(spec)

	childID := fmt.Sprintf("teammate-%x", time.Now().UnixNano()&0xffffff)
	label := name
	if len(prompt) > 0 {
		s := strings.ReplaceAll(strings.TrimSpace(prompt), "\n", " ")
		if len(s) > 30 {
			s = s[:30]
		}
		label = s
	}
	subagent.Default.Add(subagent.Entry{
		ID:        childID,
		Label:     label,
		StartedAt: time.Now(),
	})

	// Build mode-change tools that update both the child gate and the tracker.
	childGate := child.cfg.Gate
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
		AskApprove: nil, // Phase 6: wire plan-approval through team mailbox
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

	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: prompt}}},
	}

	go func() {
		runDone := make(chan struct{})
		go runDeliveryPump(child, member.Inbox, runDone)

		baseHandler := subAgentEventHandler(childID)
		history, runErr := child.Run(tmCtx, msgs, func(ev LoopEvent) {
			baseHandler(ev)
		})
		close(runDone)

		// Order matters: Unregister before sending completion so the team roster
		// is already clean when the lead processes the message.
		tm.Unregister(name)
		subagent.Default.Remove(childID)

		completionText := extractLastAssistantText(history)
		if runErr != nil {
			completionText = fmt.Sprintf("teammate %q stopped: %v", name, runErr)
		}
		_ = tm.Send(team.Message{
			From: name,
			To:   team.ReservedLeadName,
			Kind: team.KindCompletion,
			Text: completionText,
		})
	}()

	return childID, nil
}
