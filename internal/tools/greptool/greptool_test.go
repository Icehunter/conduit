package greptool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func input(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// makeTree creates a small directory tree with known content for testing.
func makeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(root, "alpha.go"), []byte("package main\n\nfunc Hello() string {\n\treturn \"hello world\"\n}\n"), 0644))
	must(os.WriteFile(filepath.Join(root, "beta.go"), []byte("package main\n\nfunc Goodbye() string {\n\treturn \"goodbye world\"\n}\n"), 0644))
	must(os.MkdirAll(filepath.Join(root, "sub"), 0755))
	must(os.WriteFile(filepath.Join(root, "sub", "gamma.go"), []byte("package sub\n\n// hello comment\nconst X = 1\n"), 0644))
	must(os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello from text\ngoodbye\n"), 0644))
	return root
}

func TestGrep_FilesWithMatches_Default(t *testing.T) {
	root := makeTree(t)
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"pattern": "hello",
		"path":    root,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	got := res.Content[0].Text
	// Should list alpha.go, gamma.go, notes.txt (all contain "hello")
	if !strings.Contains(got, "alpha.go") {
		t.Errorf("alpha.go missing; got:\n%s", got)
	}
	// Should NOT contain beta.go (only has "goodbye")
	if strings.Contains(got, "beta.go") {
		t.Errorf("beta.go should not match; got:\n%s", got)
	}
}

func TestGrep_ContentMode(t *testing.T) {
	root := makeTree(t)
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"pattern":     "hello",
		"path":        root,
		"output_mode": "content",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	got := res.Content[0].Text
	// content mode should show the matching lines
	if !strings.Contains(got, "hello") {
		t.Errorf("content mode should show matching lines; got:\n%s", got)
	}
}

func TestGrep_CaseInsensitive(t *testing.T) {
	root := makeTree(t)
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"pattern": "HELLO",
		"path":    root,
		"-i":      true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	got := res.Content[0].Text
	if !strings.Contains(got, "alpha.go") {
		t.Errorf("case-insensitive match missed alpha.go; got:\n%s", got)
	}
}

func TestGrep_GlobFilter(t *testing.T) {
	root := makeTree(t)
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"pattern": "hello",
		"path":    root,
		"glob":    "*.go",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	got := res.Content[0].Text
	// notes.txt should not appear because glob restricts to *.go
	if strings.Contains(got, "notes.txt") {
		t.Errorf("notes.txt should be excluded by glob; got:\n%s", got)
	}
}

func TestGrep_NoMatchReturnsEmpty(t *testing.T) {
	root := makeTree(t)
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"pattern": "ZZZNOMATCH_UNIQUE_STRING",
		"path":    root,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	got := res.Content[0].Text
	if strings.Contains(got, ".go") || strings.Contains(got, ".txt") {
		t.Errorf("no-match result contains files; got:\n%s", got)
	}
}

func TestGrep_HeadLimit(t *testing.T) {
	root := t.TempDir()
	// Create files all containing "needle"
	for i := 0; i < 10; i++ {
		path := filepath.Join(root, strings.Repeat("f", 1)+strconv.Itoa(i)+".go")
		content := "package main\n// needle\n"
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"pattern":    "needle",
		"path":       root,
		"head_limit": 3,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	got := res.Content[0].Text
	fileCount := strings.Count(got, ".go")
	if fileCount > 3 {
		t.Errorf("head_limit=3 not respected: %d files returned; got:\n%s", fileCount, got)
	}
}

func TestGrep_CountMode(t *testing.T) {
	root := makeTree(t)
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"pattern":     "hello",
		"path":        root,
		"output_mode": "count",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	// count mode should mention occurrences
	got := res.Content[0].Text
	if !strings.Contains(got, "occurrence") && !strings.Contains(got, "match") && !strings.Contains(got, ":") {
		t.Errorf("count mode output unclear; got:\n%s", got)
	}
}

func TestGrep_VCSExcluded(t *testing.T) {
	root := makeTree(t)
	// Put a matching file inside a .git dir
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "secret.go"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"pattern": "hello",
		"path":    root,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	got := res.Content[0].Text
	if strings.Contains(got, ".git") {
		t.Errorf(".git directory should be excluded; got:\n%s", got)
	}
}

func TestGrep_EmptyPatternRejected(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"pattern": "",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("empty pattern should be IsError=true")
	}
}

func TestGrep_InvalidJSON(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), json.RawMessage(`{bad json`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("invalid JSON should be IsError=true")
	}
}

func TestGrep_StaticMetadata(t *testing.T) {
	tt := New()
	if tt.Name() != "Grep" {
		t.Errorf("Name = %q", tt.Name())
	}
	if !tt.IsReadOnly(nil) {
		t.Error("IsReadOnly should be true")
	}
	if !tt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe should be true")
	}
	var schema map[string]any
	if err := json.Unmarshal(tt.InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema not valid JSON: %v", err)
	}
}
