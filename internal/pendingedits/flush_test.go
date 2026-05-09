package pendingedits

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFlushOne_NewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "new.txt")
	err := FlushOne(Entry{
		Path:       path,
		NewContent: []byte("hello\n"),
		Op:         OpWrite,
	})
	if err != nil {
		t.Fatalf("FlushOne: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello\n" {
		t.Errorf("content = %q, want %q", got, "hello\n")
	}
}

func TestFlushOne_OverwritePreservesMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("orig"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := FlushOne(Entry{
		Path:        path,
		OrigContent: []byte("orig"),
		OrigExisted: true,
		NewContent:  []byte("new"),
		Op:          OpEdit,
	})
	if err != nil {
		t.Fatalf("FlushOne: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600 preserved", st.Mode().Perm())
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Errorf("content = %q", got)
	}
}

func TestFlush_PerEntryErrorsDoNotAbortBatch(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.txt")
	// Use a path under a regular file to force a mkdir failure on the bad entry.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(blocker, "child.txt") // mkdir of blocker/ will fail

	results := Flush([]Entry{
		{Path: good, NewContent: []byte("ok"), Op: OpWrite},
		{Path: bad, NewContent: []byte("nope"), Op: OpWrite},
	})
	if len(results) != 2 {
		t.Fatalf("results len = %d, want 2", len(results))
	}
	if !results[0].Applied || results[0].Err != nil {
		t.Errorf("good entry: applied=%v err=%v", results[0].Applied, results[0].Err)
	}
	if results[1].Applied {
		t.Error("bad entry: Applied = true, want false")
	}
	if results[1].Err == nil {
		t.Error("bad entry: Err nil, want non-nil")
	}
	got, _ := os.ReadFile(good)
	if string(got) != "ok" {
		t.Errorf("good content = %q", got)
	}
}

func TestFlushOne_EmptyPathReturnsError(t *testing.T) {
	if err := FlushOne(Entry{Path: ""}); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestFlushOne_ExistingFileConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("orig"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("user change"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := FlushOne(Entry{
		Path:        path,
		OrigContent: []byte("orig"),
		OrigExisted: true,
		NewContent:  []byte("agent change"),
		Op:          OpEdit,
	})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if got := string(mustReadFile(t, path)); got != "user change" {
		t.Fatalf("conflict overwrote disk content: %q", got)
	}
}

func TestFlushOne_NewFileConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	if err := os.WriteFile(path, []byte("user file"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := FlushOne(Entry{
		Path:        path,
		OrigExisted: false,
		NewContent:  []byte("agent file"),
		Op:          OpWrite,
	})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if got := string(mustReadFile(t, path)); got != "user file" {
		t.Fatalf("conflict overwrote disk content: %q", got)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
