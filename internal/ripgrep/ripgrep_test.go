package ripgrep

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFind_ReturnsStringOrEmpty(t *testing.T) {
	// Find() must not panic; return value is either a valid path or empty string.
	p := Find()
	if p != "" {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("Find() returned %q but file does not exist: %v", p, err)
		}
	}
}

func TestAvailable_ConsistentWithFind(t *testing.T) {
	if Available() != (Find() != "") {
		t.Error("Available() inconsistent with Find()")
	}
}

func TestSearch_BasicMatch(t *testing.T) {
	if !Available() {
		t.Skip("rg not found on PATH; skipping integration test")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(target, []byte("hello world\ngoodbye world\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	results, err := Search("hello", dir, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected at least one match for 'hello'")
	}
	found := false
	for _, r := range results {
		if r.File == target && r.Line == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected match at %s:1, got: %+v", target, results)
	}
}

func TestSearch_NoMatch(t *testing.T) {
	if !Available() {
		t.Skip("rg not found on PATH; skipping integration test")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	results, err := Search("zzznomatch", dir, 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected no matches, got %d", len(results))
	}
}
