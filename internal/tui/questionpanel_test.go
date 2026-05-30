package tui

import (
	"testing"
)

// makeQuestionModel builds a minimal Model with a populated questionAskState.
// opts is the list of option labels for a single-select dialog (pass nil for
// free-text mode). guardFirstKey is set to match what newQuestionAskState does.
func makeQuestionModel(t *testing.T, opts []string, multi bool) (Model, chan []string) {
	t.Helper()
	reply := make(chan []string, 1)
	options := make([]questionOption, 0, len(opts))
	for _, o := range opts {
		options = append(options, questionOption{Label: o, Value: o})
	}
	msg := questionAskMsg{
		question: "pick one",
		options:  options,
		multi:    multi,
		reply:    reply,
	}
	m := idleModel()
	m.questionAsk = newQuestionAskState(msg)
	return m, reply
}

// TestGuardFirstKey_SwallowsFirstNonEscKey verifies that the first non-esc,
// non-ctrl+c keypress after dialog open is swallowed and the dialog stays open.
func TestGuardFirstKey_SwallowsFirstNonEscKey(t *testing.T) {
	m, reply := makeQuestionModel(t, []string{"alpha", "beta"}, false)

	if !m.questionAsk.guardFirstKey {
		t.Fatal("guardFirstKey should be true after newQuestionAskState")
	}

	// Send "1" — should be swallowed because guardFirstKey is true.
	m2, cmd := m.handleQuestionKey(makeKey("1"))
	if cmd != nil {
		cmd() // drain any pending command
	}

	// Dialog must still be open.
	if m2.questionAsk == nil {
		t.Fatal("dialog should still be open after first guarded key")
	}
	// guardFirstKey must now be cleared.
	if m2.questionAsk.guardFirstKey {
		t.Fatal("guardFirstKey should be cleared after first key")
	}
	// Reply channel must be empty — no answer was sent.
	select {
	case got := <-reply:
		t.Fatalf("reply channel should be empty after guarded key, got %v", got)
	default:
	}
	// Focus should not have moved (still 0).
	if m2.questionAsk.focusedIdx != 0 {
		t.Errorf("focusedIdx = %d; want 0 (guard should prevent focus move)", m2.questionAsk.focusedIdx)
	}
}

// TestGuardFirstKey_EscPassesThrough verifies that esc is not swallowed even
// when guardFirstKey is true (user must be able to cancel immediately).
func TestGuardFirstKey_EscPassesThrough(t *testing.T) {
	m, reply := makeQuestionModel(t, []string{"alpha", "beta"}, false)

	m2, cmd := m.handleQuestionKey(makeKey("esc"))
	if cmd != nil {
		cmd()
	}

	// Dialog should be closed.
	if m2.questionAsk != nil {
		t.Fatal("esc should close the dialog even with guardFirstKey set")
	}
	// Cancel sends nil on the reply channel.
	select {
	case got := <-reply:
		if got != nil {
			t.Errorf("cancel should send nil, got %v", got)
		}
	default:
		t.Fatal("cancel should have sent nil on reply channel")
	}
}

// TestDigitFocusOnly_SingleSelect verifies that after the guard is cleared a
// digit keystroke only moves focus and does NOT submit in single-select mode.
func TestDigitFocusOnly_SingleSelect(t *testing.T) {
	m, reply := makeQuestionModel(t, []string{"alpha", "beta", "gamma"}, false)
	// Clear the guard manually so we can test the digit behavior in isolation.
	m.questionAsk.guardFirstKey = false

	// Press "2" — should move focus to index 1 but not submit.
	m2, cmd := m.handleQuestionKey(makeKey("2"))
	if cmd != nil {
		cmd()
	}

	if m2.questionAsk == nil {
		t.Fatal("dialog should still be open after digit key")
	}
	if m2.questionAsk.focusedIdx != 1 {
		t.Errorf("focusedIdx = %d; want 1", m2.questionAsk.focusedIdx)
	}
	select {
	case got := <-reply:
		t.Fatalf("digit should not submit; got %v", got)
	default:
	}
}

// TestEnterAfterDigit_SingleSelect verifies that Enter after a digit focus
// delivers the focused answer and closes the dialog.
func TestEnterAfterDigit_SingleSelect(t *testing.T) {
	m, reply := makeQuestionModel(t, []string{"alpha", "beta", "gamma"}, false)
	m.questionAsk.guardFirstKey = false

	// Focus "beta" (index 1) via digit "2".
	m2, _ := m.handleQuestionKey(makeKey("2"))
	// Confirm with Enter.
	m3, cmd := m2.handleQuestionKey(makeKey("enter"))
	if cmd == nil {
		t.Fatal("enter should produce a cmd to send the answer")
	}
	cmd() // drain into reply channel

	if m3.questionAsk != nil {
		t.Fatal("dialog should be closed after Enter")
	}
	select {
	case got := <-reply:
		if len(got) != 1 || got[0] != "beta" {
			t.Errorf("answer = %v; want [beta]", got)
		}
	default:
		t.Fatal("no answer sent on reply channel")
	}
}

// TestMultiSelectDigit_TogglesAndFocuses verifies multi-select digit handling:
// digit toggles the option and moves focus; Enter on Submit delivers all checked.
func TestMultiSelectDigit_TogglesAndFocuses(t *testing.T) {
	m, reply := makeQuestionModel(t, []string{"alpha", "beta", "gamma"}, true)
	m.questionAsk.guardFirstKey = false

	// Toggle "beta" (index 1) via "2".
	m2, _ := m.handleQuestionKey(makeKey("2"))
	if m2.questionAsk.focusedIdx != 1 {
		t.Errorf("focusedIdx = %d; want 1", m2.questionAsk.focusedIdx)
	}
	if !m2.questionAsk.selected[1] {
		t.Error("option 1 should be selected after digit toggle")
	}

	// Navigate down to Submit via Down key twice more (focus is at 1, submit at 3).
	m3, _ := m2.handleQuestionKey(makeKey("down"))
	m4, _ := m3.handleQuestionKey(makeKey("down"))
	if m4.questionAsk.focusedIdx != m4.questionAsk.submitIdx() {
		t.Errorf("focusedIdx = %d; want submitIdx=%d", m4.questionAsk.focusedIdx, m4.questionAsk.submitIdx())
	}

	// Press Enter on Submit — should deliver "beta".
	m5, cmd := m4.handleQuestionKey(makeKey("enter"))
	if cmd == nil {
		t.Fatal("enter on Submit should produce a cmd")
	}
	cmd()

	if m5.questionAsk != nil {
		t.Fatal("dialog should be closed after Submit")
	}
	select {
	case got := <-reply:
		if len(got) != 1 || got[0] != "beta" {
			t.Errorf("answers = %v; want [beta]", got)
		}
	default:
		t.Fatal("no answer sent on reply channel")
	}
}
