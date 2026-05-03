package migrations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeSettings(t *testing.T, dir string, v map[string]any) {
	t.Helper()
	data, _ := json.Marshal(v)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readSettings(t *testing.T, dir string) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestModelNormalize_MigratesOldSonnet(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, map[string]any{
		"model": "claude-3-5-sonnet-20241022",
	})
	Run(dir)
	raw := readSettings(t, dir)
	var got string
	_ = json.Unmarshal(raw["model"], &got)
	if got != "claude-sonnet-4-6" {
		t.Errorf("model = %q, want claude-sonnet-4-6", got)
	}
}

func TestModelNormalize_LeavesCurrentModelAlone(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, map[string]any{
		"model": "claude-opus-4-7",
	})
	Run(dir)
	raw := readSettings(t, dir)
	var got string
	_ = json.Unmarshal(raw["model"], &got)
	if got != "claude-opus-4-7" {
		t.Errorf("model = %q, want unchanged claude-opus-4-7", got)
	}
}

func TestModelNormalize_NoModelFieldIsNoOp(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, map[string]any{
		"theme": "dark",
	})
	Run(dir)
	// Should not panic or error, and settings should be unchanged.
	raw := readSettings(t, dir)
	if _, has := raw["model"]; has {
		t.Errorf("should not have added model field")
	}
}

func TestRun_IdempotentAcrossMultipleRuns(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, map[string]any{
		"model": "claude-3-5-sonnet-20241022",
	})
	Run(dir)
	Run(dir) // second run should be a no-op
	raw := readSettings(t, dir)
	var got string
	_ = json.Unmarshal(raw["model"], &got)
	if got != "claude-sonnet-4-6" {
		t.Errorf("model = %q after second run", got)
	}
	// completedMigrations should exist.
	if _, ok := raw["completedMigrations"]; !ok {
		t.Error("completedMigrations missing from settings")
	}
}

func TestRun_MissingFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	// No settings.json — should not panic.
	Run(dir)
}
