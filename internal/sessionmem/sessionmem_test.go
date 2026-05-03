package sessionmem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureFile_SeedsTemplate(t *testing.T) {
	dir := t.TempDir()
	path, err := EnsureFile(dir, "sess-1")
	if err != nil {
		t.Fatalf("EnsureFile: %v", err)
	}

	want := filepath.Join(dir, "sess-1", "session-memory", "summary.md")
	if path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# Session Memory") {
		t.Errorf("template not seeded; got: %q", data)
	}
}

func TestEnsureFile_DoesNotOverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	path, _ := EnsureFile(dir, "sess-1")
	_ = os.WriteFile(path, []byte("user content"), 0o600)

	// Second call must not clobber.
	_, _ = EnsureFile(dir, "sess-1")

	data, _ := os.ReadFile(path)
	if string(data) != "user content" {
		t.Errorf("EnsureFile clobbered existing content; got %q", data)
	}
}

func TestLoad_MissingReturnsEmpty(t *testing.T) {
	got, err := Load("/nonexistent/sessionmem/summary.md")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty for missing file; got %q", got)
	}
}

func TestRunUpdate_PassesPromptToSubAgent(t *testing.T) {
	dir := t.TempDir()
	path, _ := EnsureFile(dir, "sess-1")

	var captured string
	runner := func(_ context.Context, prompt string) (string, error) {
		captured = prompt
		return "ok", nil
	}

	transcript := "USER: do thing\nASSISTANT: did it"
	if err := RunUpdate(context.Background(), path, transcript, runner); err != nil {
		t.Fatalf("RunUpdate: %v", err)
	}

	for _, want := range []string{
		"session-memory subagent",
		path,
		transcript,
		"# Session Memory", // current summary (template) inlined
	} {
		if !strings.Contains(captured, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}
