package attach

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractAtMentions_Basic(t *testing.T) {
	tests := []struct {
		input string
		want  []string // Original tokens
	}{
		{"look at @main.go please", []string{"@main.go"}},
		{`check @"my file.txt"`, []string{`@"my file.txt"`}},
		{"@src/main.go#L10-20", []string{"@src/main.go#L10-20"}},
		{"@file.go#L5", []string{"@file.go#L5"}},
		{"a @foo @bar", []string{"@foo", "@bar"}},
		{"@foo @foo", []string{"@foo"}},             // dedup
		{`no mentions here`, nil},
		{`@"agent (agent)"`, nil},                   // agent mention excluded
		{`@"agent (agent)" @foo`, []string{"@foo"}}, // mixed
		{"@dir/", []string{"@dir/"}},
	}
	for _, tc := range tests {
		got := ExtractAtMentions(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("input %q: got %d mentions, want %d", tc.input, len(got), len(tc.want))
			continue
		}
		for i, m := range got {
			if m.Original != tc.want[i] {
				t.Errorf("input %q mention[%d]: got %q, want %q", tc.input, i, m.Original, tc.want[i])
			}
		}
	}
}

func TestParseLineRange(t *testing.T) {
	tests := []struct {
		raw       string
		wantPath  string
		wantStart int
		wantEnd   int
	}{
		{"file.go", "file.go", 0, 0},
		{"file.go#L10", "file.go", 10, 0},
		{"file.go#L10-20", "file.go", 10, 20},
		{"file.go#heading", "file.go", 0, 0}, // non-line fragment stripped
		{"src/main.go#L1-5", "src/main.go", 1, 5},
	}
	for _, tc := range tests {
		path, ls, le := parseLineRange(tc.raw)
		if path != tc.wantPath || ls != tc.wantStart || le != tc.wantEnd {
			t.Errorf("parseLineRange(%q): got (%q,%d,%d) want (%q,%d,%d)",
				tc.raw, path, ls, le, tc.wantPath, tc.wantStart, tc.wantEnd)
		}
	}
}

func TestProcessAtMentions_File(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "hello.go")
	if err := os.WriteFile(f, []byte("line1\nline2\nline3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Full file mention.
	results := ProcessAtMentions("look at @hello.go", dir)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	if !strings.Contains(results[0].Content, "line1") {
		t.Errorf("content missing line1: %q", results[0].Content)
	}
	if results[0].DisplayPath != "hello.go" {
		t.Errorf("display path = %q, want %q", results[0].DisplayPath, "hello.go")
	}
}

func TestProcessAtMentions_LineRange(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "code.go")
	lines := "a\nb\nc\nd\ne\n"
	if err := os.WriteFile(f, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}

	results := ProcessAtMentions("@code.go#L2-4", dir)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d", len(results))
	}
	// Lines 2–4 = "b", "c", "d"
	content := results[0].Content
	if !strings.Contains(content, "b") || !strings.Contains(content, "d") {
		t.Errorf("unexpected content: %q", content)
	}
	if strings.Contains(content, "a") || strings.Contains(content, "e") {
		t.Errorf("content should not include lines outside range: %q", content)
	}
}

func TestProcessAtMentions_Directory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	results := ProcessAtMentions("check @./", dir)
	if len(results) != 1 {
		t.Fatalf("want 1 result, got %d: %+v", len(results), results)
	}
	if !strings.Contains(results[0].Content, "a.go") {
		t.Errorf("dir listing missing a.go: %q", results[0].Content)
	}
}

func TestProcessAtMentions_Missing(t *testing.T) {
	dir := t.TempDir()
	results := ProcessAtMentions("@nonexistent.go", dir)
	if len(results) != 0 {
		t.Errorf("missing file should yield no results, got %d", len(results))
	}
}

func TestFormatAtResult(t *testing.T) {
	r := AtResult{DisplayPath: "src/main.go", Content: "package main"}
	s := FormatAtResult(r)
	if !strings.Contains(s, "src/main.go") {
		t.Errorf("missing path: %q", s)
	}
	if !strings.Contains(s, "package main") {
		t.Errorf("missing content: %q", s)
	}
}
