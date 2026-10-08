package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/commands"
)

func TestCopyFlashReportsRealOutcome(t *testing.T) {
	tests := []struct {
		name    string
		res     copyResultMsg
		want    string
		notWant string
	}{
		{"success", copyResultMsg{what: "selection"}, "✓ Copied selection", "failed"},
		{"failure", copyResultMsg{what: "selection", err: errors.New("exec: pbcopy: not found")}, "Native clipboard failed", "✓ Copied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := copyFlash(tt.res)
			if !strings.Contains(got, tt.want) || strings.Contains(got, tt.notWant) {
				t.Errorf("copyFlash = %q, want contains %q and not %q", got, tt.want, tt.notWant)
			}
		})
	}
}

func TestCopyResultUpdateSetsFlash(t *testing.T) {
	m := idleModel()
	next, cmd := m.Update(copyResultMsg{what: "code block", err: errors.New("boom")})
	if got := next.(Model).flashMsg; !strings.Contains(got, "boom") {
		t.Errorf("flash = %q, want the error surfaced", got)
	}
	if cmd == nil {
		t.Error("flash must be scheduled to clear")
	}
}

func TestCopyLastResultCopiesLiveResponse(t *testing.T) {
	m := idleModel()
	m.messages = []Message{{Role: RoleAssistant, Content: "a1"}, {Role: RoleUser, Content: "q"}, {Role: RoleAssistant, Content: "a2"}}
	_, cmd := m.applyCommandResult(commands.Result{Type: "copy-last"})
	if cmd == nil {
		t.Fatal("copy-last must return a copy command")
	}
	if got := m.lastAssistantText(); got != "a2" {
		t.Errorf("lastAssistantText = %q", got)
	}
	empty := idleModel()
	m2, cmd := empty.applyCommandResult(commands.Result{Type: "copy-last"})
	if cmd != nil || m2.messages[len(m2.messages)-1].Role != RoleError {
		t.Errorf("no response: want error row and no cmd, got cmd=%v", cmd != nil)
	}
}

func TestMemoryExtractRunsOffTheEventLoop(t *testing.T) {
	var gotRecent string
	m := idleModel()
	m.history = []api.Message{{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "remember this"}}}}
	m.cfg.ExtractMemory = func(_ context.Context, recent string) error { gotRecent = recent; return errors.New("boom") }
	m2, cmd := m.applyCommandResult(commands.Result{Type: "memory-extract"})
	if cmd == nil || gotRecent != "" {
		t.Fatalf("extraction must be deferred to a Cmd: cmd=%v ran=%q", cmd != nil, gotRecent)
	}
	done := cmd().(memoryExtractDoneMsg)
	if !strings.Contains(gotRecent, "remember this") || done.err == nil {
		t.Errorf("recent=%q err=%v", gotRecent, done.err)
	}
	next, _ := m2.Update(done)
	last := next.(Model).messages
	if last[len(last)-1].Role != RoleError {
		t.Errorf("failure should show an error row")
	}
	// Nothing to extract: no Cmd.
	if _, cmd := idleModelWithExtract().applyCommandResult(commands.Result{Type: "memory-extract"}); cmd != nil {
		t.Error("empty history must not start an extraction")
	}
}

func idleModelWithExtract() Model {
	m := idleModel()
	m.cfg.ExtractMemory = func(context.Context, string) error { return nil }
	return m
}

func TestJoinSelectedRows(t *testing.T) {
	const limit = 20
	tests := []struct {
		name  string
		rows  []string
		limit int
		want  string
	}{
		{"soft wrap joined", []string{"the quick brown fox", "jumps over"}, limit, "the quick brown fox jumps over"},
		{"short line keeps newline", []string{"one", "two"}, limit, "one\ntwo"},
		{"list item starts block", []string{"the quick brown fox", "- item"}, limit, "the quick brown fox\n- item"},
		{"numbered item starts block", []string{"the quick brown fox", "2. item"}, limit, "the quick brown fox\n2. item"},
		{"blank row is paragraph break", []string{"the quick brown fox", "", "next"}, limit, "the quick brown fox\n\nnext"},
		{"unknown width falls back to newlines", []string{"the quick brown fox", "jumps"}, 0, "the quick brown fox\njumps"},
		{"indented row keeps newline", []string{"- the quick brown fox", "  jumps over"}, limit, "- the quick brown fox\n  jumps over"},
		{"code row keeps newline", []string{"the quick brown fox", "    indented()"}, limit, "the quick brown fox\n    indented()"},
		{"short row with fitting word keeps newline", []string{"the quick brown", "fox"}, limit, "the quick brown\nfox"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts := append([]string(nil), tt.rows...)
			fulls := append([]string(nil), tt.rows...)
			if got := joinSelectedRows(parts, fulls, tt.limit); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResumeHint(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "full.jsonl")
	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(full, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, id, file, want string
	}{
		{"session with content", "abc-123", full, "conduit --resume abc-123"},
		{"empty file", "abc-123", empty, ""},
		{"missing file", "abc-123", filepath.Join(dir, "none.jsonl"), ""},
		{"no id", "", full, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resumeHint(tt.id, tt.file)
			if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
				t.Errorf("resumeHint = %q, want contains %q", got, tt.want)
			}
		})
	}
}

func TestMouseCaptureSetting(t *testing.T) {
	tests := []struct {
		name    string
		disable bool
		want    tea.MouseMode
	}{
		{"captured by default", false, tea.MouseModeCellMotion},
		{"disabled by setting", true, tea.MouseModeNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(Config{Resumed: true, DisableMouse: tt.disable})
			if got := m.View().MouseMode; got != tt.want {
				t.Errorf("MouseMode = %v, want %v", got, tt.want)
			}
		})
	}
}
