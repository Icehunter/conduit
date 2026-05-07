// Package planmodetool implements the EnterPlanMode and ExitPlanMode tools.
//
// These tools let the model request permission to enter/exit plan mode.
// Entering requires user consent (the TUI shows a prompt); exiting presents
// the plan and asks the user to approve before implementation begins.
//
// Port of src/tools/EnterPlanModeTool/ and src/tools/ExitPlanModeTool/.
package planmodetool

import (
	"context"
	"encoding/json"

	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/tool"
)

const (
	enterName = "EnterPlanMode"
	exitName  = "ExitPlanMode"
)

// EnterPlanMode is the tool the model calls to request plan mode entry.
// The TUI must install SetMode and AskEnter callbacks before use.
type EnterPlanMode struct {
	// SetMode changes the active permission mode (nil = no-op).
	SetMode func(permissions.Mode)
	// CurrentMode returns the active permission mode. When already in plan mode,
	// EnterPlanMode should be seamless and not prompt again.
	CurrentMode func() permissions.Mode
	// AskEnter, when non-nil, prompts the user for approval before entering.
	// Returns true if the user consents.
	AskEnter func(ctx context.Context) bool
}

func (t *EnterPlanMode) Name() string        { return enterName }
func (t *EnterPlanMode) Description() string { return enterDescription }
func (t *EnterPlanMode) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (t *EnterPlanMode) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *EnterPlanMode) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *EnterPlanMode) Execute(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	alreadyPlan := t.CurrentMode != nil && t.CurrentMode() == permissions.ModePlan
	if !alreadyPlan && t.AskEnter != nil && !t.AskEnter(ctx) {
		return tool.ErrorResult("User declined to enter plan mode."), nil
	}
	if t.SetMode != nil {
		t.SetMode(permissions.ModePlan)
	}
	return tool.TextResult(`Entered plan mode. You should now:
1. Thoroughly explore the codebase using Glob, Grep, and Read tools
2. Understand existing patterns and architecture
3. Design an implementation approach
4. Use AskUserQuestion if you need to clarify approaches
5. Call ExitPlanMode when ready to present your plan for approval

Remember: DO NOT write or edit any files yet. This is a read-only exploration and planning phase.`), nil
}

// PlanApprovalDecision is the result of the ExitPlanMode user approval prompt.
type PlanApprovalDecision struct {
	// Approved is true when the user accepts the plan.
	Approved bool
	// Mode is the permission mode to activate on approval.
	// Defaults to ModeBypassPermissions when zero.
	Mode permissions.Mode
	// Feedback is optional text from the user on rejection.
	// Sent back to the model as part of the error message.
	Feedback string
}

// ExitPlanMode is the tool the model calls to present a plan and ask for approval.
// The user is shown the plan text and either approves or rejects.
type ExitPlanMode struct {
	// SetMode changes the active permission mode (nil = no-op).
	SetMode func(permissions.Mode)
	// AskApprove, when non-nil, shows the plan to the user and returns a
	// PlanApprovalDecision. When nil, the plan is auto-approved with
	// ModeBypassPermissions.
	AskApprove func(ctx context.Context, plan string) PlanApprovalDecision

	// IsCouncilMode returns true when the current permission mode is ModeCouncil.
	// When true, StartCouncil is called instead of AskApprove.
	IsCouncilMode func() bool

	// StartCouncil runs the council debate and returns the approval decision.
	// Called instead of AskApprove when IsCouncilMode returns true.
	StartCouncil func(ctx context.Context, plan string) PlanApprovalDecision
}

// exitInput is the JSON input for ExitPlanMode.
type exitInput struct {
	Plan string `json:"plan"`
}

func (t *ExitPlanMode) Name() string        { return exitName }
func (t *ExitPlanMode) Description() string { return exitDescription }
func (t *ExitPlanMode) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"plan": {
			"type": "string",
			"description": "The implementation plan to present to the user for approval."
		}
	},
	"required": ["plan"],
	"additionalProperties": false
}`)
}
func (t *ExitPlanMode) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *ExitPlanMode) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *ExitPlanMode) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var inp exitInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return tool.ErrorResult("invalid input: " + err.Error()), nil
	}

	var decision PlanApprovalDecision
	if t.IsCouncilMode != nil && t.IsCouncilMode() && t.StartCouncil != nil {
		decision = t.StartCouncil(ctx, inp.Plan)
	} else if t.AskApprove != nil {
		decision = t.AskApprove(ctx, inp.Plan)
	} else {
		// Auto-approve with bypass when no callback (non-interactive mode).
		decision = PlanApprovalDecision{Approved: true, Mode: permissions.ModeBypassPermissions}
	}

	if !decision.Approved {
		msg := "User rejected the plan."
		if decision.Feedback != "" {
			msg += " Feedback: " + decision.Feedback + ". Return to plan mode and revise."
		} else {
			msg += " Return to plan mode and revise your approach."
		}
		return tool.ErrorResult(msg), nil
	}

	if t.SetMode != nil {
		mode := decision.Mode
		if mode == "" {
			mode = permissions.ModeBypassPermissions
		}
		t.SetMode(mode)
	}

	modeLabel := "Auto mode"
	switch decision.Mode {
	case permissions.ModeAcceptEdits:
		modeLabel = "Accept-edits mode"
	case permissions.ModeDefault:
		modeLabel = "Default mode"
	}
	return tool.TextResult(modeLabel + " enabled. You may now begin implementation. Follow the plan you presented."), nil
}

const enterDescription = `Requests permission to enter plan mode for complex tasks requiring exploration and design. Use proactively before non-trivial implementation. In plan mode you explore the codebase with read-only tools and design an approach. When ready, use ExitPlanMode to present your plan for user approval.`

const exitDescription = `Exits plan mode by presenting your implementation plan to the user for approval. The plan field should contain a clear, structured description of what you will implement and how. If the user approves, you may begin writing code.`
