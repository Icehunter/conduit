package tui

import (
	tea "charm.land/bubbletea/v2"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestInputHistoryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history", "proj.jsonl")
	s, got := openInputHistory(path)
	if len(got) != 0 {
		t.Fatalf("fresh history = %v", got)
	}
	s.Add("first")
	s.Add("multi\nline")
	s.Add("first")
	s.Add("")
	s.Add(strings.Repeat("x", inputHistoryEntryMax+1))
	s.Close()

	s2, got := openInputHistory(path)
	defer s2.Close()
	want := []string{"first", "multi\nline", "first"}
	if !slices.Equal(got, want) {
		t.Errorf("reloaded = %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("history file mode = %o, want 600", perm)
	}
}

func TestInputHistoryIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.jsonl")
	lines := make([]string, 0, inputHistoryMax+51)
	for i := range inputHistoryMax + 50 {
		lines = append(lines, fmt.Sprintf("%q", fmt.Sprintf("prompt %d", i)))
	}
	lines = append(lines, "not json")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, got := openInputHistory(path)
	s.Close()
	if len(got) != inputHistoryMax || got[0] != "prompt 50" || got[len(got)-1] != fmt.Sprintf("prompt %d", inputHistoryMax+49) {
		t.Fatalf("got %d entries, first %q last %q", len(got), got[0], got[len(got)-1])
	}
	// The file itself is trimmed on load.
	again, total := loadInputHistory(path)
	if total != inputHistoryMax || len(again) != inputHistoryMax {
		t.Errorf("file not trimmed: total=%d entries=%d", total, len(again))
	}
}

func TestSubmitPersistsHistory(t *testing.T) {
	var saved []string
	var prog *tea.Program // runLoopCmd only dereferences it; the loop is never run here
	m := New(Config{Resumed: true, Program: &prog, InputHistory: []string{"old"}, PersistInput: func(s string) { saved = append(saved, s) }})
	if len(m.inputHistory) != 1 || m.historyIdx != -1 {
		t.Fatalf("history not seeded: %v idx=%d", m.inputHistory, m.historyIdx)
	}
	m.input.SetValue("new prompt")
	m.handleKeyBuiltins(keyPress("enter"))
	if !slices.Equal(saved, []string{"new prompt"}) {
		t.Errorf("persisted = %q", saved)
	}
}
