package planmodetool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/icehunter/conduit/internal/permissions"
)

func TestEnterPlanMode_SetsMode(t *testing.T) {
	var got permissions.Mode
	tool := &EnterPlanMode{
		SetMode: func(m permissions.Mode) { got = m },
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %v", res.Content)
	}
	if got != permissions.ModePlan {
		t.Errorf("mode = %v; want ModePlan", got)
	}
}

func TestEnterPlanMode_DeniedByUser(t *testing.T) {
	tool := &EnterPlanMode{
		AskEnter: func(ctx context.Context) bool { return false },
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error result when user declines")
	}
}

func TestEnterPlanMode_AlreadyPlanSkipsPrompt(t *testing.T) {
	var asked bool
	tool := &EnterPlanMode{
		CurrentMode: func() permissions.Mode { return permissions.ModePlan },
		AskEnter: func(ctx context.Context) bool {
			asked = true
			return false
		},
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %v", res.Content)
	}
	if asked {
		t.Error("AskEnter should not be called when already in plan mode")
	}
}

func TestEnterPlanMode_Metadata(t *testing.T) {
	tool := &EnterPlanMode{}
	if tool.Name() != "EnterPlanMode" {
		t.Errorf("Name = %q", tool.Name())
	}
	if !tool.IsReadOnly(nil) {
		t.Error("EnterPlanMode should be read-only")
	}
	if !tool.IsConcurrencySafe(nil) {
		t.Error("EnterPlanMode should be concurrency safe")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Errorf("InputSchema invalid JSON: %v", err)
	}
}

func TestExitPlanMode_ApprovedEnablesAutoMode(t *testing.T) {
	var got permissions.Mode
	tool := &ExitPlanMode{
		SetMode:    func(m permissions.Mode) { got = m },
		AskApprove: func(ctx context.Context, plan string) bool { return true },
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"plan":"implement feature X"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %v", res.Content)
	}
	if got != permissions.ModeBypassPermissions {
		t.Errorf("mode = %v; want ModeBypassPermissions", got)
	}
}

func TestExitPlanMode_RejectedKeepsPlanMode(t *testing.T) {
	var modeSet bool
	tool := &ExitPlanMode{
		SetMode:    func(_ permissions.Mode) { modeSet = true },
		AskApprove: func(ctx context.Context, plan string) bool { return false },
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"plan":"draft plan"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error result when plan rejected")
	}
	if modeSet {
		t.Error("SetMode should not be called when plan is rejected")
	}
}

func TestExitPlanMode_InvalidInput(t *testing.T) {
	tool := &ExitPlanMode{}
	res, err := tool.Execute(context.Background(), json.RawMessage(`not-json`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error for invalid input")
	}
}

func TestExitPlanMode_Metadata(t *testing.T) {
	tool := &ExitPlanMode{}
	if tool.Name() != "ExitPlanMode" {
		t.Errorf("Name = %q", tool.Name())
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Errorf("InputSchema invalid JSON: %v", err)
	}
}
