package planmodetool

import (
	"context"
	"encoding/json"
	"strings"
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

// TestEnterPlanMode_CouncilModeReturnsError verifies that calling
// EnterPlanMode while already in council mode produces an error result so the
// model self-corrects to call ExitPlanMode directly. Council mode is a
// read-only planning state — entering plan-mode-on-top would be redundant.
func TestEnterPlanMode_CouncilModeReturnsError(t *testing.T) {
	var modeSet bool
	tool := &EnterPlanMode{
		CurrentMode: func() permissions.Mode { return permissions.ModeCouncil },
		SetMode:     func(_ permissions.Mode) { modeSet = true },
		AskEnter:    func(_ context.Context) bool { return true },
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error result when calling EnterPlanMode in council mode")
	}
	if modeSet {
		t.Error("SetMode should not be called when council mode rejects EnterPlanMode")
	}
	// The error message must mention ExitPlanMode so the model knows the
	// correct alternative path.
	if len(res.Content) == 0 {
		t.Fatal("error result has no content")
	}
	got := strings.ToLower(res.Content[0].Text)
	if !strings.Contains(got, "exitplanmode") {
		t.Errorf("error message should reference ExitPlanMode, got: %s", got)
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
		SetMode: func(m permissions.Mode) { got = m },
		AskApprove: func(_ context.Context, _ string) PlanApprovalDecision {
			return PlanApprovalDecision{Approved: true, Mode: permissions.ModeBypassPermissions}
		},
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

func TestExitPlanMode_ApprovedAcceptEditsMode(t *testing.T) {
	var got permissions.Mode
	tool := &ExitPlanMode{
		SetMode: func(m permissions.Mode) { got = m },
		AskApprove: func(_ context.Context, _ string) PlanApprovalDecision {
			return PlanApprovalDecision{Approved: true, Mode: permissions.ModeAcceptEdits}
		},
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"plan":"implement feature Y"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %v", res.Content)
	}
	if got != permissions.ModeAcceptEdits {
		t.Errorf("mode = %v; want ModeAcceptEdits", got)
	}
}

func TestExitPlanMode_RejectedKeepsPlanMode(t *testing.T) {
	var modeSet bool
	tool := &ExitPlanMode{
		SetMode: func(_ permissions.Mode) { modeSet = true },
		AskApprove: func(_ context.Context, _ string) PlanApprovalDecision {
			return PlanApprovalDecision{Approved: false}
		},
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

func TestExitPlanMode_RejectedWithFeedback(t *testing.T) {
	tool := &ExitPlanMode{
		SetMode: func(_ permissions.Mode) {},
		AskApprove: func(_ context.Context, _ string) PlanApprovalDecision {
			return PlanApprovalDecision{Approved: false, Feedback: "please also add tests"}
		},
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"plan":"draft plan"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error result when plan rejected with feedback")
	}
	if len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "please also add tests") {
		t.Errorf("expected feedback in error message, got: %v", res.Content)
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
