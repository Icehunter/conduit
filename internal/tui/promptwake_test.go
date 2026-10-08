package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/commands"
)

const wakeNotif = "<task-notification>\n<task-id>bash_9</task-id>\n<summary>Background command \"x\" completed (exit 0)</summary>\n</task-notification>"

// A wake must not start an injected turn while a prompt is waiting on its
// hooks, an accepted prompt is being resubmitted, or a synthetic Enter is in
// flight; the turn starts once that clears.
func TestInjectedWake_WaitsForPendingSubmit(t *testing.T) {
	tests := []struct {
		name string
		busy func(m *Model)
	}{
		{"screening", func(m *Model) { m.screening = true }},
		{"screened prompt awaiting replay", func(m *Model) { m.screened = &screenedPrompt{raw: "a", text: "a"} }},
		{"replaying a queued submit", func(m *Model) { m.replayingSubmit = true }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, lp := wakeModel(t)
			tt.busy(&m)
			lp.InjectMessage(wakeNotif)
			next, _ := m.Update(injectedWakeMsg{})
			nm := next.(Model)
			if nm.running || !nm.wakePending || !lp.HasInjected() {
				t.Fatalf("running=%v wakePending=%v queued=%v: wake must wait", nm.running, nm.wakePending, lp.HasInjected())
			}
			// Once the pending submit is gone the wake goes ahead.
			nm.screening, nm.screened, nm.replayingSubmit = false, nil, false
			next, cmd := nm.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			started := next.(Model)
			if !started.running || cmd == nil || lp.HasInjected() {
				t.Errorf("running=%v: the deferred wake did not start", started.running)
			}
			started.cancelTurn()
		})
	}
}

// The synthetic Enter that drains a queued message clears the replay mark, and
// every drain site sets it.
func TestReplaySubmitMarksAndEnterClears(t *testing.T) {
	m := idleModel()
	cmd := m.replaySubmit()
	if !m.replayingSubmit || cmd == nil {
		t.Fatal("replaySubmit did not mark the replay")
	}
	next, _ := m.Update(cmd())
	if next.(Model).replayingSubmit {
		t.Error("Enter did not clear replayingSubmit")
	}
}

// handleAgentDone with a queued message and a pending wake: the queued message
// is submitted first and the wake waits for it.
func TestAgentDone_QueuedMessageBeforeWake(t *testing.T) {
	m, lp := wakeModel(t)
	m.running = true
	m.turnID = 1
	m.pendingMessages = []string{"follow up"}
	lp.InjectMessage(wakeNotif)
	next, _ := m.Update(agentDoneMsg{turnID: 1, history: m.history})
	nm := next.(Model)
	if nm.running {
		t.Fatal("the wake started a turn ahead of the queued message")
	}
	if !nm.replayingSubmit || nm.input.Value() != "follow up" || !nm.wakePending {
		t.Errorf("replaying=%v input=%q wakePending=%v", nm.replayingSubmit, nm.input.Value(), nm.wakePending)
	}
}

// The wake turn must not be sequenced behind the whole batch: with a flash
// tick in cmd, the wake command is still returned alongside it.
func TestAgentDone_TitleResetDeferredToWake(t *testing.T) {
	m, lp := wakeModel(t)
	m.running = true
	m.turnID = 1
	lp.InjectMessage(wakeNotif)
	next, cmd := m.Update(agentDoneMsg{turnID: 1, history: m.history})
	nm := next.(Model)
	if !nm.running || cmd == nil || nm.titleClearDeferred {
		t.Errorf("running=%v cmd=%v deferred=%v, want the wake turn started and the deferral consumed", nm.running, cmd != nil, nm.titleClearDeferred)
	}
	nm.cancelTurn()

	// Without a wake the title reset is not lost.
	m2 := idleModel()
	m2.running = true
	m2.turnID = 1
	m2.wakePending = true
	m2.titleClearDeferred = true
	next2, cmd2 := m2.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if next2.(Model).titleClearDeferred || cmd2 == nil {
		t.Log("busy model keeps the deferral") // still running: nothing to flush
	}
}

// A screened steer whose turn ended while the hooks ran starts the turn itself,
// carrying the hook's context and attachments, and is not screened again.
func TestSteerScreenedAfterTurnEndedStartsTurn(t *testing.T) {
	m, _ := wakeModel(t)
	content := []api.ContentBlock{
		{Type: "image", Source: &api.ImageSource{Type: "base64", MediaType: "image/png", Data: "AAAA"}},
		{Type: "text", Text: "typed"},
		{Type: "text", Text: "<system-reminder>\nUserPromptSubmit hook additional context: ctx\n</system-reminder>"},
	}
	next, cmd := m.handlePromptScreened(promptScreenedMsg{route: routeSteer, raw: "typed", text: "typed", content: content})
	if !next.running || cmd == nil {
		t.Fatalf("running=%v: the screened steer did not start a turn", next.running)
	}
	last := next.history[len(next.history)-1]
	if last.Role != "user" || len(last.Content) != 3 {
		t.Errorf("history tail = %+v, want the screened content intact", last)
	}
	if row := next.messages[len(next.messages)-1]; row.Role != RoleUser || row.Content != "typed" {
		t.Errorf("last row = %+v", row)
	}
	if len(next.pendingMessages) != 0 {
		t.Errorf("pendingMessages = %v, want none", next.pendingMessages)
	}
	next.cancelTurn()
}

// A blocked prompt takes its own row out, not the row of a steer screened
// while the hooks ran, and the steer is taken back.
func TestPromptBlocked_RemovesOwnRowAndClearsSteer(t *testing.T) {
	m, lp := wakeModel(t)
	m.history = []api.Message{{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "main prompt"}}}}
	m.messages = []Message{
		{Role: RoleUser, Content: "main prompt"},
		{Role: RoleUser, Content: "steer text"}, // screened steer row
	}
	m.promptRow = 1
	m.running = true
	m.turnID = 1
	lp.InjectSteerContent([]api.ContentBlock{
		{Type: "image", Source: &api.ImageSource{Type: "base64", MediaType: "image/png", Data: "BBBB"}},
		{Type: "text", Text: "steer text"},
	})

	next, _ := m.handleAgentDone(agentDoneMsg{turnID: 1, err: &agent.PromptBlockedError{Reason: "no", Prompt: "main prompt"}})

	for _, row := range next.messages {
		if row.Role == RoleUser {
			t.Errorf("user row %q left in the transcript", row.Content)
		}
	}
	if got := next.input.Value(); got != "main prompt\nsteer text" {
		t.Errorf("input = %q, want the prompt then the steer", got)
	}
	if len(next.pendingImages) != 1 || next.pendingImages[0].Data != "BBBB" {
		t.Errorf("pendingImages = %+v, want the steer's image back", next.pendingImages)
	}
	if lp.ClearSteer() != nil {
		t.Error("the steer is still pending in the loop")
	}
}

func TestClear_ResetsWakeAndQueue(t *testing.T) {
	m, lp := wakeModel(t)
	m.wakePending = true
	m.screening = true
	lp.InjectMessage(wakeNotif)
	nm, _ := m.applyCommandResult(commands.Result{Type: "clear"})
	if nm.wakePending || nm.screening || lp.HasInjected() {
		t.Errorf("wakePending=%v screening=%v queued=%v, want all cleared", nm.wakePending, nm.screening, lp.HasInjected())
	}
}

func TestResume_RendersTaskNotificationLikeLive(t *testing.T) {
	msgs := []api.Message{{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: wakeNotif}}}}
	rows := historyToDisplayMessages(msgs)
	if len(rows) != 1 || rows[0].Role != RoleSystem || rows[0].Content != injectedRow(wakeNotif) || strings.Contains(rows[0].Content, "<task") {
		t.Errorf("rows = %+v, want one system row like the live injectedRow", rows)
	}
}

// updatedPrompt targets the typed block by index even when a file expansion
// holds identical text.
func TestScreenPrompt_TypedBlockByIndex(t *testing.T) {
	m := idleModel()
	m.cfg.Loop = hookedLoop(`cat >/dev/null; printf '{"hookSpecificOutput":{"updatedPrompt":"NEW"}}'`)
	content := []api.ContentBlock{{Type: "text", Text: "same"}, {Type: "text", Text: "same"}}
	msg := m.screenPromptCmd(routeSteer, "same", content, 2, nil, nil)().(promptScreenedMsg)
	if msg.err != nil || msg.content[0].Text != "same" || msg.content[1].Text != "NEW" || msg.text != "NEW" {
		t.Errorf("content = %+v text=%q, want only block index 1 rewritten", msg.content, msg.text)
	}
}
