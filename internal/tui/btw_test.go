package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/commands"
)

func TestApplyBtwResult_NoLoop(t *testing.T) {
	m := idleModel()
	m2, cmd := m.applyBtwResult(commands.Result{Type: "btw", Text: "why?"})
	if cmd != nil {
		t.Error("a command was scheduled without a loop")
	}
	last := m2.messages[len(m2.messages)-1]
	if last.Role != RoleError || !strings.Contains(last.Content, "/btw unavailable") {
		t.Errorf("last row = %+v", last)
	}
	if m2.running {
		t.Error("/btw must not mark the session as running")
	}
}

func TestHandleBtwDone(t *testing.T) {
	tests := []struct {
		name     string
		msg      btwDoneMsg
		wantRole Role
		wantText string
	}{
		{"answer", btwDoneMsg{question: "why?", answer: "because"}, RoleSystem, "/btw why?\nbecause"},
		{"error", btwDoneMsg{question: "why?", err: errors.New("boom")}, RoleError, "/btw failed: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := idleModel()
			m.history = []api.Message{{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}}}
			m2 := m.handleBtwDone(tt.msg)
			last := m2.messages[len(m2.messages)-1]
			if last.Role != tt.wantRole || last.Content != tt.wantText {
				t.Errorf("row = %+v, want %v %q", last, tt.wantRole, tt.wantText)
			}
			if len(m2.history) != 1 {
				t.Error("side answer leaked into the conversation history")
			}
		})
	}
}

func TestHandleBtwDone_ReplacesPendingRow(t *testing.T) {
	m := idleModel()
	m.messages = append(m.messages, Message{Role: RoleSystem, Content: btwPending("why?")})
	n := len(m.messages)
	m2 := m.handleBtwDone(btwDoneMsg{question: "why?", answer: "because"})
	if len(m2.messages) != n {
		t.Fatalf("rows = %d, want %d (pending row should be replaced)", len(m2.messages), n)
	}
	if got := m2.messages[n-1].Content; got != "/btw why?\nbecause" {
		t.Errorf("row = %q", got)
	}
}

func TestBtwWhileRunning(t *testing.T) {
	reg := commands.New()
	commands.RegisterBuiltins(reg)
	commands.RegisterMiscCommands(reg)
	m := idleModel()
	m.cfg.Commands = reg
	m.running = true
	m.input.SetValue("/btw why?")
	m2, _, consumed := m.handleKeyBuiltins(keyPress("enter"))
	if !consumed {
		t.Fatal("enter not consumed")
	}
	if !m2.running {
		t.Error("/btw stopped the running turn")
	}
	if len(m2.pendingMessages) != 0 || m2.input.Value() != "" {
		t.Errorf("pending=%v input=%q", m2.pendingMessages, m2.input.Value())
	}
	found := false
	for _, msg := range m2.messages {
		if strings.Contains(msg.Content, "/btw") {
			found = true
		}
	}
	if !found {
		t.Error("/btw was swallowed while a turn was running")
	}
	// Other slash commands are still ignored mid-turn.
	m.input.SetValue("/clear")
	m3, _, _ := m.handleKeyBuiltins(keyPress("enter"))
	if len(m3.messages) != len(m.messages) {
		t.Error("/clear should not run mid-turn")
	}
}
