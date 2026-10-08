package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
)

func TestShellCommand(t *testing.T) {
	tests := []struct {
		in      string
		wantCmd string
		wantOK  bool
	}{
		{"!ls -la", "ls -la", true},
		{"  !  git status ", "git status", true},
		{"!", "", true},
		{"hello !world", "", false},
		{"/help", "", false},
	}
	for _, tt := range tests {
		cmd, ok := shellCommand(tt.in)
		if cmd != tt.wantCmd || ok != tt.wantOK {
			t.Errorf("shellCommand(%q) = %q,%v want %q,%v", tt.in, cmd, ok, tt.wantCmd, tt.wantOK)
		}
	}
}

func shellModel(gate *permissions.Gate, run func(context.Context, string) (string, bool)) Model {
	var prog *tea.Program
	m := New(Config{Resumed: true, Program: &prog, Gate: gate, RunShell: run})
	m, _ = m.handleWindowSize(tea.WindowSizeMsg{Width: 100, Height: 40})
	return m
}

func submit(t *testing.T, m Model, text string) (Model, tea.Cmd) {
	t.Helper()
	m.input.SetValue(text)
	m2, cmd, consumed := m.handleKeyBuiltins(keyPress("enter"))
	if !consumed {
		t.Fatal("enter not consumed")
	}
	return m2, cmd
}

func TestShellModeRunsAndRecordsContext(t *testing.T) {
	var ran []string
	m := shellModel(nil, func(_ context.Context, c string) (string, bool) { ran = append(ran, c); return "hi\n", false })
	m, cmd := submit(t, m, "!echo hi")
	if cmd == nil {
		t.Fatal("no command returned")
	}
	done, ok := cmd().(shellDoneMsg)
	if !ok {
		t.Fatalf("cmd result = %T", cmd())
	}
	next, _ := m.Update(done)
	m = next.(Model)

	if len(ran) != 1 || ran[0] != "echo hi" {
		t.Fatalf("ran = %v", ran)
	}
	var row *Message
	for i := range m.messages {
		if m.messages[i].Role == RoleTool && strings.HasPrefix(m.messages[i].ToolID, "shell-") {
			row = &m.messages[i]
		}
	}
	if row == nil || row.Content != "hi" || row.ToolError {
		t.Fatalf("tool row = %+v", row)
	}
	if len(m.history) != 1 {
		t.Fatalf("history len = %d, want 1", len(m.history))
	}
	text := m.history[0].Content[0].Text
	if !strings.Contains(text, "<bash-input>echo hi</bash-input>") || !strings.Contains(text, "<bash-stdout>hi") {
		t.Errorf("history entry = %q", text)
	}
	if got := m.inputHistory; len(got) != 1 || got[0] != "!echo hi" {
		t.Errorf("inputHistory = %v", got)
	}
}

func TestShellModeBareBangFlashes(t *testing.T) {
	called := false
	m := shellModel(nil, func(context.Context, string) (string, bool) { called = true; return "", false })
	m, cmd := submit(t, m, "!")
	if called || len(m.history) != 0 || !strings.Contains(m.flashMsg, "after !") {
		t.Errorf("called=%v history=%d flash=%q", called, len(m.history), m.flashMsg)
	}
	_ = cmd
}

func TestShellModeIsATurn(t *testing.T) {
	m := shellModel(nil, func(context.Context, string) (string, bool) { return "out", false })
	steered := false
	m.cfg.SteerContent = func([]api.ContentBlock) { steered = true }
	m, cmd := submit(t, m, "!ls")
	if !m.running || m.cancelTurn == nil || m.shellID == "" {
		t.Fatalf("shell mode must run as a turn: running=%v cancel=%v id=%q", m.running, m.cancelTurn != nil, m.shellID)
	}
	turn := m.turnID

	// A prompt typed meanwhile is queued, never steered into a (nonexistent) loop.
	m2, _ := submit(t, m, "next question")
	if len(m2.pendingMessages) != 1 || m2.pendingMessages[0] != "next question" {
		t.Fatalf("pending = %v", m2.pendingMessages)
	}
	if steered {
		t.Error("prompt was steered into a loop that is not running")
	}
	// So is a second shell command.
	m2, _ = submit(t, m2, "!pwd")
	if len(m2.pendingMessages) != 2 {
		t.Fatalf("pending = %v", m2.pendingMessages)
	}

	// Completion ends the turn, records history once, and drains the queue.
	next, drain := m2.Update(cmd())
	m3 := next.(Model)
	if m3.running || m3.shellID != "" || m3.turnID != turn {
		t.Errorf("turn not finished: running=%v id=%q", m3.running, m3.shellID)
	}
	if len(m3.history) != 1 {
		t.Errorf("history len = %d, want 1", len(m3.history))
	}
	if drain == nil || m3.input.Value() != "next question" || len(m3.pendingMessages) != 1 {
		t.Errorf("queue not drained: input=%q pending=%v", m3.input.Value(), m3.pendingMessages)
	}
}

func TestShellModeCtrlCCancelsAndLeavesHistoryAlone(t *testing.T) {
	var seen context.Context
	m := shellModel(nil, func(ctx context.Context, _ string) (string, bool) { seen = ctx; return "partial", false })
	m, cmd := submit(t, m, "!sleep 100")
	m, _, consumed := m.handleKeyBuiltins(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if !consumed || m.running {
		t.Fatalf("ctrl+c must cancel the shell turn: consumed=%v running=%v", consumed, m.running)
	}
	done := cmd().(shellDoneMsg)
	if seen == nil || seen.Err() == nil {
		t.Fatal("the command's context was not cancelled")
	}
	if !done.cancelled {
		t.Errorf("done.cancelled = false")
	}
	next, _ := m.Update(done)
	m = next.(Model)
	if len(m.history) != 0 {
		t.Errorf("cancelled command must not enter history: %d", len(m.history))
	}
}

// A late completion of an abandoned shell command must not touch a model turn
// that started after the cancel.
func TestShellDoneDoesNotInterleaveWithLaterTurn(t *testing.T) {
	m := shellModel(nil, func(context.Context, string) (string, bool) { return "late", false })
	m, cmd := submit(t, m, "!echo late")
	m, _, _ = m.handleKeyBuiltins(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	// A real turn begins.
	m.turnID++
	m.running = true
	m.cancelTurn = func() {}
	m.history = append(m.history, api.Message{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "real prompt"}}})
	before := len(m.history)
	next, _ := m.Update(cmd())
	m = next.(Model)
	if !m.running || len(m.history) != before {
		t.Errorf("late shell completion disturbed the turn: running=%v history %d -> %d", m.running, before, len(m.history))
	}
}

func TestShellModeRunsPreToolUseHooks(t *testing.T) {
	ran := false
	hookCfg := &settings.HooksSettings{PreToolUse: []settings.HookMatcher{{
		Matcher: "Bash",
		Hooks:   []settings.Hook{{Type: "command", Command: `echo '{"decision":"block","reason":"nope"}'`}},
	}}}
	var prog *tea.Program
	m := New(Config{Resumed: true, Program: &prog, Hooks: hookCfg, RunShell: func(context.Context, string) (string, bool) { ran = true; return "", false }})
	m, cmd := submit(t, m, "!echo hi")
	hook, ok := cmd().(shellHookMsg)
	if !ok || !hook.blocked {
		t.Fatalf("hook result = %+v ok=%v", hook, ok)
	}
	m2, cmd2 := m.shellAuthorize(hook)
	done := cmd2().(shellDoneMsg)
	if ran || !done.denied || !strings.Contains(done.output, "nope") {
		t.Errorf("ran=%v done=%+v", ran, done)
	}
	next, _ := m2.Update(done)
	if next.(Model).running {
		t.Error("turn must end after a blocked command")
	}
}

func TestShellModeRespectsPermissions(t *testing.T) {
	cwd := t.TempDir()
	t.Run("deny rule blocks the command", func(t *testing.T) {
		gate := permissions.New(cwd, nil, permissions.ModeDefault, nil, []string{"Bash"}, nil)
		ran := false
		m := shellModel(gate, func(context.Context, string) (string, bool) { ran = true; return "x", false })
		m, cmd := submit(t, m, "!echo hi")
		done := cmd().(shellDoneMsg)
		next, _ := m.Update(done)
		m = next.(Model)
		if ran || !done.denied || len(m.history) != 0 || m.running {
			t.Errorf("ran=%v denied=%v history=%d", ran, done.denied, len(m.history))
		}
	})
	t.Run("ask opens a once-only prompt and waits for the answer", func(t *testing.T) {
		gate := permissions.New(cwd, nil, permissions.ModeDefault, nil, nil, []string{"Bash"})
		ran := false
		m := shellModel(gate, func(context.Context, string) (string, bool) { ran = true; return "ok", false })
		m, cmd := submit(t, m, "!rm -rf build")
		if m.permPrompt == nil || !m.permPrompt.onceOnly {
			t.Fatalf("permPrompt = %+v", m.permPrompt)
		}
		if got := permissionChoicesFor("", m.permPrompt.protected || m.permPrompt.onceOnly); len(got) != 2 {
			t.Errorf("choices = %v, want allow/deny only", got)
		}
		result := make(chan tea.Msg, 1)
		go func() { result <- cmd() }()
		m.permPrompt.reply <- permissionReply{allow: true}
		done := (<-result).(shellDoneMsg)
		if !ran || done.denied || done.output != "ok" {
			t.Errorf("ran=%v done=%+v", ran, done)
		}
	})
	t.Run("ask then deny never runs", func(t *testing.T) {
		gate := permissions.New(cwd, nil, permissions.ModeDefault, nil, nil, []string{"Bash"})
		ran := false
		m := shellModel(gate, func(context.Context, string) (string, bool) { ran = true; return "", false })
		m, cmd := submit(t, m, "!rm -rf build")
		result := make(chan tea.Msg, 1)
		go func() { result <- cmd() }()
		m.permPrompt.reply <- permissionReply{allow: false}
		done := (<-result).(shellDoneMsg)
		if ran || !done.denied {
			t.Errorf("ran=%v done=%+v", ran, done)
		}
	})
}

func TestShellDoneCancelledOrDeniedStaysOutOfHistory(t *testing.T) {
	for _, tt := range []struct {
		name            string
		cancelled, deny bool
		wantHistory     int
	}{
		{"completed", false, false, 1},
		{"cancelled", true, false, 0},
		{"denied", false, true, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := shellModel(nil, func(context.Context, string) (string, bool) { return "x", false })
			m, _ = submit(t, m, "!echo x")
			m, _ = m.applyShellDone(shellDoneMsg{id: m.shellID, turnID: m.turnID, command: "echo x", output: "x", cancelled: tt.cancelled, denied: tt.deny})
			if len(m.history) != tt.wantHistory || m.running {
				t.Errorf("history=%d running=%v, want %d / false", len(m.history), m.running, tt.wantHistory)
			}
		})
	}
}

// A PreToolUse hook approval skips the ask prompt but never a deny rule.
func TestShellModeHookApprovalStillHonorsDeny(t *testing.T) {
	gate := permissions.New(t.TempDir(), nil, permissions.ModeDefault, nil, []string{"Bash(rm:*)"}, nil)
	ran := false
	m := shellModel(gate, func(context.Context, string) (string, bool) { ran = true; return "", false })
	m, _ = submit(t, m, "!rm -rf build")
	_, cmd := m.shellAuthorize(shellHookMsg{id: m.shellID, turnID: m.turnID, command: "rm -rf build", approved: true})
	if cmd == nil {
		t.Fatal("expected a command")
	}
	done, ok := cmd().(shellDoneMsg)
	if !ok || !done.denied || ran {
		t.Fatalf("hook-approved rm with deny rule: denied=%v ran=%v", done.denied, ran)
	}
}

func TestShellHookApprovalDoesNotSkipAskRule(t *testing.T) {
	gate := permissions.New(t.TempDir(), nil, permissions.ModeDefault, nil, nil, []string{"Bash(git push:*)"})
	m := shellModel(gate, func(context.Context, string) (string, bool) { return "", false })
	m, _ = submit(t, m, "!git push origin main")
	m.permPrompt = nil
	m2, _ := m.shellAuthorize(shellHookMsg{id: m.shellID, turnID: m.turnID, command: "git push origin main", approved: true})
	if m2.permPrompt == nil {
		t.Error("hook approval skipped an explicit ask rule")
	}
}

func TestShellHookAskAndUpdatedInput(t *testing.T) {
	gate := permissions.New(t.TempDir(), nil, permissions.ModeDefault, []string{"Bash"}, nil, nil)
	var got string
	m := shellModel(gate, func(_ context.Context, c string) (string, bool) { got = c; return "", false })
	m, _ = submit(t, m, "!echo original")
	m2, _ := m.shellAuthorize(shellHookMsg{id: m.shellID, turnID: m.turnID, command: "echo original", ask: true, updated: "echo rewritten"})
	if m2.permPrompt == nil {
		t.Fatal("hook ask did not prompt")
	}
	if m2.permPrompt.toolInput != "echo rewritten" {
		t.Errorf("prompt shows %q, want the rewritten command", m2.permPrompt.toolInput)
	}
	// Without ask the rewritten command runs.
	m3, cmd := m.shellAuthorize(shellHookMsg{id: m.shellID, turnID: m.turnID, command: "echo original", updated: "echo rewritten"})
	done := cmd().(shellDoneMsg)
	if got != "echo rewritten" || done.command != "echo rewritten" || m3.permPrompt != nil {
		t.Errorf("ran %q, done.command %q", got, done.command)
	}
}

func TestShellHookUpdatedInputRechecksGate(t *testing.T) {
	gate := permissions.New(t.TempDir(), nil, permissions.ModeDefault, nil, []string{"Bash(rm:*)"}, nil)
	ran := false
	m := shellModel(gate, func(context.Context, string) (string, bool) { ran = true; return "", false })
	m, _ = submit(t, m, "!echo hi")
	_, cmd := m.shellAuthorize(shellHookMsg{id: m.shellID, turnID: m.turnID, command: "echo hi", updated: "rm -rf /tmp/x"})
	done := cmd().(shellDoneMsg)
	if !done.denied || ran {
		t.Errorf("rewritten command bypassed deny rule: denied=%v ran=%v", done.denied, ran)
	}
}

func TestShellOutputRedactedInHistory(t *testing.T) {
	secret := "AKIAIOSFODNN7EXAMPLE"
	out := "AWS_ACCESS_KEY_ID=" + secret + "\nghp_" + strings.Repeat("a", 36)
	for _, off := range []bool{false, true} {
		var prog *tea.Program
		m := New(Config{Resumed: true, Program: &prog,
			RunShell:      func(context.Context, string) (string, bool) { return out, false },
			RedactSecrets: func() bool { return !off }})
		m, _ = m.handleWindowSize(tea.WindowSizeMsg{Width: 100, Height: 40})
		m, _ = submit(t, m, "!cat .env")
		_, cmd := m.shellAuthorize(shellHookMsg{id: m.shellID, turnID: m.turnID, command: "cat .env"})
		m3, _ := m.applyShellDone(cmd().(shellDoneMsg))
		last := m3.history[len(m3.history)-1].Content[0].Text
		if leaked := strings.Contains(last, secret); leaked != off {
			t.Errorf("redaction off=%v leaked=%v: %q", off, leaked, last)
		}
	}
}

// A secret typed into the command itself is masked in history too.
func TestShellCommandRedactedInHistory(t *testing.T) {
	cmdText := "export GITHUB_TOKEN=ghp_" + strings.Repeat("b", 36)
	var prog *tea.Program
	m := New(Config{Resumed: true, Program: &prog,
		RunShell: func(context.Context, string) (string, bool) { return "ok", false }})
	m, _ = m.handleWindowSize(tea.WindowSizeMsg{Width: 100, Height: 40})
	m, _ = submit(t, m, "!"+cmdText)
	_, cmd := m.shellAuthorize(shellHookMsg{id: m.shellID, turnID: m.turnID, command: cmdText})
	m3, _ := m.applyShellDone(cmd().(shellDoneMsg))
	last := m3.history[len(m3.history)-1].Content[0].Text
	if strings.Contains(last, "ghp_"+strings.Repeat("b", 36)) {
		t.Fatalf("token leaked into history: %q", last)
	}
}
