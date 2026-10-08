package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/permissions"
)

// makePermPromptModel builds a minimal Model with an active permission prompt.
// guardFirstKey is set to true (as it would be in production) so tests can
// verify both guard and normal key behaviour.
func makePermPromptModel(t *testing.T, withGuard bool) (Model, chan permissionReply) {
	t.Helper()
	reply := make(chan permissionReply, 1)
	// handlePermissionKey dereferences *m.cfg.Program to keep the program alive
	// in the reply goroutine. Supply a nil *tea.Program so the dereference
	// succeeds without actually running a real TUI program.
	var prog *tea.Program
	m := Model{
		width:  100,
		height: 40,
		cfg:    Config{Program: &prog},
	}
	m.permPrompt = &permissionPromptState{
		toolName:      "Bash",
		toolInput:     "rm -rf /tmp/test",
		reply:         reply,
		selected:      0,
		guardFirstKey: withGuard,
	}
	return m, reply
}

// TestPermission_GuardFirstKey verifies that the first keystroke after the
// permission prompt opens is swallowed (preventing an in-flight Enter from
// auto-accepting the tool). Esc/ctrl+c bypass the guard.
func TestPermission_GuardFirstKey(t *testing.T) {
	tests := []struct {
		name     string
		msg      tea.KeyPressMsg
		passThru bool // esc/ctrl+c bypass the guard
	}{
		{"enter swallowed", tea.KeyPressMsg{Code: tea.KeyEnter}, false},
		{"space swallowed", tea.KeyPressMsg{Code: ' ', Text: " "}, false},
		{"1 swallowed", tea.KeyPressMsg{Code: '1', Text: "1"}, false},
		{"esc passes through", tea.KeyPressMsg{Code: tea.KeyEsc}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, reply := makePermPromptModel(t, true)

			m2, cmd := m.handlePermissionKey(tt.msg)
			if tt.passThru {
				// Esc should resolve the prompt as deny.
				if m2.permPrompt != nil {
					t.Error("esc should bypass the guard and close the prompt")
				}
				if cmd == nil {
					t.Error("esc should produce a command")
				}
				cmd()
				select {
				case r := <-reply:
					if r.allow {
						t.Error("esc should deny, got allow=true")
					}
				default:
					t.Error("esc: no reply sent on channel")
				}
			} else {
				// Key should be swallowed: prompt stays open, no command.
				if m2.permPrompt == nil {
					t.Error("prompt should still be open after guard swallow")
				}
				if cmd != nil {
					t.Errorf("key should be swallowed (no cmd) but got a command")
				}
				if m2.permPrompt.guardFirstKey {
					t.Error("guardFirstKey should be false after first key is swallowed")
				}
			}
		})
	}
}

// TestPermission_EnterAcceptsAfterGuardClears verifies that Enter accepts
// the default option (Allow once) on the second keystroke — the guard should
// only block the very first key.
func TestPermission_EnterAcceptsAfterGuardClears(t *testing.T) {
	m, reply := makePermPromptModel(t, true)

	// First Enter — swallowed by guard.
	m2, cmd := m.handlePermissionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("first Enter should be swallowed (guard active)")
	}
	if m2.permPrompt == nil {
		t.Fatal("prompt should still be open after guard swallow")
	}

	// Second Enter — should accept (selected=0 = Allow once).
	m3, cmd2 := m2.handlePermissionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd2 == nil {
		t.Fatal("second Enter should produce a command")
	}
	if m3.permPrompt != nil {
		t.Error("prompt should be closed after second Enter")
	}
	cmd2()
	select {
	case r := <-reply:
		if !r.allow {
			t.Errorf("second Enter should allow (selected=0), got allow=%v", r.allow)
		}
		if r.alwaysAllow {
			t.Error("second Enter at selected=0 should not set alwaysAllow")
		}
	default:
		t.Error("no reply sent after second Enter")
	}
}

// TestPermission_NoGuard verifies normal key handling when the guard is inactive
// (covers the direct handlePermissionKey path used in render tests etc.).
func TestPermission_NoGuard(t *testing.T) {
	m, reply := makePermPromptModel(t, false)

	m2, cmd := m.handlePermissionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter without guard should produce a command")
	}
	if m2.permPrompt != nil {
		t.Error("prompt should be closed")
	}
	cmd()
	select {
	case r := <-reply:
		if !r.allow {
			t.Errorf("Enter at selected=0 should allow, got allow=%v", r.allow)
		}
	default:
		t.Error("no reply sent")
	}
}

func TestPermissionChoicesFor(t *testing.T) {
	tests := []struct {
		name      string
		label     string
		protected bool
		want      []permChoice
	}{
		{"direct", "", false, []permChoice{permAllowOnce, permAlwaysAllow, permDeny}},
		{"direct protected write hides always allow", "", true, []permChoice{permAllowOnce, permDeny}},
		{"subagent", "worker", false, []permChoice{permAllowOnce, permSwitchToAuto, permDeny}},
		{"subagent protected", "worker", true, []permChoice{permAllowOnce, permSwitchToAuto, permDeny}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := permissionChoicesFor(tt.label, tt.protected)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestNewPermissionPromptMarksProtectedWrites(t *testing.T) {
	cwd := t.TempDir()
	gate := permissions.New(cwd, nil, permissions.ModeDefault, nil, nil, nil)
	m := idleModel()
	m.cfg.Gate = gate
	tests := []struct {
		name, tool, input string
		want              bool
	}{
		{"git config write", "Write", ".git/config", true},
		{"ordinary write", "Write", "main.go", false},
		{"protected path via read tool", "Read", ".git/config", false},
		{"bash is not a path write", "Bash", ".git/config", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := m.newPermissionPrompt(permissionAskMsg{toolName: tt.tool, toolInput: tt.input})
			if p.protected != tt.want {
				t.Errorf("protected = %v, want %v", p.protected, tt.want)
			}
		})
	}
}

func TestProtectedPromptEnterOnLastOptionDenies(t *testing.T) {
	m, reply := makePermPromptModel(t, false)
	m.permPrompt.protected = true
	m.permPrompt.selected = 0
	m, _ = m.handlePermissionKey(tea.KeyPressMsg{Code: tea.KeyDown})
	m, _ = m.handlePermissionKey(tea.KeyPressMsg{Code: tea.KeyDown}) // clamps at last option
	if m.permPrompt.selected != 1 {
		t.Fatalf("selected = %d, want 1 (only two options)", m.permPrompt.selected)
	}
	_, cmd := m.handlePermissionKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	cmd()
	if r := <-reply; r.allow || r.alwaysAllow {
		t.Errorf("reply = %+v, want deny", r)
	}
}
