package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func quitConfirmModel() Model {
	m := idleModel()
	m.quitConfirm = &quitConfirmState{selected: 1} // default: Nope
	return m
}

// TestQuitConfirm_DefaultSelectionIsNope ensures we don't quit on accidental Enter.
func TestQuitConfirm_DefaultSelectionIsNope(t *testing.T) {
	m := quitConfirmModel()
	if m.quitConfirm.selected != 1 {
		t.Fatalf("default selected = %d; want 1 (Nope)", m.quitConfirm.selected)
	}
}

// TestQuitConfirm_EnterOnNopeDismisses verifies Enter on the default (Nope) selection
// clears the dialog without quitting.
func TestQuitConfirm_EnterOnNopeDismisses(t *testing.T) {
	m := quitConfirmModel()
	m2, cmd := m.handleQuitConfirmKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m2.quitConfirm != nil {
		t.Error("quitConfirm should be nil after Enter on Nope")
	}
	if cmd != nil {
		t.Errorf("cmd should be nil after dismiss; got %T", cmd)
	}
}

// TestQuitConfirm_EnterOnYepQuits verifies Enter on Yep returns tea.Quit.
func TestQuitConfirm_EnterOnYepQuits(t *testing.T) {
	m := quitConfirmModel()
	m.quitConfirm.selected = 0 // Yep!
	m2, cmd := m.handleQuitConfirmKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m2.quitConfirm != nil {
		t.Error("quitConfirm should be nil after quitting")
	}
	if cmd == nil {
		t.Fatal("expected tea.Quit cmd, got nil")
	}
	// Execute the cmd and verify it returns tea.QuitMsg.
	msg := cmd()
	if _, ok := msg.(tea.QuitMsg); !ok {
		t.Errorf("cmd() returned %T; want tea.QuitMsg", msg)
	}
}

// TestQuitConfirm_YKeyQuitsImmediately verifies the 'y' accelerator quits
// regardless of the current selection.
func TestQuitConfirm_YKeyQuitsImmediately(t *testing.T) {
	for _, sel := range []int{0, 1} {
		m := quitConfirmModel()
		m.quitConfirm.selected = sel
		m2, cmd := m.handleQuitConfirmKey(tea.KeyPressMsg{Code: 'y'})
		if m2.quitConfirm != nil {
			t.Errorf("selected=%d: quitConfirm should be nil after 'y'", sel)
		}
		if cmd == nil {
			t.Fatalf("selected=%d: expected tea.Quit cmd, got nil", sel)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Errorf("selected=%d: cmd() is not tea.QuitMsg", sel)
		}
	}
}

// TestQuitConfirm_DismissKeys verifies n, N, Esc, q all dismiss without quitting.
func TestQuitConfirm_DismissKeys(t *testing.T) {
	dismissKeys := []tea.KeyPressMsg{
		{Code: 'n'},
		{Code: 'N'},
		{Code: tea.KeyEsc},
		{Code: 'q'},
	}
	for _, key := range dismissKeys {
		m := quitConfirmModel()
		m2, cmd := m.handleQuitConfirmKey(key)
		if m2.quitConfirm != nil {
			t.Errorf("key %q: quitConfirm should be nil after dismiss", key.String())
		}
		if cmd != nil {
			t.Errorf("key %q: cmd should be nil after dismiss; got %T", key.String(), cmd)
		}
	}
}

// TestQuitConfirm_NavigationTogglesSelection verifies left/right/h/l move between buttons.
func TestQuitConfirm_NavigationTogglesSelection(t *testing.T) {
	tests := []struct {
		name    string
		start   int
		key     tea.KeyPressMsg
		wantSel int
	}{
		{"left from Nope → Yep", 1, tea.KeyPressMsg{Code: tea.KeyLeft}, 0},
		{"right from Yep → Nope", 0, tea.KeyPressMsg{Code: tea.KeyRight}, 1},
		{"h from Nope → Yep", 1, tea.KeyPressMsg{Code: 'h'}, 0},
		{"l from Yep → Nope", 0, tea.KeyPressMsg{Code: 'l'}, 1},
		{"left at Yep stays", 0, tea.KeyPressMsg{Code: tea.KeyLeft}, 0},
		{"right at Nope stays", 1, tea.KeyPressMsg{Code: tea.KeyRight}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := quitConfirmModel()
			m.quitConfirm.selected = tt.start
			m2, _ := m.handleQuitConfirmKey(tt.key)
			if m2.quitConfirm == nil {
				t.Fatal("quitConfirm should still be open")
			}
			if m2.quitConfirm.selected != tt.wantSel {
				t.Errorf("selected = %d; want %d", m2.quitConfirm.selected, tt.wantSel)
			}
		})
	}
}

// TestQuitConfirm_CtrlCIdle verifies that pressing ctrl+c when idle (not running)
// opens the quit-confirm dialog rather than quitting immediately.
func TestQuitConfirm_CtrlCIdle(t *testing.T) {
	m := idleModel()
	m.running = false
	m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if m2.quitConfirm == nil {
		t.Fatal("ctrl+c idle: quitConfirm should be set")
	}
	if m2.quitConfirm.selected != 1 {
		t.Errorf("ctrl+c idle: default selection = %d; want 1 (Nope)", m2.quitConfirm.selected)
	}
	if cmd != nil {
		t.Errorf("ctrl+c idle: expected nil cmd (not tea.Quit); got %T", cmd)
	}
}

// TestQuitConfirm_CtrlCRunningInterrupts verifies ctrl+c while running still
// cancels the turn without opening the quit dialog.
func TestQuitConfirm_CtrlCRunningInterrupts(t *testing.T) {
	m := idleModel()
	m.running = true
	cancelled := false
	m.cancelTurn = func() { cancelled = true }

	m2, _, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	if m2.quitConfirm != nil {
		t.Error("ctrl+c while running should NOT open quit-confirm")
	}
	if !cancelled {
		t.Error("ctrl+c while running should cancel the turn")
	}
}

// TestQuitConfirm_KeyHandlerIntercepts verifies the top-level handleKey routes
// to the quit-confirm handler when m.quitConfirm is set.
func TestQuitConfirm_KeyHandlerIntercepts(t *testing.T) {
	m := quitConfirmModel()
	// Any key should be consumed by the quit-confirm overlay.
	for _, key := range []string{"up", "down", "enter", "esc"} {
		_, _, consumed := m.handleKey(keyPress(key))
		if !consumed {
			t.Errorf("quit-confirm should consume %q", key)
		}
	}
}
