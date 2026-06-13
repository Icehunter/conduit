package hashedittool_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/hashline"
	"github.com/icehunter/conduit/internal/pendingedits"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/tools/hashedittool"
)

// fakeStager records the last staged entry, or returns ErrNotStaging if
// configured to do so.
type fakeStager struct {
	entries       []pendingedits.Entry
	returnErr     error
	pendingResult *pendingedits.Entry
}

func (f *fakeStager) Stage(e pendingedits.Entry) error {
	if f.returnErr != nil {
		return f.returnErr
	}
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeStager) Pending(path string) (pendingedits.Entry, bool) {
	if f.pendingResult != nil && f.pendingResult.Path == path {
		return *f.pendingResult, true
	}
	return pendingedits.Entry{}, false
}

func execTool(t *testing.T, tool *hashedittool.Tool, input any) tool.Result {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("Execute returned Go error: %v", err)
	}
	return result
}

func TestHashEdit_basicSingleEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "line one\nline two\nline three\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	anchors := hashline.Compute([]byte(content))
	if len(anchors) < 2 {
		t.Fatal("expected at least 2 anchors")
	}

	ht := hashedittool.New()
	result := execTool(t, ht, map[string]any{
		"file_path": path,
		"edits": []map[string]any{
			{"anchor": anchors[1].Hash, "new_lines": "line TWO"},
		},
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "line one\nline TWO\nline three\n"
	if string(got) != want {
		t.Errorf("content mismatch:\ngot:  %q\nwant: %q", string(got), want)
	}
}

func TestHashEdit_multiEdit_bottomToTop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "alpha\nbeta\ngamma\ndelta\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	anchors := hashline.Compute([]byte(content))

	ht := hashedittool.New()
	result := execTool(t, ht, map[string]any{
		"file_path": path,
		"edits": []map[string]any{
			{"anchor": anchors[0].Hash, "new_lines": "ALPHA"},
			{"anchor": anchors[2].Hash, "new_lines": "GAMMA"},
		},
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "ALPHA\nbeta\nGAMMA\ndelta\n"
	if string(got) != want {
		t.Errorf("content mismatch:\ngot:  %q\nwant: %q", string(got), want)
	}
}

func TestHashEdit_delete(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "keep this\ndelete me\nalso keep\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	anchors := hashline.Compute([]byte(content))

	ht := hashedittool.New()
	result := execTool(t, ht, map[string]any{
		"file_path": path,
		"edits": []map[string]any{
			{"anchor": anchors[1].Hash, "delete": true},
		},
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "keep this\nalso keep\n"
	if string(got) != want {
		t.Errorf("content mismatch:\ngot:  %q\nwant: %q", string(got), want)
	}
}

func TestHashEdit_staleAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	original := "foo\nbar\nbaz\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	anchors := hashline.Compute([]byte(original))
	barHash := anchors[1].Hash

	if err := os.WriteFile(path, []byte("foo\nBAR_CHANGED\nbaz\n"), 0644); err != nil {
		t.Fatal(err)
	}

	ht := hashedittool.New()
	result := execTool(t, ht, map[string]any{
		"file_path": path,
		"edits": []map[string]any{
			{"anchor": barHash, "new_lines": "bar_new"},
		},
	})
	if !result.IsError {
		t.Error("expected error for stale anchor, got success")
	}
	if len(result.Content) == 0 || result.Content[0].Text == "" {
		t.Error("expected error message in result")
	}
}

func TestHashEdit_ambiguousAnchor(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "x\na\na\na\na\na\nx\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	anchors := hashline.Compute([]byte(content))
	hashCounts := make(map[string]int)
	for _, a := range anchors {
		hashCounts[a.Hash]++
	}
	var ambiguous string
	for h, c := range hashCounts {
		if c > 1 {
			ambiguous = h
			break
		}
	}
	if ambiguous == "" {
		t.Fatal("no ambiguous hash in test input")
	}

	ht := hashedittool.New()
	result := execTool(t, ht, map[string]any{
		"file_path": path,
		"edits": []map[string]any{
			{"anchor": ambiguous, "new_lines": "replacement"},
		},
	})
	if !result.IsError {
		t.Error("expected error for ambiguous anchor, got success")
	}
}

func TestHashEdit_expectUniqueFalse_ambiguousUsesFirst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	// The inner "a" lines share the same hash (prev=a, line=a, next=a context).
	// This is the same pattern used by TestHashEdit_ambiguousAnchor.
	content := "x\na\na\na\na\na\nx\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	anchors := hashline.Compute([]byte(content))
	// Find the first anchor whose hash appears more than once.
	hashCounts := make(map[string]int)
	for _, a := range anchors {
		hashCounts[a.Hash]++
	}
	var ambiguous string
	var firstMatchLine int
	for _, a := range anchors { // iterate in order to get first occurrence
		if hashCounts[a.Hash] > 1 {
			ambiguous = a.Hash
			firstMatchLine = a.Line // 1-indexed
			break
		}
	}
	if ambiguous == "" {
		t.Fatal("no ambiguous hash in test input")
	}

	f := false
	ht := hashedittool.New()
	result := execTool(t, ht, map[string]any{
		"file_path":     path,
		"expect_unique": f,
		"edits": []map[string]any{
			{"anchor": ambiguous, "new_lines": "REPLACED"},
		},
	})
	if result.IsError {
		t.Fatalf("expected success with expect_unique=false, got error: %s", result.Content[0].Text)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// Build expected: the first matching line (firstMatchLine, 1-indexed) is replaced.
	// We rebuild from the raw content so no slice indexing is needed.
	rawLines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if firstMatchLine < 1 || firstMatchLine > len(rawLines) {
		t.Fatalf("firstMatchLine %d out of range [1,%d]", firstMatchLine, len(rawLines))
	}
	for i := range rawLines {
		if i == firstMatchLine-1 {
			rawLines[i] = "REPLACED"
			break
		}
	}
	want := strings.Join(rawLines, "\n") + "\n"
	if string(got) != want {
		t.Errorf("expect_unique=false should replace first match (line %d):\ngot:  %q\nwant: %q", firstMatchLine, string(got), want)
	}
}

func TestHashEdit_stagedWritePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "hello\nworld\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	anchors := hashline.Compute([]byte(content))

	stager := &fakeStager{}
	ht := hashedittool.NewWithStager(stager)
	result := execTool(t, ht, map[string]any{
		"file_path": path,
		"edits": []map[string]any{
			{"anchor": anchors[0].Hash, "new_lines": "HELLO"},
		},
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}
	if len(stager.entries) == 0 {
		t.Error("expected stager to have received an entry")
	}
	if stager.entries[0].ToolName != "HashEdit" {
		t.Errorf("expected ToolName=HashEdit, got %q", stager.entries[0].ToolName)
	}
}

func TestHashEdit_errNotStagingFallthrough(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "one\ntwo\nthree\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	anchors := hashline.Compute([]byte(content))

	stager := &fakeStager{returnErr: pendingedits.ErrNotStaging}
	ht := hashedittool.NewWithStager(stager)
	result := execTool(t, ht, map[string]any{
		"file_path": path,
		"edits": []map[string]any{
			{"anchor": anchors[1].Hash, "new_lines": "TWO"},
		},
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "one\nTWO\nthree\n"
	if string(got) != want {
		t.Errorf("fallthrough write: got %q, want %q", string(got), want)
	}
}

func TestHashEdit_roundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "round.txt")
	content := "package main\n\nfunc hello() string {\n\treturn \"hello\"\n}\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	anchors := hashline.Compute([]byte(content))
	var returnHash string
	for _, a := range anchors {
		lines := splitTestLines(content)
		if a.Line-1 < len(lines) && lines[a.Line-1] == "\treturn \"hello\"" {
			returnHash = a.Hash
			break
		}
	}
	if returnHash == "" {
		t.Fatal("could not find return line anchor")
	}

	ht := hashedittool.New()
	result := execTool(t, ht, map[string]any{
		"file_path": path,
		"edits": []map[string]any{
			{"anchor": returnHash, "new_lines": "\treturn \"world\""},
		},
	})
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content[0].Text)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "package main\n\nfunc hello() string {\n\treturn \"world\"\n}\n"
	if string(got) != want {
		t.Errorf("round trip:\ngot:  %q\nwant: %q", string(got), want)
	}
}

func splitTestLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func TestHashEdit_chainedStagedEdits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chained.txt")
	initial := "alpha\nbeta\ngamma\n"
	if err := os.WriteFile(path, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}

	stager := &fakeStager{}
	ht := hashedittool.NewWithStager(stager)

	// First edit: "beta" → "BETA"
	anchors1 := hashline.Compute([]byte(initial))
	var betaHash string
	lines1 := splitTestLines(initial)
	for _, a := range anchors1 {
		if a.Line-1 < len(lines1) && lines1[a.Line-1] == "beta" {
			betaHash = a.Hash
			break
		}
	}
	if betaHash == "" {
		t.Fatal("could not find 'beta' anchor in initial content")
	}

	result1 := execTool(t, ht, map[string]any{
		"file_path": path,
		"edits": []map[string]any{
			{"anchor": betaHash, "new_lines": "BETA"},
		},
	})
	if result1.IsError {
		t.Fatalf("first edit unexpected error: %s", result1.Content[0].Text)
	}
	if len(stager.entries) == 0 {
		t.Fatal("expected stager to have one entry after first edit")
	}
	firstEntry := stager.entries[0]
	wantAfterFirst := "alpha\nBETA\ngamma\n"
	if string(firstEntry.NewContent) != wantAfterFirst {
		t.Errorf("first staged NewContent:\ngot:  %q\nwant: %q", string(firstEntry.NewContent), wantAfterFirst)
	}
	if string(firstEntry.OrigContent) != initial {
		t.Errorf("first staged OrigContent:\ngot:  %q\nwant: %q", string(firstEntry.OrigContent), initial)
	}

	// Pre-populate the stager's pending result so the second call reads from staged content.
	stager.pendingResult = &pendingedits.Entry{
		Path:        path,
		OrigContent: firstEntry.OrigContent,
		OrigExisted: true,
		NewContent:  firstEntry.NewContent,
		Op:          pendingedits.OpEdit,
		ToolName:    "HashEdit",
	}

	// Second edit: "BETA" → "BETA_2" — anchors must be computed from staged content.
	stagedContent := firstEntry.NewContent
	anchors2 := hashline.Compute(stagedContent)
	var betaUpperHash string
	lines2 := splitTestLines(string(stagedContent))
	for _, a := range anchors2 {
		if a.Line-1 < len(lines2) && lines2[a.Line-1] == "BETA" {
			betaUpperHash = a.Hash
			break
		}
	}
	if betaUpperHash == "" {
		t.Fatal("could not find 'BETA' anchor in staged content")
	}

	result2 := execTool(t, ht, map[string]any{
		"file_path": path,
		"edits": []map[string]any{
			{"anchor": betaUpperHash, "new_lines": "BETA_2"},
		},
	})
	if result2.IsError {
		t.Fatalf("second edit unexpected error: %s", result2.Content[0].Text)
	}
	if len(stager.entries) < 2 {
		t.Fatal("expected stager to have two entries after second edit")
	}
	secondEntry := stager.entries[1]
	wantAfterSecond := "alpha\nBETA_2\ngamma\n"
	if string(secondEntry.NewContent) != wantAfterSecond {
		t.Errorf("second staged NewContent (should edit staged 'BETA', not disk 'beta'):\ngot:  %q\nwant: %q",
			string(secondEntry.NewContent), wantAfterSecond)
	}
}

// Ensure errors.Is works for ErrNotStaging in fakeStager
var _ error = pendingedits.ErrNotStaging
var _ = errors.Is(pendingedits.ErrNotStaging, pendingedits.ErrNotStaging)
