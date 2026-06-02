package skillmanagetool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/skillusage"
)

// mustExec is a helper to execute a tool action and require no Go error.
func mustExec(t *testing.T, tool *Tool, raw map[string]any) (string, bool) {
	t.Helper()
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	res, err := tool.Execute(context.Background(), data)
	if err != nil {
		t.Fatalf("Execute returned unexpected Go error: %v", err)
	}
	// Extract text from all content blocks.
	var sb strings.Builder
	for _, b := range res.Content {
		sb.WriteString(b.Text)
	}
	return sb.String(), res.IsError
}

func TestCreate_ProjectScope(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(t.TempDir(), ".conduit"))

	tk := New(cwd)
	text, isErr := mustExec(t, tk, map[string]any{
		"action":  "create",
		"name":    "my-skill",
		"content": "# My Skill\nDoes a thing.",
		"scope":   "project",
	})
	if isErr {
		t.Fatalf("expected success, got error: %s", text)
	}

	want := filepath.Join(cwd, ".claude", "skills", "my-skill", "SKILL.md")
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("SKILL.md not written to project path: %v", err)
	}
	if !strings.Contains(string(data), "My Skill") {
		t.Errorf("unexpected content: %s", data)
	}
}

func TestCreate_GlobalConduitScope(t *testing.T) {
	cwd := t.TempDir()
	conduitDir := filepath.Join(t.TempDir(), ".conduit")
	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)

	tk := New(cwd, WithAgentProvenance())
	text, isErr := mustExec(t, tk, map[string]any{
		"action":  "create",
		"name":    "reusable-skill",
		"content": "# Reusable\nWorks anywhere.",
		"scope":   "global-conduit",
	})
	if isErr {
		t.Fatalf("expected success, got error: %s", text)
	}

	want := filepath.Join(conduitDir, "skills", "reusable-skill", "SKILL.md")
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("SKILL.md not written to global-conduit path: %v", err)
	}
	if !strings.Contains(string(data), "Reusable") {
		t.Errorf("unexpected content: %s", data)
	}

	// Verify usage record has CreatedBy=="agent".
	records := skillusage.All()
	var found bool
	for _, r := range records {
		if r.Name == "reusable-skill" {
			found = true
			if r.CreatedBy != "agent" {
				t.Errorf("expected CreatedBy=agent, got %q", r.CreatedBy)
			}
		}
	}
	if !found {
		t.Error("usage record not created for reusable-skill")
	}
}

func TestCreate_AlreadyExists(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(t.TempDir(), ".conduit"))

	tk := New(cwd)
	input := map[string]any{
		"action":  "create",
		"name":    "dup-skill",
		"content": "# Dup\nContent.",
		"scope":   "project",
	}

	// First create should succeed.
	text, isErr := mustExec(t, tk, input)
	if isErr {
		t.Fatalf("first create failed: %s", text)
	}

	// Second create should return an error result, not a Go error.
	text, isErr = mustExec(t, tk, input)
	if !isErr {
		t.Fatalf("expected error result on duplicate create, got: %s", text)
	}
	if !strings.Contains(text, "already exists") {
		t.Errorf("expected 'already exists' in error, got: %s", text)
	}
}

func TestUpdate_NotFound(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(t.TempDir(), ".conduit"))

	tk := New(cwd)
	text, isErr := mustExec(t, tk, map[string]any{
		"action":  "update",
		"name":    "nonexistent",
		"content": "# New Content",
		"scope":   "project",
	})
	if !isErr {
		t.Fatalf("expected error result for missing skill, got: %s", text)
	}
	if !strings.Contains(text, "not found") {
		t.Errorf("expected 'not found' in error, got: %s", text)
	}
}

func TestPromote(t *testing.T) {
	cwd := t.TempDir()
	conduitDir := filepath.Join(t.TempDir(), ".conduit")
	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)

	tk := New(cwd)

	// Create a project-scoped skill first.
	_, isErr := mustExec(t, tk, map[string]any{
		"action":  "create",
		"name":    "promote-me",
		"content": "# Promote Me\nGoes global.",
		"scope":   "project",
	})
	if isErr {
		t.Fatal("create failed before promote test")
	}

	projectPath := filepath.Join(cwd, ".claude", "skills", "promote-me")
	conduitPath := filepath.Join(conduitDir, "skills", "promote-me")

	// Promote it.
	text, isErr := mustExec(t, tk, map[string]any{
		"action": "promote",
		"name":   "promote-me",
	})
	if isErr {
		t.Fatalf("promote failed: %s", text)
	}

	// File must be at global-conduit path.
	if _, err := os.Stat(filepath.Join(conduitPath, skillFileName)); err != nil {
		t.Errorf("SKILL.md not found at global-conduit path: %v", err)
	}

	// File must be gone from project path.
	if _, err := os.Stat(projectPath); err == nil {
		t.Error("project skill directory still exists after promote")
	}
}

func TestPromote_AlreadyAtDest(t *testing.T) {
	cwd := t.TempDir()
	conduitDir := filepath.Join(t.TempDir(), ".conduit")
	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)

	tk := New(cwd)

	// Create skill in project scope.
	_, isErr := mustExec(t, tk, map[string]any{
		"action":  "create",
		"name":    "conflict-skill",
		"content": "# Conflict\nIn project.",
		"scope":   "project",
	})
	if isErr {
		t.Fatal("create project skill failed")
	}

	// Also create skill in global-conduit scope to simulate conflict.
	_, isErr = mustExec(t, tk, map[string]any{
		"action":  "create",
		"name":    "conflict-skill",
		"content": "# Conflict\nAlready in conduit.",
		"scope":   "global-conduit",
	})
	if isErr {
		t.Fatal("create global-conduit skill failed")
	}

	// Promote should fail with error result.
	text, isErr := mustExec(t, tk, map[string]any{
		"action": "promote",
		"name":   "conflict-skill",
	})
	if !isErr {
		t.Fatalf("expected error result on conflicting promote, got: %s", text)
	}
	if !strings.Contains(text, "already exists") {
		t.Errorf("expected 'already exists' in error, got: %s", text)
	}
}

func TestView_BumpsView(t *testing.T) {
	cwd := t.TempDir()
	conduitDir := filepath.Join(t.TempDir(), ".conduit")
	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)

	tk := New(cwd)

	// Create a skill to view.
	_, isErr := mustExec(t, tk, map[string]any{
		"action":  "create",
		"name":    "viewable",
		"content": "# Viewable\nSome content.",
		"scope":   "project",
	})
	if isErr {
		t.Fatal("create failed before view test")
	}

	// View the skill.
	text, isErr := mustExec(t, tk, map[string]any{
		"action": "view",
		"name":   "viewable",
	})
	if isErr {
		t.Fatalf("view failed: %s", text)
	}
	if !strings.Contains(text, "Viewable") {
		t.Errorf("unexpected view content: %s", text)
	}

	// Verify usage record has ViewCount >= 1.
	records := skillusage.All()
	var found bool
	for _, r := range records {
		if r.Name == "viewable" {
			found = true
			if r.ViewCount < 1 {
				t.Errorf("expected ViewCount >= 1, got %d", r.ViewCount)
			}
		}
	}
	if !found {
		t.Error("usage record not found for viewable skill")
	}
}
