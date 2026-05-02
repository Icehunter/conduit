package secure

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFileStorage_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	s := NewFileStorage(path)

	if _, err := s.Get("svc", "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on missing: err = %v", err)
	}
	if err := s.Set("svc", "k", []byte("secret-bytes")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := s.Get("svc", "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, []byte("secret-bytes")) {
		t.Errorf("Get = %q", got)
	}

	if err := s.Delete("svc", "k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("svc", "k"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: err = %v", err)
	}
}

func TestFileStorage_PersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")

	s1 := NewFileStorage(path)
	if err := s1.Set("svc", "k", []byte("hello")); err != nil {
		t.Fatal(err)
	}

	s2 := NewFileStorage(path)
	got, err := s2.Get("svc", "k")
	if err != nil {
		t.Fatalf("second instance Get: %v", err)
	}
	if !bytes.Equal(got, []byte("hello")) {
		t.Errorf("Get = %q", got)
	}
}

func TestFileStorage_FileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are not meaningful on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "creds.json")
	s := NewFileStorage(path)
	if err := s.Set("svc", "k", []byte("x")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %o; want 0600", info.Mode().Perm())
	}
}

func TestFileStorage_RefusesGroupReadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission semantics differ on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewFileStorage(path)
	_, err := s.Get("svc", "k")
	if err == nil {
		t.Fatal("expected error from world-readable file")
	}
}
