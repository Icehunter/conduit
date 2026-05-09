package filewritetool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/icehunter/conduit/internal/pendingedits"
)

type fakeStager struct {
	mu     sync.Mutex
	staged []pendingedits.Entry
}

func (s *fakeStager) Stage(e pendingedits.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.staged = append(s.staged, e)
	return nil
}

func (s *fakeStager) snapshot() []pendingedits.Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]pendingedits.Entry, len(s.staged))
	copy(out, s.staged)
	return out
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestFileWrite_WithStager_DoesNotTouchDisk_NewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	stager := &fakeStager{}
	tt := NewWithStager(stager)
	res, err := tt.Execute(context.Background(), mustJSON(t, map[string]any{
		"file_path": path,
		"content":   "hello\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("disk file must not be created on stage")
	}

	staged := stager.snapshot()
	if len(staged) != 1 {
		t.Fatalf("staged %d, want 1", len(staged))
	}
	e := staged[0]
	if e.OrigExisted {
		t.Error("OrigExisted should be false for new file")
	}
	if string(e.NewContent) != "hello\n" {
		t.Errorf("NewContent = %q", e.NewContent)
	}
	if e.Op != pendingedits.OpWrite {
		t.Errorf("Op = %v, want OpWrite", e.Op)
	}
}

func TestFileWrite_WithStager_OverwriteCapturesOrigContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stager := &fakeStager{}
	tt := NewWithStager(stager)
	res, err := tt.Execute(context.Background(), mustJSON(t, map[string]any{
		"file_path": path,
		"content":   "new\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "old\n" {
		t.Errorf("disk content = %q (must be unchanged on stage)", got)
	}

	staged := stager.snapshot()
	if len(staged) != 1 {
		t.Fatalf("staged %d, want 1", len(staged))
	}
	e := staged[0]
	if !e.OrigExisted {
		t.Error("OrigExisted = false, want true for existing file")
	}
	if string(e.OrigContent) != "old\n" {
		t.Errorf("OrigContent = %q, want %q", e.OrigContent, "old\n")
	}
	if string(e.NewContent) != "new\n" {
		t.Errorf("NewContent = %q", e.NewContent)
	}
}

func TestFileWrite_NilStager_BehavesAsBefore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	tt := NewWithStager(nil)
	res, err := tt.Execute(context.Background(), mustJSON(t, map[string]any{
		"file_path": path,
		"content":   "direct\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "direct\n" {
		t.Errorf("disk content = %q (write should have happened)", got)
	}
}
