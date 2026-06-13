package astgreptool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/pendingedits"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func marshalInput(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeBin returns the path of the injected lookAstGrepFunc that reports an error.
func withMissingBinary(t *testing.T) func() {
	t.Helper()
	orig := lookAstGrepFunc
	lookAstGrepFunc = func() (string, error) {
		return "", fmt.Errorf("not found")
	}
	return func() { lookAstGrepFunc = orig }
}

// fakeStager records staged entries and can be configured to return errors.
type fakeStager struct {
	staged    []pendingedits.Entry
	returnErr error
}

func (s *fakeStager) Stage(e pendingedits.Entry) error {
	if s.returnErr != nil {
		return s.returnErr
	}
	s.staged = append(s.staged, e)
	return nil
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestAstGrep_MissingBinary(t *testing.T) {
	defer withMissingBinary(t)()
	tool := New()
	res, err := tool.Execute(context.Background(), marshalInput(t, map[string]any{
		"pattern": "fmt.Println($A)",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true when binary not found")
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "ast-grep not found") {
		t.Errorf("error message missing 'ast-grep not found'; got: %s", text)
	}
	if !strings.Contains(text, "not auto-downloaded") {
		t.Errorf("error message missing 'not auto-downloaded'; got: %s", text)
	}
}

func TestAstGrep_EmptyPattern(t *testing.T) {
	tool := New()
	res, err := tool.Execute(context.Background(), marshalInput(t, map[string]any{
		"pattern": "   ",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for empty pattern")
	}
}

func TestAstGrep_InvalidJSON(t *testing.T) {
	tool := New()
	res, err := tool.Execute(context.Background(), json.RawMessage(`{bad json`))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for invalid JSON")
	}
}

func TestAstGrep_SearchParsesJSONStream(t *testing.T) {
	// Canned output: two matches in streaming JSON format.
	cannedOutput := `{"file":"main.go","range":{"start":{"line":4,"column":1},"end":{"line":4,"column":20}},"text":"fmt.Println(\"hello\")","charCount":{"leading":0,"trailing":0}}
{"file":"util.go","range":{"start":{"line":10,"column":0},"end":{"line":10,"column":25}},"text":"fmt.Println(\"world\")","charCount":{"leading":0,"trailing":0}}
`

	origLook := lookAstGrepFunc
	origRun := runCmdFunc
	t.Cleanup(func() {
		lookAstGrepFunc = origLook
		runCmdFunc = origRun
	})

	lookAstGrepFunc = func() (string, error) { return "/fake/ast-grep", nil }
	runCmdFunc = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(cannedOutput), nil
	}

	tl := New()
	res, err := tl.Execute(context.Background(), marshalInput(t, map[string]any{
		"pattern": "fmt.Println($A)",
		"lang":    "go",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content[0].Text)
	}

	text := res.Content[0].Text
	tests := []struct {
		want string
	}{
		{"main.go:5:"},
		{"util.go:11:"},
		{`fmt.Println("hello")`},
		{`fmt.Println("world")`},
	}
	for _, tc := range tests {
		if !strings.Contains(text, tc.want) {
			t.Errorf("output missing %q; got:\n%s", tc.want, text)
		}
	}
}

func TestAstGrep_SearchNoMatches(t *testing.T) {
	origLook := lookAstGrepFunc
	origRun := runCmdFunc
	t.Cleanup(func() {
		lookAstGrepFunc = origLook
		runCmdFunc = origRun
	})

	lookAstGrepFunc = func() (string, error) { return "/fake/ast-grep", nil }
	// ast-grep returns exit code 1 with empty output for no matches.
	// We need a real *exec.ExitError — use exec to run a failing command.
	runCmdFunc = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		// Simulate no-match: return empty + nil (exit 0 with no output is also valid).
		return []byte(""), nil
	}

	tl := New()
	res, err := tl.Execute(context.Background(), marshalInput(t, map[string]any{
		"pattern": "nonexistent_pattern_xyz",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content[0].Text)
	}
	if !strings.Contains(res.Content[0].Text, "No matches found") {
		t.Errorf("expected 'No matches found' message; got: %s", res.Content[0].Text)
	}
}

func TestAstGrep_RewriteDryRunShowsChanges(t *testing.T) {
	// Canned search result showing two matches.
	cannedSearch := `{"file":"a.go","range":{"start":{"line":2,"column":0},"end":{"line":2,"column":10}},"text":"old_func()","charCount":{"leading":0,"trailing":0}}
{"file":"b.go","range":{"start":{"line":7,"column":4},"end":{"line":7,"column":14}},"text":"old_func()","charCount":{"leading":0,"trailing":0}}
`
	origLook := lookAstGrepFunc
	origRun := runCmdFunc
	t.Cleanup(func() {
		lookAstGrepFunc = origLook
		runCmdFunc = origRun
	})

	lookAstGrepFunc = func() (string, error) { return "/fake/ast-grep", nil }
	runCmdFunc = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return []byte(cannedSearch), nil
	}

	stager := &fakeStager{}
	tl := NewWithStager(stager)
	dryRun := true
	res, err := tl.Execute(context.Background(), marshalInput(t, map[string]any{
		"pattern": "old_func()",
		"rewrite": "new_func()",
		"dry_run": dryRun,
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content[0].Text)
	}

	text := res.Content[0].Text
	if !strings.Contains(text, "Would rewrite 2 files") {
		t.Errorf("expected 'Would rewrite 2 files'; got:\n%s", text)
	}
	if !strings.Contains(text, "a.go") || !strings.Contains(text, "b.go") {
		t.Errorf("expected both file names in output; got:\n%s", text)
	}
	// dry_run must not stage anything.
	if len(stager.staged) > 0 {
		t.Errorf("dry_run should not stage; got %d staged entries", len(stager.staged))
	}
}

func TestAstGrep_RewriteApplyStages(t *testing.T) {
	// Set up a temp dir with a real file to read before/after.
	dir := t.TempDir()
	file := filepath.Join(dir, "foo.go")
	origContent := []byte("package main\nfunc old_func() {}\n")
	if err := os.WriteFile(file, origContent, 0644); err != nil {
		t.Fatal(err)
	}

	newContent := []byte("package main\nfunc new_func() {}\n")

	cannedSearch := fmt.Sprintf(
		"{\"file\":%q,\"range\":{\"start\":{\"line\":1,\"column\":5},\"end\":{\"line\":1,\"column\":13}},\"text\":\"old_func\",\"charCount\":{\"leading\":0,\"trailing\":0}}\n",
		file,
	)

	origLook := lookAstGrepFunc
	origRun := runCmdFunc
	t.Cleanup(func() {
		lookAstGrepFunc = origLook
		runCmdFunc = origRun
	})

	callCount := 0
	lookAstGrepFunc = func() (string, error) { return "/fake/ast-grep", nil }
	runCmdFunc = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		callCount++
		if callCount == 1 {
			// Search pass.
			return []byte(cannedSearch), nil
		}
		// Apply pass: write the new content to simulate ast-grep --update-all.
		if err := os.WriteFile(file, newContent, 0644); err != nil {
			return nil, err
		}
		return []byte(""), nil
	}

	stager := &fakeStager{}
	tl := NewWithStager(stager)
	dryRun := false
	res, err := tl.Execute(context.Background(), marshalInput(t, map[string]any{
		"pattern": "old_func",
		"rewrite": "new_func",
		"dry_run": dryRun,
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content[0].Text)
	}

	// One entry should be staged.
	if len(stager.staged) != 1 {
		t.Fatalf("expected 1 staged entry; got %d", len(stager.staged))
	}
	e := stager.staged[0]
	if e.Path != file {
		t.Errorf("staged path = %q; want %q", e.Path, file)
	}
	if string(e.OrigContent) != string(origContent) {
		t.Errorf("staged orig content mismatch")
	}
	if string(e.NewContent) != string(newContent) {
		t.Errorf("staged new content mismatch")
	}
	if e.ToolName != "AstGrep" {
		t.Errorf("staged ToolName = %q; want AstGrep", e.ToolName)
	}
}

func TestAstGrep_ErrNotStagingFallthrough(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "bar.go")
	origContent := []byte("package main\nvar x = 1\n")
	if err := os.WriteFile(file, origContent, 0644); err != nil {
		t.Fatal(err)
	}
	newContent := []byte("package main\nvar x = 2\n")

	cannedSearch := fmt.Sprintf(
		"{\"file\":%q,\"range\":{\"start\":{\"line\":1,\"column\":8},\"end\":{\"line\":1,\"column\":9}},\"text\":\"1\",\"charCount\":{\"leading\":0,\"trailing\":0}}\n",
		file,
	)

	origLook := lookAstGrepFunc
	origRun := runCmdFunc
	t.Cleanup(func() {
		lookAstGrepFunc = origLook
		runCmdFunc = origRun
	})

	callCount := 0
	lookAstGrepFunc = func() (string, error) { return "/fake/ast-grep", nil }
	runCmdFunc = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		callCount++
		if callCount == 1 {
			return []byte(cannedSearch), nil
		}
		// Simulate --update-all: write file and return.
		if err := os.WriteFile(file, newContent, 0644); err != nil {
			return nil, err
		}
		return []byte(""), nil
	}

	// Stager returns ErrNotStaging.
	stager := &fakeStager{returnErr: pendingedits.ErrNotStaging}
	tl := NewWithStager(stager)
	dryRun := false
	res, err := tl.Execute(context.Background(), marshalInput(t, map[string]any{
		"pattern": "1",
		"rewrite": "2",
		"dry_run": dryRun,
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content[0].Text)
	}

	// File should have been written directly by ast-grep (our runCmdFunc wrote it).
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newContent) {
		t.Errorf("file content = %q; want %q", string(got), string(newContent))
	}

	// Output should mention "Applied directly".
	if !strings.Contains(res.Content[0].Text, "Applied directly") {
		t.Errorf("expected 'Applied directly' in output; got: %s", res.Content[0].Text)
	}
}

func TestAstGrep_IsReadOnly(t *testing.T) {
	tl := New()
	tests := []struct {
		name     string
		input    map[string]any
		wantRead bool
	}{
		{"no rewrite → read-only", map[string]any{"pattern": "foo"}, true},
		{"empty rewrite → read-only", map[string]any{"pattern": "foo", "rewrite": ""}, true},
		{"with rewrite → not read-only", map[string]any{"pattern": "foo", "rewrite": "bar"}, false},
		{"invalid json → not read-only", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var raw json.RawMessage
			if tc.input != nil {
				raw = marshalInput(t, tc.input)
			} else {
				raw = json.RawMessage(`{bad`)
			}
			got := tl.IsReadOnly(raw)
			if got != tc.wantRead {
				t.Errorf("IsReadOnly() = %v; want %v", got, tc.wantRead)
			}
		})
	}
}

func TestAstGrep_IsConcurrencySafe(t *testing.T) {
	tl := New()
	if tl.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe() should return false")
	}
}

func TestAstGrep_Cancelled(t *testing.T) {
	origLook := lookAstGrepFunc
	origRun := runCmdFunc
	t.Cleanup(func() {
		lookAstGrepFunc = origLook
		runCmdFunc = origRun
	})

	lookAstGrepFunc = func() (string, error) { return "/fake/ast-grep", nil }
	runCmdFunc = func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	tl := New()
	res, err := tl.Execute(ctx, marshalInput(t, map[string]any{
		"pattern": "foo",
	}))
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	// The tool checks ctx.Done() before lookup, so we get "cancelled".
	if !res.IsError {
		t.Error("expected IsError=true for cancelled context")
	}
	if !strings.Contains(res.Content[0].Text, "cancelled") {
		t.Errorf("expected 'cancelled' in error text; got: %s", res.Content[0].Text)
	}
}

// ── parseMatchLines unit tests ────────────────────────────────────────────────

func TestParseMatchLines(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantCount int
		wantFile  string
	}{
		{
			name: "valid JSON lines",
			input: `{"file":"foo.go","range":{"start":{"line":0,"column":0},"end":{"line":0,"column":5}},"text":"hello","charCount":{"leading":0,"trailing":0}}
{"file":"bar.go","range":{"start":{"line":3,"column":2},"end":{"line":3,"column":7}},"text":"world","charCount":{"leading":0,"trailing":0}}
`,
			wantCount: 2,
			wantFile:  "foo.go",
		},
		{
			name:      "empty output",
			input:     "",
			wantCount: 0,
		},
		{
			name:      "invalid JSON lines are skipped",
			input:     "not json\nalso not json\n",
			wantCount: 0,
		},
		{
			name: "mixed valid and invalid",
			input: `{"file":"ok.go","range":{"start":{"line":1,"column":0},"end":{"line":1,"column":3}},"text":"ok","charCount":{"leading":0,"trailing":0}}
not a json line
`,
			wantCount: 1,
			wantFile:  "ok.go",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := parseMatchLines([]byte(tc.input))
			if len(got) != tc.wantCount {
				t.Errorf("parseMatchLines() = %d matches; want %d", len(got), tc.wantCount)
			}
			if tc.wantFile != "" && len(got) > 0 && got[0].File != tc.wantFile {
				t.Errorf("got[0].File = %q; want %q", got[0].File, tc.wantFile)
			}
		})
	}
}
