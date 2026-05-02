package fileedittool

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

func writeFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "edit-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFileEdit_BasicReplacement(t *testing.T) {
	path := writeFile(t, "hello world\n")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  path,
		"old_string": "hello",
		"new_string": "goodbye",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	if got := readFile(t, path); got != "goodbye world\n" {
		t.Errorf("content = %q", got)
	}
}

func TestFileEdit_MultilineReplacement(t *testing.T) {
	original := "func foo() {\n\treturn 1\n}\n"
	path := writeFile(t, original)
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  path,
		"old_string": "return 1",
		"new_string": "return 42",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "return 42") {
		t.Errorf("replacement not applied; got: %q", got)
	}
}

func TestFileEdit_OldStringNotFound(t *testing.T) {
	path := writeFile(t, "hello world\n")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  path,
		"old_string": "DOES NOT EXIST",
		"new_string": "x",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("expected IsError when old_string not found")
	}
	if !strings.Contains(res.Content[0].Text, "not found") &&
		!strings.Contains(res.Content[0].Text, "String not found") {
		t.Errorf("error message: %s", res.Content[0].Text)
	}
}

func TestFileEdit_OldEqualsNew(t *testing.T) {
	path := writeFile(t, "hello world\n")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  path,
		"old_string": "hello",
		"new_string": "hello",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("expected IsError when old_string == new_string")
	}
}

func TestFileEdit_FileNotFound(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  "/does/not/exist.txt",
		"old_string": "x",
		"new_string": "y",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("expected IsError for missing file")
	}
}

func TestFileEdit_CreateNewFileWhenOldStringEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  path,
		"old_string": "",
		"new_string": "brand new content\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	if got := readFile(t, path); got != "brand new content\n" {
		t.Errorf("content = %q", got)
	}
}

func TestFileEdit_ReplaceAll(t *testing.T) {
	path := writeFile(t, "foo bar foo baz foo\n")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":   path,
		"old_string":  "foo",
		"new_string":  "qux",
		"replace_all": true,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	got := readFile(t, path)
	if strings.Contains(got, "foo") {
		t.Errorf("replace_all=true left 'foo' in file; got: %q", got)
	}
	if !strings.Contains(got, "qux") {
		t.Errorf("replacement not applied; got: %q", got)
	}
}

func TestFileEdit_ReplaceDefaultOnlyFirst(t *testing.T) {
	path := writeFile(t, "foo foo foo\n")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  path,
		"old_string": "foo",
		"new_string": "bar",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	got := readFile(t, path)
	if got != "bar foo foo\n" {
		t.Errorf("default should replace first only; got: %q", got)
	}
}

func TestFileEdit_CurlyQuoteNormalization(t *testing.T) {
	// File contains curly quotes; model sends straight quotes — should match.
	path := writeFile(t, "She said “hello” to him.\n")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  path,
		"old_string": `She said "hello" to him.`,
		"new_string": `She said "goodbye" to him.`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError (curly quote normalization): %v", res.Content)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "goodbye") {
		t.Errorf("curly quote match failed; got: %q", got)
	}
}

func TestFileEdit_DeleteByEmptyNewString(t *testing.T) {
	path := writeFile(t, "keep this\ndelete this\nkeep this too\n")
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  path,
		"old_string": "delete this\n",
		"new_string": "",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	got := readFile(t, path)
	if strings.Contains(got, "delete this") {
		t.Errorf("deletion not applied; got: %q", got)
	}
	if !strings.Contains(got, "keep this") {
		t.Errorf("kept content missing; got: %q", got)
	}
}

func TestFileEdit_EmptyPathRejected(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  "",
		"old_string": "x",
		"new_string": "y",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("empty file_path should be IsError=true")
	}
}

func TestFileEdit_InvalidJSON(t *testing.T) {
	tt := New()
	res, err := tt.Execute(context.Background(), json.RawMessage(`{bad`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("invalid JSON should be IsError=true")
	}
}

func TestFileEdit_StaticMetadata(t *testing.T) {
	tt := New()
	if tt.Name() != "Edit" {
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
		t.Fatalf("InputSchema invalid JSON: %v", err)
	}
}
