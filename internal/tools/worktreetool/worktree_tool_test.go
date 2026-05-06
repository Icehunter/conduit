package worktreetool

import (
	"context"
	"encoding/json"
	"testing"
)

func TestSanitizeSlug(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"feature/my-thing", "feature-my-thing"},
		{"valid-name_1.0", "valid-name_1.0"},
		{"spaces in name", "spaces-in-name"},
		{"", ""},
		{"---leading---", "leading"},
	}
	for _, tt := range tests {
		got := sanitizeSlug(tt.in)
		if got != tt.want {
			t.Errorf("sanitizeSlug(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func TestEnterWorktree_Metadata(t *testing.T) {
	tool := &EnterWorktree{}
	if tool.Name() != "EnterWorktree" {
		t.Errorf("Name = %q", tool.Name())
	}
	if tool.IsReadOnly(nil) {
		t.Error("EnterWorktree should not be read-only")
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Errorf("InputSchema invalid JSON: %v", err)
	}
}

func TestExitWorktree_Metadata(t *testing.T) {
	tool := &ExitWorktree{}
	if tool.Name() != "ExitWorktree" {
		t.Errorf("Name = %q", tool.Name())
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Errorf("InputSchema invalid JSON: %v", err)
	}
}

func TestExitWorktree_InvalidAction(t *testing.T) {
	tool := &ExitWorktree{}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"invalid"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error for invalid action")
	}
}

func TestExitWorktree_MissingAction(t *testing.T) {
	tool := &ExitWorktree{}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error for missing action")
	}
}

func TestEnterWorktree_NotInGit(t *testing.T) {
	// When not in a git repo, should return an error result.
	tool := &EnterWorktree{
		GetCwd: func() string { return t.TempDir() },
	}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Error("expected error when not in a git repo")
	}
}
