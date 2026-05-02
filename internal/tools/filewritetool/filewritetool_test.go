package filewritetool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestFileWrite_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path": path,
		"content":   "hello world\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	// File should now exist with correct content
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if string(got) != "hello world\n" {
		t.Errorf("content = %q", string(got))
	}
}

func TestFileWrite_OverwritesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.txt")
	if err := os.WriteFile(path, []byte("old content"), 0644); err != nil {
		t.Fatal(err)
	}
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path": path,
		"content":   "new content\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new content\n" {
		t.Errorf("content = %q", string(got))
	}
}

func TestFileWrite_CreatesParentDirectories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deep", "nested", "file.txt")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path": path,
		"content":   "deep\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created at nested path: %v", err)
	}
}

func TestFileWrite_ResultTextContainsPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.txt")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path": path,
		"content":   "x",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError; content=%v", res.Content)
	}
	// The result text should mention the path
	if !strings.Contains(res.Content[0].Text, path) &&
		!strings.Contains(res.Content[0].Text, "successfully") {
		t.Errorf("result text doesn't mention path or success; got: %s", res.Content[0].Text)
	}
}

func TestFileWrite_EmptyPathRejected(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path": "",
		"content":   "data",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("empty file_path should be IsError=true")
	}
}

func TestFileWrite_RelativePathRejected(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path": "relative/path/file.txt",
		"content":   "data",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("relative path should be IsError=true")
	}
}

func TestFileWrite_InvalidJSON(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), json.RawMessage(`{bad json`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("invalid JSON should be IsError=true")
	}
}

func TestFileWrite_StaticMetadata(t *testing.T) {
	tt := New()
	if tt.Name() != "Write" {
		t.Errorf("Name = %q", tt.Name())
	}
	if tt.IsReadOnly(nil) {
		t.Error("IsReadOnly should be false")
	}
	if tt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe should be false")
	}
	var schema map[string]any
	if err := json.Unmarshal(tt.InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema not valid JSON: %v", err)
	}
}
