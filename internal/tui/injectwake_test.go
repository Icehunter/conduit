package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/bgjobs"
)

func wakeModel(t *testing.T) (Model, *agent.Loop) {
	t.Helper()
	lp := agent.NewLoop(nil, nil, agent.LoopConfig{Model: "claude-sonnet-5-5"})
	var prog *tea.Program // runLoopCmd only dereferences it; the loop is never run here
	return New(Config{Resumed: true, Loop: lp, Program: &prog}), lp
}

func TestInjectedWakeStartsTurn(t *testing.T) {
	notif := bgjobs.Notification(bgjobs.Job{ID: "bash_1", Command: "make test", Status: bgjobs.StatusCompleted, OutputPath: "/tmp/out"}, "ok\n")

	t.Run("idle session runs a turn", func(t *testing.T) {
		m, lp := wakeModel(t)
		lp.InjectMessage(notif)
		next, cmd := m.Update(injectedWakeMsg{})
		nm := next.(Model)
		if !nm.running || cmd == nil || nm.cancelTurn == nil {
			t.Fatalf("running=%v cmd=%v: the wake did not start a turn", nm.running, cmd != nil)
		}
		if lp.HasInjected() {
			t.Error("queue not drained")
		}
		last := nm.history[len(nm.history)-1]
		if last.Role != "user" || !strings.Contains(last.Content[0].Text, "<task-id>bash_1</task-id>") {
			t.Errorf("history tail = %+v", last)
		}
		row := nm.messages[len(nm.messages)-1]
		if row.Role != RoleSystem || !strings.Contains(row.Content, `Background command "make test" completed (exit 0)`) {
			t.Errorf("transcript row = %+v", row)
		}
		nm.cancelTurn()
	})

	t.Run("nothing queued does nothing", func(t *testing.T) {
		m, _ := wakeModel(t)
		next, cmd := m.Update(injectedWakeMsg{})
		if next.(Model).running || cmd != nil {
			t.Error("a wake with an empty queue started a turn")
		}
	})

	t.Run("busy session remembers the wake and starts a turn when idle", func(t *testing.T) {
		m, lp := wakeModel(t)
		m.running = true // /compact, council, shell mode and local calls set this without a loop turn
		lp.InjectMessage(notif)
		next, _ := m.Update(injectedWakeMsg{})
		busy := next.(Model)
		if !lp.HasInjected() || len(busy.history) != len(m.history) || !busy.wakePending {
			t.Fatalf("busy wake: queued=%v pending=%v", lp.HasInjected(), busy.wakePending)
		}
		// Anything that clears running (here a stray message) lets Update deliver.
		busy.running = false
		var cmd tea.Cmd
		next, cmd = busy.Update(clearFlash{})
		idle := next.(Model)
		if !idle.running || idle.wakePending || cmd == nil || lp.HasInjected() {
			t.Errorf("running=%v pending=%v queued=%v", idle.running, idle.wakePending, lp.HasInjected())
		}
		idle.cancelTurn()
	})

	t.Run("failed turn still delivers queued notifications", func(t *testing.T) {
		m, lp := wakeModel(t)
		m.running = true
		m.turnID = 1
		lp.InjectMessage(notif)
		next, cmd := m.Update(agentDoneMsg{turnID: 1, err: errors.New("boom")})
		nm := next.(Model)
		if !nm.running || cmd == nil || lp.HasInjected() {
			t.Errorf("running=%v queued=%v", nm.running, lp.HasInjected())
		}
		nm.cancelTurn()
	})

	t.Run("cancelled turn leaves notifications queued", func(t *testing.T) {
		m, lp := wakeModel(t)
		m.running = true
		m.turnID = 1
		lp.InjectMessage(notif)
		next, _ := m.Update(agentDoneMsg{turnID: 1, cancelled: true})
		if next.(Model).running || !lp.HasInjected() {
			t.Error("a user-cancelled turn started another")
		}
	})

	t.Run("each notification is its own block", func(t *testing.T) {
		m, lp := wakeModel(t)
		lp.InjectMessage(notif)
		lp.InjectMessage(notif)
		next, _ := m.Update(injectedWakeMsg{})
		nm := next.(Model)
		blocks := nm.history[len(nm.history)-1].Content
		if len(blocks) != 2 || !strings.HasPrefix(blocks[0].Text, "<task-notification>") || !strings.HasPrefix(blocks[1].Text, "<task-notification>") {
			t.Errorf("blocks = %+v", blocks)
		}
		nm.cancelTurn()
	})

	t.Run("turn that ends with notifications queued starts another", func(t *testing.T) {
		m, lp := wakeModel(t)
		m.running = true
		m.turnID = 1
		lp.InjectMessage(notif)
		hist := []api.Message{{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}}}
		next, cmd := m.Update(agentDoneMsg{turnID: 1, history: hist})
		nm := next.(Model)
		if !nm.running || cmd == nil || nm.turnID != 2 || lp.HasInjected() {
			t.Errorf("running=%v turnID=%d queued=%v", nm.running, nm.turnID, lp.HasInjected())
		}
		nm.cancelTurn()
	})
}

func TestInjectedRow(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"notification summary", "<task-notification>\n<summary>Workflow &quot;x&quot; done</summary>\n</task-notification>", `● Workflow "x" done`},
		{"plain text first line", "line one\nline two", "● line one"},
		{"long line clipped", strings.Repeat("a", 200), "● " + strings.Repeat("a", 119) + "…"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := injectedRow(tt.in); got != tt.want {
				t.Errorf("injectedRow = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWakeWithoutProgram(t *testing.T) {
	activeProgram.Store(nil)
	Wake() // must not panic or block
}

func TestShellsHint(t *testing.T) {
	if got := shellsHint(); got != "" {
		t.Errorf("hint with no jobs = %q", got)
	}
}
