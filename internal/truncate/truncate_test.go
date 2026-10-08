package truncate

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// isolate points Dir() at a per-test directory so tests never write to the
// real ~/.conduit.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
}

func TestApply_NoTruncationNeeded(t *testing.T) {
	isolate(t)
	text := "line1\nline2\nline3"
	result, err := Apply(text, Options{MaxLines: 10, MaxBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if result.Truncated {
		t.Error("expected no truncation")
	}
	if result.Content != text {
		t.Errorf("content mismatch: got %q, want %q", result.Content, text)
	}
	if result.OutputPath != "" {
		t.Error("expected no output path")
	}
}

func TestApply_TruncateByLines(t *testing.T) {
	isolate(t)
	// Generate 100 lines
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, "line")
	}
	text := strings.Join(lines, "\n")

	result, err := Apply(text, Options{MaxLines: 10, MaxBytes: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated {
		t.Error("expected truncation")
	}
	if !strings.Contains(result.Content, "90 lines truncated") {
		t.Errorf("expected truncation message, got: %s", result.Content)
	}
	if result.OutputPath == "" {
		t.Error("expected output path to be set")
	}
	// Verify file exists
	if _, err := os.Stat(result.OutputPath); err != nil {
		t.Errorf("output file should exist: %v", err)
	}
}

func TestApply_TruncateByBytes(t *testing.T) {
	isolate(t)
	// Generate content that exceeds bytes but not lines
	text := strings.Repeat("x", 1000)

	result, err := Apply(text, Options{MaxLines: 1000, MaxBytes: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated {
		t.Error("expected truncation")
	}
	if !strings.Contains(result.Content, "bytes truncated") {
		t.Errorf("expected bytes truncation message, got: %s", result.Content)
	}
}

func TestApply_TailDirection(t *testing.T) {
	isolate(t)
	lines := []string{"first", "second", "third", "fourth", "fifth"}
	text := strings.Join(lines, "\n")

	result, err := Apply(text, Options{MaxLines: 2, MaxBytes: 100000, Direction: "tail"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated {
		t.Error("expected truncation")
	}
	// Should contain last lines
	if !strings.Contains(result.Content, "fourth") || !strings.Contains(result.Content, "fifth") {
		t.Errorf("expected tail lines, got: %s", result.Content)
	}
	// Should NOT contain first lines in preview
	if strings.Contains(result.Content, "first\n") {
		t.Errorf("should not contain first line in preview, got: %s", result.Content)
	}
}

func TestApply_HintWithTask(t *testing.T) {
	isolate(t)
	text := strings.Repeat("line\n", 100)
	result, err := Apply(text, Options{MaxLines: 5, MaxBytes: 100000, HasTask: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "Task tool") {
		t.Error("expected Task tool hint")
	}
	if !strings.Contains(result.Content, "explore agent") {
		t.Error("expected explore agent mention")
	}
}

func TestApply_HintWithoutTask(t *testing.T) {
	isolate(t)
	text := strings.Repeat("line\n", 100)
	result, err := Apply(text, Options{MaxLines: 5, MaxBytes: 100000, HasTask: false})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, "Use Grep") {
		t.Error("expected Grep hint")
	}
	if !strings.Contains(result.Content, "Read with offset/limit") {
		t.Error("expected Read hint")
	}
}

func TestCleanup(t *testing.T) {
	isolate(t)
	dir := Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	files := []struct {
		name     string
		old      bool
		wantKeep bool
	}{
		{"tool_old.txt", true, false},
		{"tool_new.txt", false, true},
		{"notes.txt", true, true}, // not ours: Cleanup only sweeps tool_ files
	}
	for _, f := range files {
		path := filepath.Join(dir, f.name)
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if f.old {
			if err := os.Chtimes(path, oldTime, oldTime); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	for _, f := range files {
		_, err := os.Stat(filepath.Join(dir, f.name))
		if kept := err == nil; kept != f.wantKeep {
			t.Errorf("%s kept = %v, want %v (stat err %v)", f.name, kept, f.wantKeep, err)
		}
	}
}

func TestStats(t *testing.T) {
	isolate(t)
	// This test verifies the Stats function works without errors
	// on an empty or non-existent directory
	count, bytes, err := Stats()
	if err != nil {
		// Only fail if it's not a "not exist" error
		if !os.IsNotExist(err) {
			t.Errorf("Stats error: %v", err)
		}
	}
	// Count and bytes should be >= 0
	if count < 0 || bytes < 0 {
		t.Error("negative stats values")
	}
}

func TestUniqueFilenames(t *testing.T) {
	isolate(t)
	// Write multiple files rapidly to test counter
	var paths []string
	for i := 0; i < 5; i++ {
		result, err := Apply(strings.Repeat("x\n", 100), Options{MaxLines: 5, MaxBytes: 100})
		if err != nil {
			t.Fatal(err)
		}
		if result.OutputPath == "" {
			t.Fatal("expected output path")
		}
		paths = append(paths, result.OutputPath)
	}

	// Verify all paths are unique
	seen := make(map[string]bool)
	for _, p := range paths {
		if seen[p] {
			t.Errorf("duplicate path: %s", p)
		}
		seen[p] = true
	}
}

// TestApply_HintIsNeutral: tools truncate error output too, so the hint must
// not claim the call succeeded.
func TestApply_HintIsNeutral(t *testing.T) {
	isolate(t)
	for _, hasTask := range []bool{true, false} {
		result, err := Apply(strings.Repeat("line\n", 100), Options{MaxLines: 5, MaxBytes: 100000, HasTask: hasTask})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(result.Content, "succeeded") {
			t.Errorf("HasTask=%v hint claims success: %q", hasTask, result.Content)
		}
		if !strings.Contains(result.Content, result.OutputPath) {
			t.Errorf("HasTask=%v hint does not name the file %q", hasTask, result.OutputPath)
		}
	}
}

// TestNewFile_Private: spill files can hold secrets from tool output, so the
// directory is 0700 and each file 0600, even when an older version created
// the directory 0755.
func TestNewFile_Private(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on Windows")
	}
	isolate(t)
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}

	f, err := NewFile()
	if err != nil {
		t.Fatalf("NewFile: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(f.Name()) != Dir() || !strings.HasPrefix(filepath.Base(f.Name()), "tool_") {
		t.Errorf("NewFile = %q, want a tool_ file in %q", f.Name(), Dir())
	}
	for path, want := range map[string]os.FileMode{Dir(): 0o700, f.Name(): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("mode of %s = %v, want %v", path, got, want)
		}
	}
}

// TestApply_FileHoldsFullText: the saved file is the complete input, and is
// private.
func TestApply_FileHoldsFullText(t *testing.T) {
	isolate(t)
	text := strings.Repeat("abcdefghij\n", 500)
	result, err := Apply(text, Options{MaxLines: 10, MaxBytes: 100000})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(result.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != text {
		t.Errorf("saved file differs from input: %d bytes, want %d", len(got), len(text))
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(result.OutputPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
}
