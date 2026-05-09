package decisiontool

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/decisionlog"
)

func withHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
}

func execute(t *testing.T, raw string) (string, bool) {
	t.Helper()
	tool := New()
	res, err := tool.Execute(context.Background(), json.RawMessage(raw))
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	return res.Content[0].Text, res.IsError
}

func TestExecute_HappyPath(t *testing.T) {
	withHome(t)
	text, isErr := execute(t, `{"kind":"chose","scope":"auth","summary":"middleware B"}`)
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	if !strings.Contains(text, "chose") || !strings.Contains(text, "auth") {
		t.Errorf("want kind/scope in result; got %q", text)
	}

	cwd, _ := os.Getwd()
	entries, err := decisionlog.Recent(cwd, 1)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(entries) != 1 || entries[0].Scope != "auth" {
		t.Errorf("want 1 persisted entry; got %+v", entries)
	}
}

func TestExecute_MissingSummary(t *testing.T) {
	withHome(t)
	_, isErr := execute(t, `{"kind":"chose","scope":"x"}`)
	if !isErr {
		t.Errorf("want error result when summary missing")
	}
}

func TestExecute_InvalidJSON(t *testing.T) {
	withHome(t)
	_, isErr := execute(t, `{not json}`)
	if !isErr {
		t.Errorf("want error result on invalid JSON")
	}
}

func TestExecute_WithOptionalFields(t *testing.T) {
	withHome(t)
	raw := `{"kind":"ruled_out","scope":"pattern-A","summary":"too hard to test","why":"PR #412 showed coverage gaps","files":["internal/auth/middleware.go"]}`
	_, isErr := execute(t, raw)
	if isErr {
		t.Fatal("unexpected error")
	}
	cwd, _ := os.Getwd()
	entries, _ := decisionlog.Recent(cwd, 1)
	if entries[0].Why == "" || len(entries[0].Files) == 0 {
		t.Errorf("optional fields not persisted: %+v", entries[0])
	}
}

func TestSchema(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(New().InputSchema(), &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatal("schema missing 'required'")
	}
	want := map[string]bool{"kind": true, "scope": true, "summary": true}
	for _, r := range required {
		delete(want, r.(string))
	}
	if len(want) > 0 {
		t.Errorf("schema 'required' missing fields: %v", want)
	}
}
