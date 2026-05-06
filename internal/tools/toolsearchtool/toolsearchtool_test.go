package toolsearchtool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/tool"
)

// stubTool is a minimal tool.Tool for testing the registry.
type stubTool struct{ name, desc string }

func (s *stubTool) Name() string                           { return s.name }
func (s *stubTool) Description() string                    { return s.desc }
func (s *stubTool) InputSchema() json.RawMessage           { return json.RawMessage("{}") }
func (s *stubTool) IsReadOnly(json.RawMessage) bool        { return true }
func (s *stubTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (s *stubTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.TextResult("ok"), nil
}

func newTestRegistry(tools ...tool.Tool) *tool.Registry {
	reg := tool.NewRegistry()
	for _, t := range tools {
		reg.Register(t)
	}
	return reg
}

func TestToolSearch_KeywordMatch(t *testing.T) {
	reg := newTestRegistry(
		&stubTool{"Bash", "Executes a bash command"},
		&stubTool{"Read", "Reads a file"},
		&stubTool{"Edit", "Edits a file"},
	)
	ts := New(reg)

	raw, _ := json.Marshal(map[string]any{"query": "bash", "max_results": 5})
	res, err := ts.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("IsError=true: %v", res.Content)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	matches := out["matches"].([]any)
	if len(matches) != 1 {
		t.Errorf("matches len = %d; want 1", len(matches))
	}
	name := matches[0].(map[string]any)["name"].(string)
	if !strings.EqualFold(name, "Bash") {
		t.Errorf("matched tool name = %q; want Bash", name)
	}
}

func TestToolSearch_SelectByName(t *testing.T) {
	reg := newTestRegistry(
		&stubTool{"Bash", "Executes a bash command"},
		&stubTool{"Edit", "Edits a file"},
	)
	ts := New(reg)

	raw, _ := json.Marshal(map[string]any{"query": "select:Edit"})
	res, err := ts.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(res.Content[0].Text), &out)
	matches := out["matches"].([]any)
	if len(matches) != 1 {
		t.Errorf("select: matches len = %d; want 1", len(matches))
	}
}

func TestToolSearch_NoMatch(t *testing.T) {
	reg := newTestRegistry(&stubTool{"Bash", "executes bash"})
	ts := New(reg)

	raw, _ := json.Marshal(map[string]any{"query": "zzznomatch"})
	res, _ := ts.Execute(context.Background(), raw)
	var out map[string]any
	_ = json.Unmarshal([]byte(res.Content[0].Text), &out)
	// matches is null when no results — treat nil as empty.
	matches, _ := out["matches"].([]any)
	if len(matches) != 0 {
		t.Errorf("matches len = %d; want 0", len(matches))
	}
}
