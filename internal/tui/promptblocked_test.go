package tui

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/attach"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
)

func blockedModel() Model {
	m := idleModel()
	m.history = []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "earlier"}}},
		{Role: "assistant", Content: []api.ContentBlock{{Type: "text", Text: "ok"}}},
		{Role: "user", Content: []api.ContentBlock{
			{Type: "image", Source: &api.ImageSource{Type: "base64", MediaType: "image/png", Data: "AAAA"}},
			{Type: "text", Text: "my prompt"},
		}},
	}
	m.messages = []Message{
		{Role: RoleUser, Content: "earlier"},
		{Role: RoleAssistant, Content: "ok"},
		{Role: RoleUser, Content: "my prompt"},
	}
	m.running = true
	m.turnID = 1
	return m
}

func TestHandleAgentDone_PromptBlocked(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		draft     string
		wantRow   string
		wantInput string
	}{
		{
			name:      "blocked prompt is restored",
			err:       &agent.PromptBlockedError{Reason: "no secrets", Prompt: "my prompt"},
			wantRow:   "UserPromptSubmit blocked: no secrets",
			wantInput: "my prompt",
		},
		{
			name:      "wrapped error is still recognised",
			err:       fmt.Errorf("run: %w", &agent.PromptBlockedError{Reason: "policy", Prompt: "my prompt"}),
			wantRow:   "UserPromptSubmit blocked: policy",
			wantInput: "my prompt",
		},
		{
			name:      "stopped shows the stop reason",
			err:       &agent.PromptBlockedError{Reason: "halted by hook", Prompt: "my prompt", Stopped: true},
			wantRow:   "halted by hook",
			wantInput: "my prompt",
		},
		{
			name:      "a slash command is restored as typed",
			err:       &agent.PromptBlockedError{Reason: "no", Prompt: "/review 12"},
			wantRow:   "UserPromptSubmit blocked: no",
			wantInput: "/review 12",
		},
		{
			name:      "a draft typed meanwhile is kept below the restored prompt",
			err:       &agent.PromptBlockedError{Reason: "no", Prompt: "my prompt"},
			draft:     "half-written follow-up",
			wantRow:   "UserPromptSubmit blocked: no",
			wantInput: "my prompt\nhalf-written follow-up",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := blockedModel()
			if tt.draft != "" {
				m.input.SetValue(tt.draft)
			}
			// The loop returns the history without the dropped prompt.
			m2, _ := m.handleAgentDone(agentDoneMsg{turnID: 1, history: m.history[:2], err: tt.err})

			if got := len(m2.history); got != 2 {
				t.Errorf("history len = %d, want 2 (blocked prompt removed)", got)
			}
			for _, msg := range m2.messages {
				if msg.Role == RoleUser && msg.Content == "my prompt" {
					t.Error("blocked prompt row still in the transcript")
				}
			}
			last := m2.messages[len(m2.messages)-1]
			if last.Role != RoleSystem || last.Content != tt.wantRow {
				t.Errorf("last row = %+v, want system row %q", last, tt.wantRow)
			}
			if got := m2.input.Value(); got != tt.wantInput {
				t.Errorf("input = %q, want %q", got, tt.wantInput)
			}
			if len(m2.pendingImages) != 1 || m2.pendingImages[0].Data != "AAAA" {
				t.Errorf("pendingImages = %+v, want the prompt's image restored", m2.pendingImages)
			}
			if m2.running {
				t.Error("model still running after the turn ended")
			}
		})
	}
}

// After an updatedPrompt rewrite, cancelling must leave the rewrite in the
// history so the next turn does not send the text the hook redacted.
func TestHandleAgentDone_CancelAdoptsRewrittenPrompt(t *testing.T) {
	m := blockedModel()
	rewritten := []api.Message{m.history[0], m.history[1], {Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "REDACTED"}}}}
	m2, _ := m.handleAgentDone(agentDoneMsg{turnID: 1, history: rewritten, cancelled: true})
	last := m2.history[len(m2.history)-1]
	if len(m2.history) != 3 || last.Content[0].Text != "REDACTED" {
		t.Errorf("history = %+v, want the rewritten prompt last", m2.history)
	}
}

func TestHandlePromptScreened(t *testing.T) {
	steered := func(m *Model, got *[]api.ContentBlock) {
		m.cfg.SteerContent = func(c []api.ContentBlock) { *got = c }
	}
	t.Run("steer blocked restores text and attachments", func(t *testing.T) {
		m := idleModel()
		m.running = true
		var got []api.ContentBlock
		steered(&m, &got)
		m2, _ := m.handlePromptScreened(promptScreenedMsg{
			route:  routeSteer,
			raw:    "stop doing that",
			err:    &agent.PromptBlockedError{Reason: "rude", Prompt: "stop doing that"},
			images: []*attach.Image{{Data: "AAAA", MediaType: "image/png"}},
		})
		if got != nil {
			t.Error("blocked steering reached the loop")
		}
		if m2.input.Value() != "stop doing that" || len(m2.pendingImages) != 1 {
			t.Errorf("input=%q images=%d, want text and image restored", m2.input.Value(), len(m2.pendingImages))
		}
		if last := m2.messages[len(m2.messages)-1]; last.Content != "UserPromptSubmit blocked: rude" {
			t.Errorf("last row = %q", last.Content)
		}
	})
	t.Run("steer accepted is injected with the hooks' content", func(t *testing.T) {
		m := idleModel()
		m.running = true
		var got []api.ContentBlock
		steered(&m, &got)
		content := []api.ContentBlock{{Type: "text", Text: "new text"}}
		m2, _ := m.handlePromptScreened(promptScreenedMsg{route: routeSteer, raw: "typed", text: "new text", content: content})
		if len(got) != 1 || got[0].Text != "new text" {
			t.Errorf("steered = %+v", got)
		}
		if last := m2.messages[len(m2.messages)-1]; last.Role != RoleUser || last.Content != "typed" {
			t.Errorf("last row = %+v, want the typed text", last)
		}
	})
	t.Run("steer accepted after the turn ended starts a turn", func(t *testing.T) {
		m, _ := wakeModel(t)
		var got []api.ContentBlock
		steered(&m, &got)
		m2, _ := m.handlePromptScreened(promptScreenedMsg{route: routeSteer, raw: "typed", text: "typed", content: []api.ContentBlock{{Type: "text", Text: "typed"}}})
		if got != nil || !m2.running || len(m2.pendingMessages) != 0 {
			t.Errorf("steered=%v running=%v pending=%v, want a new turn", got, m2.running, m2.pendingMessages)
		}
		m2.cancelTurn()
	})
	t.Run("council blocked leaves the input alone", func(t *testing.T) {
		m := idleModel()
		m.input.SetValue("debate this")
		m.screening = true
		m2, _ := m.handlePromptScreened(promptScreenedMsg{route: routeCouncil, raw: "debate this", err: &agent.PromptBlockedError{Reason: "no", Prompt: "debate this"}})
		if m2.input.Value() != "debate this" || m2.screening || m2.screened != nil {
			t.Errorf("input=%q screening=%v screened=%v", m2.input.Value(), m2.screening, m2.screened)
		}
	})
	t.Run("council accepted resubmits with the rewrite", func(t *testing.T) {
		m := idleModel()
		m.screening = true
		m2, cmd := m.handlePromptScreened(promptScreenedMsg{route: routeCouncil, raw: "debate this", text: "debate that"})
		if m2.screened == nil || m2.screened.text != "debate that" || m2.screening || cmd == nil {
			t.Errorf("screened=%+v screening=%v cmd=%v", m2.screened, m2.screening, cmd != nil)
		}
	})
}

func TestHistoryToDisplayMessages_HidesSystemReminderBlocks(t *testing.T) {
	msgs := []api.Message{{Role: "user", Content: []api.ContentBlock{
		{Type: "text", Text: "do the thing"},
		{Type: "text", Text: "<system-reminder>\nUserPromptSubmit hook additional context: secret\n</system-reminder>"},
	}}}
	got := historyToDisplayMessages(msgs)
	if len(got) != 1 || got[0].Content != "do the thing" {
		t.Errorf("display = %+v, want only the prompt", got)
	}
	one, ok := historyToDisplayMessage(msgs[0])
	if !ok || one.Content != "do the thing" {
		t.Errorf("single display = %q, %v", one.Content, ok)
	}

	onlyReminder := api.Message{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "<system-reminder>x</system-reminder>"}}}
	if _, ok := historyToDisplayMessage(onlyReminder); ok {
		t.Error("a user message holding only a reminder must not produce a row")
	}
	if rows := historyToDisplayMessages([]api.Message{onlyReminder}); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
	if !strings.Contains(blockedRow(&agent.PromptBlockedError{}), "no reason given") {
		t.Error("empty reason not described")
	}
}

func hookedLoop(command string) *agent.Loop {
	return agent.NewLoop(nil, tool.NewRegistry(), agent.LoopConfig{
		Model: "m",
		Hooks: &settings.HooksSettings{UserPromptSubmit: []settings.HookMatcher{{
			Hooks: []settings.Hook{{Type: "command", Command: command}},
		}}},
	})
}

// Steering and council prompts do not reach Loop.RunPrompt, so Enter must send
// them through the hooks first instead of straight to their destination.
func TestEnter_ScreensPromptsThatBypassTheLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook command runner uses sh")
	}
	t.Run("steering", func(t *testing.T) {
		m := idleModel()
		m.cfg.Loop = hookedLoop("cat >/dev/null; echo no >&2; exit 2")
		steered := false
		m.cfg.SteerContent = func([]api.ContentBlock) { steered = true }
		m.running = true
		m.input.SetValue("change course")

		m2, cmd, handled := m.handleKeyBuiltins(keyPress("enter"))
		if !handled || cmd == nil {
			t.Fatalf("handled=%v cmd=%v, want a screening command", handled, cmd != nil)
		}
		if steered {
			t.Fatal("steering was injected before the hooks ran")
		}
		msg, ok := cmd().(promptScreenedMsg)
		if !ok || msg.route != routeSteer || msg.raw != "change course" {
			t.Fatalf("cmd result = %#v", msg)
		}
		var pb *agent.PromptBlockedError
		if !errors.As(msg.err, &pb) || pb.Reason != "no" {
			t.Fatalf("err = %v, want a block with reason no", msg.err)
		}
		m3, _ := m2.handlePromptScreened(msg)
		if steered || m3.input.Value() != "change course" {
			t.Errorf("steered=%v input=%q, want blocked steering restored to the input", steered, m3.input.Value())
		}
	})
	t.Run("council", func(t *testing.T) {
		m := idleModel()
		m.cfg.Loop = hookedLoop("cat >/dev/null; printf '{\"hookSpecificOutput\":{\"updatedPrompt\":\"rewritten\"}}'")
		m.permissionMode = permissions.ModeCouncil
		m.input.SetValue("should we use redis for the session cache")

		m2, cmd, handled := m.handleKeyBuiltins(keyPress("enter"))
		if !handled || cmd == nil || !m2.screening || m2.running {
			t.Fatalf("handled=%v cmd=%v screening=%v running=%v, want screening before any dispatch", handled, cmd != nil, m2.screening, m2.running)
		}
		if m2.input.Value() != "should we use redis for the session cache" {
			t.Errorf("input cleared before the verdict: %q", m2.input.Value())
		}
		msg := cmd().(promptScreenedMsg)
		if msg.err != nil || msg.text != "rewritten" {
			t.Fatalf("msg = %+v, want the rewritten text", msg)
		}
	})
}
