package globtool

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

// makeTree creates a small directory tree for testing.
// Returns the root directory path.
//
//	root/
//	  a.go
//	  b.go
//	  sub/
//	    c.go
//	    d.txt
func makeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(root, "a.go"), []byte("a"), 0644))
	must(os.WriteFile(filepath.Join(root, "b.go"), []byte("b"), 0644))
	must(os.MkdirAll(filepath.Join(root, "sub"), 0755))
	must(os.WriteFile(filepath.Join(root, "sub", "c.go"), []byte("c"), 0644))
	must(os.WriteFile(filepath.Join(root, "sub", "d.txt"), []byte("d"), 0644))
	return root
}

func TestGlob_FindsGoFiles(t *testing.T) {
	root := makeTree(t)
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"pattern": "**/*.go",
		"path":    root,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	got := res.Content[0].Text
	if !strings.Contains(got, "a.go") {
		t.Errorf("a.go missing; got:\n%s", got)
	}
	if !strings.Contains(got, "b.go") {
		t.Errorf("b.go missing; got:\n%s", got)
	}
	if !strings.Contains(got, "c.go") {
		t.Errorf("c.go missing; got:\n%s", got)
	}
}

func TestGlob_PatternFiltersExtension(t *testing.T) {
	root := makeTree(t)
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"pattern": "**/*.txt",
		"path":    root,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	got := res.Content[0].Text
	if strings.Contains(got, ".go") {
		t.Errorf(".go files should not appear in *.txt glob; got:\n%s", got)
	}
	if !strings.Contains(got, "d.txt") {
		t.Errorf("d.txt missing; got:\n%s", got)
	}
}

func TestGlob_NoMatchReturnsEmpty(t *testing.T) {
	root := makeTree(t)
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"pattern": "**/*.rs",
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
		t.Errorf("unexpected files in no-match result; got:\n%s", got)
	}
}

func TestGlob_CapsAtMaxResults(t *testing.T) {
	root := t.TempDir()
	// Create MaxResults + 10 files
	for i := 0; i < MaxResults+10; i++ {
		path := filepath.Join(root, strings.Repeat("x", 1)+strings.Repeat("0", 4-len(strconv.Itoa(i)))+strconv.Itoa(i)+".go")
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"pattern": "*.go",
		"path":    root,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	// Count lines — each file on its own line
	got := res.Content[0].Text
	lines := strings.Split(strings.TrimSpace(got), "\n")
	goLines := 0
	for _, l := range lines {
		if strings.HasSuffix(l, ".go") {
			goLines++
		}
	}
	if goLines > MaxResults {
		t.Errorf("result not capped: %d files returned (max %d)", goLines, MaxResults)
	}
}

func TestGlob_InvalidDirectoryReturnsError(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"pattern": "*.go",
		"path":    "/does/not/exist/at/all",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("invalid directory should return IsError=true")
	}
}

func TestGlob_EmptyPatternRejected(t *testing.T) {
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

func TestGlob_InvalidJSON(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), json.RawMessage(`{bad json`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("invalid JSON should be IsError=true")
	}
}

func TestGlob_StaticMetadata(t *testing.T) {
	tt := New()
	if tt.Name() != "Glob" {
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
