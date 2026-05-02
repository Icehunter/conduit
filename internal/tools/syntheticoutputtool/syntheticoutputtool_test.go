package syntheticoutputtool

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSyntheticOutput_ValidJSON(t *testing.T) {
	var captured json.RawMessage
	tool := &SyntheticOutput{
		OnOutput: func(d json.RawMessage) { captured = d },
	}

	input := json.RawMessage(`{"name":"test","value":42}`)
	res, err := tool.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %v", res.Content)
	}
	if captured == nil {
		t.Error("OnOutput was not called")
	}
	// Result should be pretty-printed JSON.
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Errorf("result not valid JSON: %v", err)
	}
	if out["name"] != "test" {
		t.Errorf("name = %v", out["name"])
	}
}

func TestSyntheticOutput_InvalidJSON(t *testing.T) {
	tool := &SyntheticOutput{}
	res, err := tool.Execute(context.Background(), json.RawMessage(`not-json`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error for invalid JSON")
	}
}

func TestSyntheticOutput_NoCallback(t *testing.T) {
	tool := &SyntheticOutput{}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("unexpected error: %v", res.Content)
	}
}

func TestSyntheticOutput_Metadata(t *testing.T) {
	tool := &SyntheticOutput{}
	if tool.Name() != "StructuredOutput" {
		t.Errorf("Name = %q", tool.Name())
	}
	if !tool.IsReadOnly(nil) {
		t.Error("should be read-only")
	}
	if !tool.IsConcurrencySafe(nil) {
		t.Error("should be concurrency safe")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Errorf("InputSchema invalid JSON: %v", err)
	}
}
