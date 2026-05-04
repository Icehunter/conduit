package memdir

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunExtract_PassesPromptToSubAgent(t *testing.T) {
	dir := t.TempDir()

	var got string
	runner := func(_ context.Context, prompt string) (string, error) {
		got = prompt
		return "ok", nil
	}

	recent := "USER: please remember I prefer Go over Rust\nASSISTANT: noted"
	if err := RunExtract(context.Background(), dir, recent, runner); err != nil {
		t.Fatalf("RunExtract: %v", err)
	}

	for _, want := range []string{
		"memory extraction subagent",
		recent,
		"four-type taxonomy",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestRunExtract_IncludesExistingManifest(t *testing.T) {
	dir := t.TempDir()
	memDir := Path(dir)
	_ = os.MkdirAll(memDir, 0o755)
	// Drop a memory file so the manifest is non-empty.
	_ = os.WriteFile(filepath.Join(memDir, "feedback_testing.md"),
		[]byte("---\nname: feedback_testing\ndescription: test guidance\ntype: feedback\n---\nbody"),
		0o644)

	var got string
	runner := func(_ context.Context, prompt string) (string, error) {
		got = prompt
		return "", nil
	}
	_ = RunExtract(context.Background(), dir, "USER: hi", runner)

	if !strings.Contains(got, "Existing memory files") {
		excerpt := got
		if len(excerpt) > 2000 {
			excerpt = excerpt[:2000]
		}
		t.Errorf("manifest section missing from prompt; got:\n%s", excerpt)
	}
}

func TestRunExtract_ReturnsRunnerError(t *testing.T) {
	dir := t.TempDir()
	runner := func(_ context.Context, _ string) (string, error) {
		return "", context.DeadlineExceeded
	}
	err := RunExtract(context.Background(), dir, "USER: hi", runner)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded; got %v", err)
	}
}
