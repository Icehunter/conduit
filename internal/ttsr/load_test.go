package ttsr

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTTSRFile creates a .conduit/ttsr/<name>.md file in dir with the given content.
func writeTTSRFile(t *testing.T, dir, name, content string) {
	t.Helper()
	d := filepath.Join(dir, ".conduit", "ttsr")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(d, name), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestLoad_NoDirectory(t *testing.T) {
	dir := t.TempDir()
	rules, err := Load(dir)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if rules != nil {
		t.Fatalf("expected nil rules, got %v", rules)
	}
}

func TestLoad_ValidFile(t *testing.T) {
	dir := t.TempDir()
	writeTTSRFile(t, dir, "no-rewrites.md", `---
name: no-broad-rewrites
pattern: I will rewrite all
correction: Please make targeted changes.
max_fires: 2
---
`)

	rules, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	r := rules[0]
	if r.Name != "no-broad-rewrites" {
		t.Errorf("name: got %q, want %q", r.Name, "no-broad-rewrites")
	}
	if r.Pattern == nil || r.Pattern.String() != "I will rewrite all" {
		t.Errorf("pattern: got %v", r.Pattern)
	}
	if r.Correction != "Please make targeted changes." {
		t.Errorf("correction: got %q", r.Correction)
	}
	if r.MaxFires != 2 {
		t.Errorf("max_fires: got %d, want 2", r.MaxFires)
	}
}

func TestLoad_InvalidPattern(t *testing.T) {
	dir := t.TempDir()
	// Write a file with an invalid regex — should be silently skipped.
	writeTTSRFile(t, dir, "bad-regex.md", `---
name: bad
pattern: [invalid(regex
correction: This should be skipped.
---
`)
	// Also write a valid file alongside.
	writeTTSRFile(t, dir, "good.md", `---
name: good
pattern: ok pattern
correction: Correct.
---
`)

	rules, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 valid rule, got %d", len(rules))
	}
	if rules[0].Name != "good" {
		t.Errorf("unexpected rule name %q", rules[0].Name)
	}
}

func TestLoad_BodyAsCorrection(t *testing.T) {
	dir := t.TempDir()
	writeTTSRFile(t, dir, "body-correction.md", `---
name: body-rule
pattern: stop this
---
This is the correction text
written in the body of the file.
`)

	rules, err := Load(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	want := "This is the correction text\nwritten in the body of the file."
	if rules[0].Correction != want {
		t.Errorf("correction: got %q, want %q", rules[0].Correction, want)
	}
}
