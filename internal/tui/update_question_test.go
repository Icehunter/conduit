package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// makeQuestionAskMsg builds a questionAskMsg with the given option labels and
// a buffered reply channel owned by the caller.
func makeQuestionAskMsg(opts []string, reply chan []string) questionAskMsg {
	options := make([]questionOption, 0, len(opts))
	for _, o := range opts {
		options = append(options, questionOption{Label: o, Value: o})
	}
	return questionAskMsg{
		question: "which one?",
		options:  options,
		multi:    false,
		reply:    reply,
	}
}

// TestQuestionAskMsg_EmptyInput_ActivatesImmediately verifies that when the
// input field is empty a questionAskMsg activates the dialog immediately.
func TestQuestionAskMsg_EmptyInput_ActivatesImmediately(t *testing.T) {
	m := idleModel()
	m.input.SetValue("")

	reply := make(chan []string, 1)
	msg := makeQuestionAskMsg([]string{"yes", "no"}, reply)
	m2, _ := m.Update(msg)
	m2model := m2.(Model)

	if m2model.questionAsk == nil {
		t.Fatal("questionAsk should be active when input is empty")
	}
	if m2model.pendingQuestion != nil {
		t.Fatal("pendingQuestion should be nil when dialog activates immediately")
	}
}

// TestQuestionAskMsg_NonEmptyInput_Defers verifies that a questionAskMsg
// while the user has text in the input is deferred to pendingQuestion.
func TestQuestionAskMsg_NonEmptyInput_Defers(t *testing.T) {
	m := idleModel()
	m.input.SetValue("hello")

	reply := make(chan []string, 1)
	msg := makeQuestionAskMsg([]string{"yes", "no"}, reply)
	m2, _ := m.Update(msg)
	m2model := m2.(Model)

	if m2model.questionAsk != nil {
		t.Fatal("questionAsk should be nil when input has text")
	}
	if m2model.pendingQuestion == nil {
		t.Fatal("pendingQuestion should be set when input has text")
	}
	if m2model.flashMsg == "" {
		t.Fatal("flashMsg should indicate the pending question")
	}
}

// TestPendingQuestion_PromotedOnInputClear verifies that after a question is
// deferred, pressing Backspace to clear the input promotes the question.
func TestPendingQuestion_PromotedOnInputClear(t *testing.T) {
	m := idleModel()
	m.input.SetValue("x")

	reply := make(chan []string, 1)
	msg := makeQuestionAskMsg([]string{"yes", "no"}, reply)
	m2, _ := m.Update(msg)
	m2model := m2.(Model)

	if m2model.pendingQuestion == nil {
		t.Fatal("precondition: pendingQuestion should be set")
	}

	// Press Backspace — clears "x", input becomes empty → pending question promoted.
	m3, _ := m2model.Update(tea.KeyPressMsg{Code: tea.KeyBackspace})
	m3model := m3.(Model)

	if m3model.questionAsk == nil {
		t.Fatal("questionAsk should be promoted after input empties")
	}
	if m3model.pendingQuestion != nil {
		t.Fatal("pendingQuestion should be cleared after promotion")
	}
	if !m3model.questionAsk.guardFirstKey {
		t.Error("promoted question dialog should have guardFirstKey set")
	}
}
