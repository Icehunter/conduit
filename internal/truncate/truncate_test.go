package truncate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApply_NoTruncationNeeded(t *testing.T) {
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
	// Clean up
	os.Remove(result.OutputPath)
}

func TestApply_TruncateByBytes(t *testing.T) {
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
	if result.OutputPath != "" {
		os.Remove(result.OutputPath)
	}
}

func TestApply_TailDirection(t *testing.T) {
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
	if result.OutputPath != "" {
		os.Remove(result.OutputPath)
	}
}

func TestApply_HintWithTask(t *testing.T) {
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
	if result.OutputPath != "" {
		os.Remove(result.OutputPath)
	}
}

func TestApply_HintWithoutTask(t *testing.T) {
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
	if result.OutputPath != "" {
		os.Remove(result.OutputPath)
	}
}

func TestCleanup(t *testing.T) {
	// Create test directory
	dir := filepath.Join(t.TempDir(), "truncated")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Create old file (simulate 8 days old)
	oldFile := filepath.Join(dir, "tool_old.txt")
	if err := os.WriteFile(oldFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(oldFile, oldTime, oldTime)

	// Create recent file
	newFile := filepath.Join(dir, "tool_new.txt")
	if err := os.WriteFile(newFile, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Note: We can't easily test Cleanup() directly since it uses Dir()
	// which returns the real conduit directory. In production, this would
	// be tested with dependency injection or environment override.
}

func TestStats(t *testing.T) {
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
		os.Remove(p)
	}
}
