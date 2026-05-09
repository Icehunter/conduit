package fileedittool

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/icehunter/conduit/internal/pendingedits"
	"github.com/icehunter/conduit/internal/permissions"
)

// fakeStager records every Stage call without applying anything to disk.
type fakeStager struct {
	mu      sync.Mutex
	staged  []pendingedits.Entry
	failNow bool
}

func (s *fakeStager) Stage(e pendingedits.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNow {
		return os.ErrPermission
	}
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

func TestFileEdit_WithStager_DoesNotTouchDisk(t *testing.T) {
	path := writeFile(t, "hello world\n")
	stat0, _ := os.Stat(path)
	mtime0 := stat0.ModTime()

	stager := &fakeStager{}
	tt := NewWithStager(stager)
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

	// Disk content must be untouched.
	if got := readFile(t, path); got != "hello world\n" {
		t.Errorf("disk content changed: %q", got)
	}
	stat1, _ := os.Stat(path)
	if !stat1.ModTime().Equal(mtime0) {
		t.Error("file mtime changed; staging must not touch disk")
	}

	// Stager must have received exactly one entry with disk→staged content.
	staged := stager.snapshot()
	if len(staged) != 1 {
		t.Fatalf("staged %d entries, want 1", len(staged))
	}
	e := staged[0]
	if string(e.OrigContent) != "hello world\n" {
		t.Errorf("OrigContent = %q", e.OrigContent)
	}
	if string(e.NewContent) != "goodbye world\n" {
		t.Errorf("NewContent = %q", e.NewContent)
	}
	if !e.OrigExisted {
		t.Error("OrigExisted should be true for an edited file")
	}
	if e.Op != pendingedits.OpEdit {
		t.Errorf("Op = %v, want OpEdit", e.Op)
	}
	if e.ToolName != "Edit" {
		t.Errorf("ToolName = %q, want Edit", e.ToolName)
	}

	// Tool result text must carry the "Staged:" marker so downstream layers
	// (PostToolUse hooks, TUI) can recognise it.
	if len(res.Content) == 0 || !contains(res.Content[0].Text, "Staged: ") {
		t.Errorf("result missing 'Staged:' marker: %q", res.Content[0].Text)
	}
}

func TestFileEdit_WithStager_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")

	stager := &fakeStager{}
	tt := NewWithStager(stager)
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  path,
		"old_string": "",
		"new_string": "fresh\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}

	// File must NOT exist on disk yet.
	if _, err := os.Stat(path); err == nil {
		t.Error("staged create must not touch disk")
	}

	staged := stager.snapshot()
	if len(staged) != 1 {
		t.Fatalf("staged %d, want 1", len(staged))
	}
	if staged[0].OrigExisted {
		t.Error("OrigExisted should be false for new file")
	}
	if string(staged[0].NewContent) != "fresh\n" {
		t.Errorf("NewContent = %q", staged[0].NewContent)
	}
}

func TestFileEdit_NilStager_BehavesAsBefore(t *testing.T) {
	// NewWithStager(nil) must be observably identical to New().
	path := writeFile(t, "hello\n")
	tt := NewWithStager(nil)
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  path,
		"old_string": "hello",
		"new_string": "bye",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	if got := readFile(t, path); got != "bye\n" {
		t.Errorf("disk content = %q (write should have happened)", got)
	}
}

func TestFileEdit_StagerFailureSurfacedAsError(t *testing.T) {
	path := writeFile(t, "x\n")
	stager := &fakeStager{failNow: true}
	tt := NewWithStager(stager)
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  path,
		"old_string": "x",
		"new_string": "y",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("expected IsError when stager fails")
	}
	if got := readFile(t, path); got != "x\n" {
		t.Errorf("disk content = %q (must be unchanged on stager failure)", got)
	}
}

func TestFileEdit_WithGatedStager_ComposesSequentialStagedEdits(t *testing.T) {
	path := writeFile(t, "hello world\nsecond line\n")
	table := pendingedits.NewTable()
	gate := permissions.New("", nil, permissions.ModeAcceptEdits, nil, nil, nil)
	tt := NewWithStager(&pendingedits.GatedStager{Table: table, Gate: gate})

	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  path,
		"old_string": "hello",
		"new_string": "goodbye",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("first edit IsError: %v", res.Content)
	}
	res, err = tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  path,
		"old_string": "goodbye world",
		"new_string": "farewell world",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("second edit IsError: %v", res.Content)
	}

	if got := readFile(t, path); got != "hello world\nsecond line\n" {
		t.Fatalf("disk content changed while staging: %q", got)
	}
	e, ok := table.Get(path)
	if !ok {
		t.Fatal("missing staged entry")
	}
	if string(e.OrigContent) != "hello world\nsecond line\n" {
		t.Errorf("OrigContent = %q", e.OrigContent)
	}
	if string(e.NewContent) != "farewell world\nsecond line\n" {
		t.Errorf("NewContent = %q", e.NewContent)
	}
}

func TestFileEdit_WithGatedStager_EditsStagedCreate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.txt")
	table := pendingedits.NewTable()
	gate := permissions.New("", nil, permissions.ModeAcceptEdits, nil, nil, nil)
	tt := NewWithStager(&pendingedits.GatedStager{Table: table, Gate: gate})

	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  path,
		"old_string": "",
		"new_string": "alpha\n",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("create IsError: %v", res.Content)
	}
	res, err = tt.Execute(context.Background(), input(t, map[string]any{
		"file_path":  path,
		"old_string": "alpha",
		"new_string": "beta",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("edit staged create IsError: %v", res.Content)
	}

	e, ok := table.Get(path)
	if !ok {
		t.Fatal("missing staged entry")
	}
	if e.OrigExisted {
		t.Error("OrigExisted changed for staged create")
	}
	if string(e.NewContent) != "beta\n" {
		t.Errorf("NewContent = %q", e.NewContent)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
