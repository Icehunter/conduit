package ccr

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// testStore creates a Store backed by a temp dir and registers cleanup.
func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return &Store{dir: dir}
}

func TestPutGet(t *testing.T) {
	s := testStore(t)
	content := "hello, CCR world"

	handle, err := s.Put(content)
	if err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}
	if handle == "" {
		t.Fatal("Put: returned empty handle")
	}

	got, err := s.Get(handle)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if got != content {
		t.Fatalf("Get: content mismatch\n  want: %q\n  got:  %q", content, got)
	}
}

func TestPutIdempotent(t *testing.T) {
	s := testStore(t)
	content := "idempotent content"

	h1, err := s.Put(content)
	if err != nil {
		t.Fatalf("Put (1st): %v", err)
	}
	h2, err := s.Put(content)
	if err != nil {
		t.Fatalf("Put (2nd): %v", err)
	}
	if h1 != h2 {
		t.Fatalf("handles differ: %q vs %q", h1, h2)
	}

	// Exactly one file on disk.
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 file, got %d", len(entries))
	}
}

func TestSlice(t *testing.T) {
	s := testStore(t)
	lines := "line0\nline1\nline2\nline3\nline4"

	handle, err := s.Put(lines)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	cases := []struct {
		name   string
		offset int
		limit  int
		want   string
	}{
		{"first-two", 0, 2, "line0\nline1"},
		{"from-line1", 1, 0, "line1\nline2\nline3\nline4"},
		{"middle-two", 2, 2, "line2\nline3"},
		{"beyond-end", 10, 5, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Slice(handle, tc.offset, tc.limit)
			if err != nil {
				t.Fatalf("Slice(%d,%d): %v", tc.offset, tc.limit, err)
			}
			if got != tc.want {
				t.Fatalf("Slice(%d,%d):\n  want: %q\n  got:  %q", tc.offset, tc.limit, tc.want, got)
			}
		})
	}
}

func TestGetInvalidHandle(t *testing.T) {
	s := testStore(t)

	cases := []struct {
		name   string
		handle string
	}{
		{"no-prefix", "nothandle"},
		{"too-long-key", "ccr:toolongkey1234567890"},
		{"too-short-key", "ccr:abc"},
		{"bad-hex", "ccr:gggggggggggggggg"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Get(tc.handle)
			if err == nil {
				t.Fatalf("Get(%q): expected error, got nil", tc.handle)
			}
		})
	}
}

func TestCleanup(t *testing.T) {
	s := testStore(t)

	// Write a "fresh" file.
	freshHandle, err := s.Put("fresh content")
	if err != nil {
		t.Fatalf("Put fresh: %v", err)
	}
	key, _ := parseHandle(freshHandle)
	freshPath := s.filePath(key)

	// Write a "stale" file and backdate its mtime.
	staleHandle, err := s.Put("stale content")
	if err != nil {
		t.Fatalf("Put stale: %v", err)
	}
	staleKey, _ := parseHandle(staleHandle)
	stalePath := s.filePath(staleKey)
	old := time.Now().Add(-(Retention + 24*time.Hour))
	if err := os.Chtimes(stalePath, old, old); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if err := s.Cleanup(); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// Stale file should be gone.
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatal("stale file was not removed by Cleanup")
	}
	// Fresh file should survive.
	if _, err := os.Stat(freshPath); err != nil {
		t.Fatalf("fresh file was unexpectedly removed: %v", err)
	}
}

func TestStats(t *testing.T) {
	s := testStore(t)

	if _, err := s.Put("alpha content"); err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	if _, err := s.Put("beta content"); err != nil {
		t.Fatalf("Put 2: %v", err)
	}

	count, total, err := s.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if count != 2 {
		t.Fatalf("Stats count: want 2, got %d", count)
	}
	if total <= 0 {
		t.Fatalf("Stats totalBytes: want >0, got %d", total)
	}
}

func TestCleanupEmptyDir(t *testing.T) {
	// Cleanup on a non-existent directory must not error.
	s := &Store{dir: filepath.Join(t.TempDir(), "nonexistent")}
	if err := s.Cleanup(); err != nil {
		t.Fatalf("Cleanup on missing dir: %v", err)
	}
}
