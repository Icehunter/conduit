package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// keyPress constructs a KeyPressMsg from common key names.
func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "shift+up":
		return tea.KeyPressMsg{Code: tea.KeyUp, Mod: tea.ModShift}
	case "shift+down":
		return tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	default:
		runes := []rune(s)
		if len(runes) == 1 {
			return tea.KeyPressMsg{Code: runes[0]}
		}
		return tea.KeyPressMsg{Text: s}
	}
}

// idleModel returns a New(Config{}) model with no running state — safe base for key tests.
func idleModel() Model { return New(Config{}) }

// TestHandleKey_HistoryNav_Up verifies Up navigates backward through history when idle.
func TestHandleKey_HistoryNav_Up(t *testing.T) {
	m := idleModel()
	m.inputHistory = []string{"first command", "second command"}
	m.historyIdx = -1
	m.running = false

	m2, _, consumed := m.handleKeyBuiltins(keyPress("up"))
	if !consumed {
		t.Fatal("up should be consumed during history nav")
	}
	if m2.historyIdx != 1 {
		t.Errorf("historyIdx after first up = %d; want 1 (last entry)", m2.historyIdx)
	}
	if m2.input.Value() != "second command" {
		t.Errorf("input after first up = %q; want %q", m2.input.Value(), "second command")
	}

	// Second up → first entry.
	m3, _, _ := m2.handleKeyBuiltins(keyPress("up"))
	if m3.historyIdx != 0 {
		t.Errorf("historyIdx after second up = %d; want 0", m3.historyIdx)
	}
	if m3.input.Value() != "first command" {
		t.Errorf("input after second up = %q; want %q", m3.input.Value(), "first command")
	}
}

// TestHandleKey_HistoryNav_UpAtBoundary verifies Up doesn't go below index 0.
func TestHandleKey_HistoryNav_UpAtBoundary(t *testing.T) {
	m := idleModel()
	m.inputHistory = []string{"only command"}
	m.historyIdx = 0
	m.running = false
	m.input.SetValue("only command")

	m2, _, _ := m.handleKeyBuiltins(keyPress("up"))
	if m2.historyIdx != 0 {
		t.Errorf("historyIdx at top boundary = %d; want 0", m2.historyIdx)
	}
}

// TestHandleKey_HistoryNav_Down verifies Down navigates forward and restores draft.
func TestHandleKey_HistoryNav_Down(t *testing.T) {
	m := idleModel()
	m.inputHistory = []string{"first command", "second command"}
	m.historyIdx = 0
	m.historyDraft = "current draft"
	m.running = false
	m.input.SetValue("first command")

	m2, _, consumed := m.handleKeyBuiltins(keyPress("down"))
	if !consumed {
		t.Fatal("down should be consumed during history nav")
	}
	if m2.historyIdx != 1 {
		t.Errorf("historyIdx after down = %d; want 1", m2.historyIdx)
	}
}

// TestHandleKey_HistoryNav_NoHistory is a no-op when there's no history.
func TestHandleKey_HistoryNav_NoHistory(t *testing.T) {
	m := idleModel()
	m.running = false
	m.historyIdx = -1
	m2, _, consumed := m.handleKeyBuiltins(keyPress("up"))
	if !consumed {
		t.Fatal("up should still be consumed even with no history")
	}
	if m2.historyIdx != -1 {
		t.Errorf("historyIdx changed unexpectedly with no history: %d", m2.historyIdx)
	}
}

// TestHandleKey_ShiftUpDown verifies viewport scroll keys are consumed without panic.
func TestHandleKey_ShiftUpDown(t *testing.T) {
	m := idleModel()
	_, _, up := m.handleKeyBuiltins(keyPress("shift+up"))
	if !up {
		t.Error("shift+up should be consumed")
	}
	_, _, down := m.handleKeyBuiltins(keyPress("shift+down"))
	if !down {
		t.Error("shift+down should be consumed")
	}
}

// TestHandleKey_TrustDialog_CapturesAllKeys verifies the trust dialog owns the key space.
func TestHandleKey_TrustDialog_CapturesAllKeys(t *testing.T) {
	m := idleModel()
	m.trustDialog = &trustDialogState{}
	for _, key := range []string{"up", "down", "enter", "esc"} {
		_, _, consumed := m.handleKeyBuiltins(keyPress(key))
		if !consumed {
			t.Errorf("trust dialog should consume %q", key)
		}
	}
}

// TestHandleKey_QuestionAsk_CapturesAllKeys verifies the AskUserQuestion dialog owns the key space.
func TestHandleKey_QuestionAsk_CapturesAllKeys(t *testing.T) {
	m := idleModel()
	m.questionAsk = &questionAskState{}
	for _, key := range []string{"up", "down", "enter"} {
		_, _, consumed := m.handleKeyBuiltins(keyPress(key))
		if !consumed {
			t.Errorf("questionAsk dialog should consume %q", key)
		}
	}
}

// TestHandleKey_RunningUp_NotConsumed verifies Up is NOT consumed by the history
// handler when the agent is running (no history nav while busy; falls through to
// viewport or parent handler).
func TestHandleKey_RunningUp_NotConsumed(t *testing.T) {
	m := idleModel()
	m.running = true
	m.historyIdx = -1
	_, _, consumed := m.handleKeyBuiltins(keyPress("up"))
	if consumed {
		t.Error("up should not be consumed by history nav while agent is running")
	}
}

func TestPluginDiscoverIInstallsSelectedAfterSearch(t *testing.T) {
	m := idleModel()
	p := &pluginPanelState{
		tab:            pluginTabDiscover,
		discoverSearch: "front",
		discoverItems: []discoverItem{
			{pluginID: "frontend-design@claude-plugins-official", name: "frontend-design"},
		},
	}
	p.applyDiscoverFilter()

	m2, cmd := m.handlePluginListKey(p, "i")
	if cmd == nil {
		t.Fatal("i should install the selected discover row")
	}
	if m2.pluginPanel.discoverSearch != "front" {
		t.Fatalf("search changed after install shortcut: %q", m2.pluginPanel.discoverSearch)
	}
}

func TestPluginAddMarketplaceAcceptsPaste(t *testing.T) {
	m := idleModel()
	m.pluginPanel = &pluginPanelState{view: pluginViewAddMkt}

	m2, _ := m.handlePaste(tea.PasteMsg{Content: "anthropics/claude-plugins-official"})
	if got := m2.pluginPanel.addMktInput; got != "anthropics/claude-plugins-official" {
		t.Fatalf("pasted marketplace input = %q", got)
	}
}
