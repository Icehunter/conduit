package filereadtool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helper: write a temp file, return its path.
func writeTmp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "fileread-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

func input(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFileRead_ReadsFileWithLineNumbers(t *testing.T) {
	path := writeTmp(t, "alpha\nbeta\ngamma\n")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{"file_path": path}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	got := res.Content[0].Text
	// Expect cat -n style: "     1\talpha", etc.
	if !strings.Contains(got, "1\talpha") {
		t.Errorf("missing line 1; got:\n%s", got)
	}
	if !strings.Contains(got, "2\tbeta") {
		t.Errorf("missing line 2; got:\n%s", got)
	}
	if !strings.Contains(got, "3\tgamma") {
		t.Errorf("missing line 3; got:\n%s", got)
	}
}

func TestFileRead_Offset(t *testing.T) {
	path := writeTmp(t, "line1\nline2\nline3\nline4\n")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path": path,
		"offset":    3, // start at line 3 (1-indexed)
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	got := res.Content[0].Text
	if strings.Contains(got, "line1") || strings.Contains(got, "line2") {
		t.Errorf("offset not respected; got:\n%s", got)
	}
	if !strings.Contains(got, "line3") {
		t.Errorf("line3 missing with offset=3; got:\n%s", got)
	}
}

func TestFileRead_Limit(t *testing.T) {
	path := writeTmp(t, "a\nb\nc\nd\ne\n")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path": path,
		"limit":     2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	got := res.Content[0].Text
	if strings.Contains(got, "\tc\n") || strings.Contains(got, "\td\n") || strings.Contains(got, "\te\n") {
		t.Errorf("limit not respected; got:\n%s", got)
	}
	if !strings.Contains(got, "1\ta") {
		t.Errorf("line 1 missing; got:\n%s", got)
	}
	if !strings.Contains(got, "2\tb") {
		t.Errorf("line 2 missing; got:\n%s", got)
	}
}

func TestFileRead_OffsetAndLimit(t *testing.T) {
	path := writeTmp(t, "a\nb\nc\nd\ne\n")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path": path,
		"offset":    2,
		"limit":     2,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	got := res.Content[0].Text
	// should contain lines 2 and 3 only
	if !strings.Contains(got, "2\tb") || !strings.Contains(got, "3\tc") {
		t.Errorf("expected lines 2-3; got:\n%s", got)
	}
	if strings.Contains(got, "1\ta") || strings.Contains(got, "4\td") {
		t.Errorf("out-of-range lines present; got:\n%s", got)
	}
}

func TestFileRead_FileNotFound(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path": "/does/not/exist/surely.txt",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("expected IsError=true for missing file")
	}
	if !strings.Contains(res.Content[0].Text, "not found") &&
		!strings.Contains(res.Content[0].Text, "no such file") &&
		!strings.Contains(res.Content[0].Text, "does not exist") {
		t.Errorf("error message unclear; got: %s", res.Content[0].Text)
	}
}

func TestFileRead_EmptyFile(t *testing.T) {
	path := writeTmp(t, "")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{"file_path": path}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError on empty file; content=%v", res.Content)
	}
}

func TestFileRead_BinaryFileRejected(t *testing.T) {
	// Write a file with null bytes (binary signature)
	dir := t.TempDir()
	path := filepath.Join(dir, "bin.bin")
	if err := os.WriteFile(path, []byte{0x00, 0x01, 0x02, 0xFF}, 0644); err != nil {
		t.Fatal(err)
	}
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{"file_path": path}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("binary file should return IsError=true")
	}
}

func TestFileRead_LargeFileTruncated(t *testing.T) {
	// Generate a file with more than MaxLines lines
	var sb strings.Builder
	for i := 0; i < MaxLines+100; i++ {
		fmt.Fprintf(&sb, "line %d\n", i+1)
	}
	path := writeTmp(t, sb.String())
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{"file_path": path}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError on large file; content=%v", res.Content)
	}
	// Should mention truncation or limit
	got := res.Content[0].Text
	lineCount := strings.Count(got, "\n")
	if lineCount > MaxLines+10 { // small slack for the truncation marker
		t.Errorf("large file not truncated: %d lines in output", lineCount)
	}
}

func TestFileRead_InvalidJSON(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), json.RawMessage(`{bad json`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("invalid JSON should be IsError=true")
	}
}

func TestFileRead_EmptyPathRejected(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{"file_path": ""}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("empty file_path should be IsError=true")
	}
}

func TestFileRead_StaticMetadata(t *testing.T) {
	tt := New()
	if tt.Name() != "Read" {
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
	if schema["type"] != "object" {
		t.Errorf("schema type = %v", schema["type"])
	}
}
