package ccr

import (
	"os"
	"path/filepath"
	"runtime"
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

// TestPutPrivate: stored output can hold secrets, so the store directory is
// 0700 and each file 0600, even when an older version made the directory
// 0755.
func TestPutPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not apply on Windows")
	}
	s := testStore(t)
	if err := os.Chmod(s.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	handle, err := s.Put("secret output")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	key, _ := parseHandle(handle)
	for path, want := range map[string]os.FileMode{s.dir: 0o700, s.filePath(key): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("mode of %s = %v, want %v", path, got, want)
		}
	}
}

// TestPutReplacesUntrustedFile: a file already at the content key is only
// reused when it holds exactly that content. A planted or truncated file is
// replaced, so Get never returns content that does not match its key.
func TestPutReplacesUntrustedFile(t *testing.T) {
	content := "the real output"
	tests := []struct {
		name  string
		plant string
	}{
		{"different content", "attacker content"},
		{"truncated content", "the real"},
		{"same length different bytes", "THE REAL OUTPUT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testStore(t)
			path := s.filePath(contentKey(content))
			if err := os.WriteFile(path, []byte(tt.plant), 0o600); err != nil {
				t.Fatal(err)
			}
			handle, err := s.Put(content)
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			got, err := s.Get(handle)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got != content {
				t.Errorf("Get = %q, want %q", got, content)
			}
			entries, err := os.ReadDir(s.dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Errorf("store holds %d entries, want 1 (temp file left behind?)", len(entries))
			}
		})
	}
}

// TestPutDoesNotFollowSymlink: a symlink at the content key is replaced,
// never written through, so Put cannot be aimed at a file outside the store.
func TestPutDoesNotFollowSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks needs privileges on Windows")
	}
	s := testStore(t)
	target := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	content := "tool output"
	path := s.filePath(contentKey(content))
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Put(content); err != nil {
		t.Fatalf("Put: %v", err)
	}
	victim, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(victim) != "untouched" {
		t.Errorf("symlink target was written: %q", victim)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("key path mode = %v, want a regular file", info.Mode())
	}
}
