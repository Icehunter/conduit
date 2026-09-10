package tui

import (
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/tools/todowritetool"
)

// TestCtrlT_TogglesTodoStrip_EvenInTeamMode guards against a regression
// where ctrl+t toggled the teammate strip instead of the todo strip whenever
// a team session was active, leaving the todo strip's visibility stuck with
// no way to bring it back via the keyboard.
func TestCtrlT_TogglesTodoStrip_EvenInTeamMode(t *testing.T) {
	cleanup := seedTodos(t, []todowritetool.Todo{
		{ID: "1", Content: "task", Status: todowritetool.StatusPending},
	})
	defer cleanup()

	m := Model{width: 100, height: 40, teamActive: true}
	m.input = textarea.New()
	m.todoStripHidden = false

	m2, _, consumed := m.handleKeyBuiltins(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if !consumed {
		t.Fatal("ctrl+t was not consumed")
	}
	if !m2.todoStripHidden {
		t.Error("ctrl+t did not hide the todo strip while team mode is active")
	}
	if m2.teammateStripHidden {
		t.Error("ctrl+t unexpectedly touched the teammate strip")
	}

	m3, _, _ := m2.handleKeyBuiltins(tea.KeyPressMsg{Code: 't', Mod: tea.ModCtrl})
	if m3.todoStripHidden {
		t.Error("second ctrl+t did not show the todo strip again")
	}
}

// TestCtrlG_TogglesTeammateStrip verifies the dedicated team-strip binding
// and that it is a no-op outside of team mode.
func TestCtrlG_TogglesTeammateStrip(t *testing.T) {
	m := Model{width: 100, height: 40, teamActive: true}
	m.input = textarea.New()

	m2, _, consumed := m.handleKeyBuiltins(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if !consumed {
		t.Fatal("ctrl+g was not consumed")
	}
	if !m2.teammateStripHidden {
		t.Error("ctrl+g did not hide the teammate strip")
	}

	m3 := Model{width: 100, height: 40, teamActive: false}
	m3.input = textarea.New()
	m4, _, consumed := m3.handleKeyBuiltins(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	if !consumed {
		t.Fatal("ctrl+g was not consumed outside team mode")
	}
	if m4.teammateStripHidden {
		t.Error("ctrl+g toggled teammateStripHidden outside team mode")
	}
}
