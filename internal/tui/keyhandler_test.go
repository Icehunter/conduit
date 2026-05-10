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

func TestHandleKey_EnterShrinksMultilineInputAfterNoAuthSend(t *testing.T) {
	m := idleModel()
	var cmd tea.Cmd
	m, cmd = m.handleWindowSize(tea.WindowSizeMsg{Width: 100, Height: 40})
	if cmd == nil {
		t.Fatal("window resize should return repaint command")
	}
	m.noAuth = true
	m.input.SetValue("first line\nsecond line\nthird line")
	m = m.applyLayout()
	if got := m.input.Height(); got <= inputMinRows {
		t.Fatalf("test setup input height = %d, want > %d", got, inputMinRows)
	}

	m2, _, consumed := m.handleKeyBuiltins(keyPress("enter"))
	if !consumed {
		t.Fatal("enter should be consumed")
	}
	if got := m2.input.Value(); got != "" {
		t.Fatalf("input value after send = %q, want empty", got)
	}
	if got := m2.input.Height(); got != inputMinRows {
		t.Fatalf("input height after send = %d, want %d", got, inputMinRows)
	}
}

func TestHandleKey_EnterShrinksMultilineInputAfterSteeringSend(t *testing.T) {
	m := idleModel()
	m, _ = m.handleWindowSize(tea.WindowSizeMsg{Width: 100, Height: 40})
	m.running = true
	var steered string
	m.cfg.SteerMessage = func(text string) {
		steered = text
	}
	m.input.SetValue("steer line one\nsteer line two")
	m = m.applyLayout()
	if got := m.input.Height(); got <= inputMinRows {
		t.Fatalf("test setup input height = %d, want > %d", got, inputMinRows)
	}

	m2, _, consumed := m.handleKeyBuiltins(keyPress("enter"))
	if !consumed {
		t.Fatal("enter should be consumed")
	}
	if steered != "steer line one\nsteer line two" {
		t.Fatalf("steered text = %q", steered)
	}
	if got := m2.input.Value(); got != "" {
		t.Fatalf("input value after steering send = %q, want empty", got)
	}
	if got := m2.input.Height(); got != inputMinRows {
		t.Fatalf("input height after steering send = %d, want %d", got, inputMinRows)
	}
}

func TestPluginDiscoverIRequiresSpaceToggle(t *testing.T) {
	m := idleModel()
	newState := func(search string) *pluginPanelState {
		p := &pluginPanelState{
			tab:            pluginTabDiscover,
			discoverSearch: search,
			discoverItems: []discoverItem{
				{pluginID: "frontend-design@claude-plugins-official", name: "frontend-design"},
			},
		}
		p.applyDiscoverFilter()
		return p
	}

	// No toggles, no search — i appends to search.
	p1 := newState("")
	_, cmd := m.handlePluginListKey(p1, "i")
	if cmd != nil {
		t.Fatal("i without toggled items should not install anything")
	}
	if p1.discoverSearch != "i" {
		t.Fatalf("i without toggles should append to search, got: %q", p1.discoverSearch)
	}

	// No toggles, search active — i appends to search.
	p2 := newState("front")
	_, cmd2 := m.handlePluginListKey(p2, "i")
	if cmd2 != nil {
		t.Fatal("i with search active but no toggles should not install")
	}
	if p2.discoverSearch != "fronti" {
		t.Fatalf("i should append to search, got: %q", p2.discoverSearch)
	}

	// Toggled item, search active — i appends to search (not install).
	p3 := newState("front")
	p3.discoverItems[0].selected = true
	m3, cmd3 := m.handlePluginListKey(p3, "i")
	if cmd3 != nil {
		t.Fatal("i with search active should not install even if items are toggled")
	}
	if m3.pluginPanel.discoverSearch != "fronti" {
		t.Fatalf("i with search+toggles should append to search, got: %q", m3.pluginPanel.discoverSearch)
	}

	// Toggled item, no search — i installs.
	p4 := newState("")
	p4.discoverItems[0].selected = true
	_, cmd4 := m.handlePluginListKey(p4, "i")
	if cmd4 == nil {
		t.Fatal("i with toggled item and no search should install")
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
