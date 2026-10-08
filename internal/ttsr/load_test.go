package ttsr

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLoad_ExtendedFrontmatter(t *testing.T) {
	dir := t.TempDir()
	writeTTSRFile(t, dir, "full.md", `---
name: full
pattern: TODO
mode: warn
scope: text, tool
tools: Write, Edit
paths: **/*.go, go.mod
agents: main, general-*
repeat: once
repeat_gap: 3
max_fires: 2
---
Mind the TODOs.
`)
	writeTTSRFile(t, dir, "pathonly.md", `---
name: pathonly
paths: vendor/**
correction: do not touch vendor
---
`)
	writeTTSRFile(t, dir, "badmode.md", `---
name: badmode
pattern: x
mode: explode
---
`)
	writeTTSRFile(t, dir, "badscope.md", `---
name: badscope
pattern: x
scope: nowhere
---
`)
	writeTTSRFile(t, dir, "nopattern.md", `---
name: nopattern
---
`)
	rules, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Rule{}
	for _, r := range rules {
		byName[r.Name] = r
	}
	if len(byName) != 2 {
		t.Fatalf("loaded %d rules (%v), want full and pathonly", len(byName), byName)
	}
	f := byName["full"]
	if f.Mode != ModeWarn || f.Scope != ScopeText|ScopeToolArgs || !f.Once || f.Gap != 3 || f.MaxFires != 2 {
		t.Errorf("full = %+v", f)
	}
	if got := strings.Join(f.Tools, "|"); got != "Write|Edit" {
		t.Errorf("tools = %q", got)
	}
	if got := strings.Join(f.Paths, "|"); got != "**/*.go|go.mod" {
		t.Errorf("paths = %q", got)
	}
	if got := strings.Join(f.Agents, "|"); got != "main|general-*" {
		t.Errorf("agents = %q", got)
	}
	if f.Correction != "Mind the TODOs." {
		t.Errorf("correction = %q", f.Correction)
	}
	p := byName["pathonly"]
	if p.Pattern != nil || len(p.Paths) != 1 || p.scope() != ScopeToolArgs {
		t.Errorf("pathonly = %+v", p)
	}
}
