// Package automodetool implements the EnterAutoMode and ExitAutoMode tools.
//
// These tools let the model request to enter or leave bypassPermissions (auto)
// mode outside the plan-mode flow. EnterAutoMode prompts the user for consent;
// ExitAutoMode returns to default mode without prompting.
package automodetool

import (
	"context"
	"encoding/json"

	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/tool"
)

const (
	enterName = "EnterAutoMode"
	exitName  = "ExitAutoMode"
)

// EnterAutoMode requests permission to switch to bypassPermissions (auto) mode.
type EnterAutoMode struct {
	// SetMode changes the active permission mode (nil = no-op).
	SetMode func(permissions.Mode)
	// CurrentMode returns the active permission mode. When already in auto mode,
	// the prompt is skipped.
	CurrentMode func() permissions.Mode
	// AskEnter, when non-nil, prompts the user for approval before entering.
	// Returns true if the user consents.
	AskEnter func(ctx context.Context) bool
}

func (t *EnterAutoMode) Name() string { return enterName }
func (t *EnterAutoMode) Description() string {
	return "Requests permission to enter auto mode (bypassPermissions). " +
		"In auto mode, tool calls are executed without per-call prompts. " +
		"Use when the user has approved autonomous operation for the current task."
}
func (t *EnterAutoMode) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (t *EnterAutoMode) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *EnterAutoMode) IsConcurrencySafe(_ json.RawMessage) bool { return true }

// Execute implements tool.Tool.
func (t *EnterAutoMode) Execute(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	alreadyAuto := t.CurrentMode != nil && t.CurrentMode() == permissions.ModeBypassPermissions
	if !alreadyAuto && t.AskEnter != nil && !t.AskEnter(ctx) {
		return tool.ErrorResult("User declined to enter auto mode."), nil
	}
	if t.SetMode != nil {
		t.SetMode(permissions.ModeBypassPermissions)
	}
	return tool.TextResult("Entered auto mode. Tool calls will proceed without per-call prompts."), nil
}

// ExitAutoMode switches back to default mode (asks for non-read-only tools).
type ExitAutoMode struct {
	// SetMode changes the active permission mode (nil = no-op).
	SetMode func(permissions.Mode)
}

func (t *ExitAutoMode) Name() string { return exitName }
func (t *ExitAutoMode) Description() string {
	return "Exits auto mode and returns to default permission mode, " +
		"where non-read-only tool calls require user approval."
}
func (t *ExitAutoMode) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (t *ExitAutoMode) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *ExitAutoMode) IsConcurrencySafe(_ json.RawMessage) bool { return true }

// Execute implements tool.Tool.
func (t *ExitAutoMode) Execute(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	if t.SetMode != nil {
		t.SetMode(permissions.ModeDefault)
	}
	return tool.TextResult("Exited auto mode. Tool calls now require per-call approval for non-read-only operations."), nil
}
