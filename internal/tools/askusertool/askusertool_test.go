package askusertool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAskUserQuestion_ReturnsAnswer(t *testing.T) {
	tool := &AskUserQuestion{
		Ask: func(ctx context.Context, q string, opts []Option, multi bool) []string {
			return []string{"option A"}
		},
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"Which approach?","options":[{"label":"option A"},{"label":"option B"}]}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %v", res.Content)
	}
	if res.StopTurn {
		t.Error("real answer path must not set StopTurn")
	}
	if len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "option A") {
		t.Errorf("result = %v; want option A", res.Content)
	}
}

func TestAskUserQuestion_MultiSelect(t *testing.T) {
	var capturedMulti bool
	tool := &AskUserQuestion{
		Ask: func(ctx context.Context, q string, opts []Option, multi bool) []string {
			capturedMulti = multi
			return []string{"A", "B"}
		},
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"Pick all that apply","multiSelect":true}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %v", res.Content)
	}
	if !capturedMulti {
		t.Error("multiSelect should be passed to Ask callback")
	}
	if !strings.Contains(res.Content[0].Text, "A") || !strings.Contains(res.Content[0].Text, "B") {
		t.Errorf("multi answers not joined: %v", res.Content[0].Text)
	}
}

func TestAskUserQuestion_NoAskCallback(t *testing.T) {
	tool := &AskUserQuestion{}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"pick one"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error when no Ask callback")
	}
}

func TestAskUserQuestion_NoAnswer(t *testing.T) {
	tool := &AskUserQuestion{
		Ask: func(ctx context.Context, q string, opts []Option, multi bool) []string {
			return nil // user cancelled
		},
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"question":"pick one"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Error("expected non-error neutral result when user dismisses question")
	}
	if !res.StopTurn {
		t.Error("dismiss path must set StopTurn = true to hand control back to user")
	}
	if len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "dismissed") {
		t.Errorf("expected dismiss framing in result, got: %v", res.Content)
	}
}

func TestAskUserQuestion_InvalidInput(t *testing.T) {
	tool := &AskUserQuestion{}
	res, err := tool.Execute(context.Background(), json.RawMessage(`not-json`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error for invalid JSON")
	}
}

func TestAskUserQuestion_EmptyQuestion(t *testing.T) {
	tool := &AskUserQuestion{
		Ask: func(ctx context.Context, q string, opts []Option, multi bool) []string {
			return []string{"answer"}
		},
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"question":""}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error for empty question")
	}
}

func TestAskUserQuestion_Metadata(t *testing.T) {
	tool := &AskUserQuestion{}
	if tool.Name() != "AskUserQuestion" {
		t.Errorf("Name = %q", tool.Name())
	}
	if !tool.IsReadOnly(nil) {
		t.Error("should be read-only")
	}
	if tool.IsConcurrencySafe(nil) {
		t.Error("should not be concurrency safe (blocks on user input)")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Errorf("InputSchema invalid JSON: %v", err)
	}
}
