package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestHandleResumeKeySpaceExtendsActiveFilter(t *testing.T) {
	m := idleModel()
	m.resumePrompt = &resumePromptState{
		sessions: []resumeSession{
			{filePath: "one.jsonl", preview: "alpha beta", age: "today"},
			{filePath: "two.jsonl", preview: "alphabet", age: "yesterday"},
		},
		filter: "alpha",
	}
	m.resumePrompt.applyFilter()

	got, cmd := m.handleResumeKey(tea.KeyPressMsg{Code: tea.KeySpace})
	if cmd != nil {
		t.Fatal("space while filtering should not load the selected session")
	}
	if got.resumePrompt == nil {
		t.Fatal("space while filtering should keep the resume picker open")
	}
	if got.resumePrompt.filter != "alpha " {
		t.Fatalf("filter after space = %q, want %q", got.resumePrompt.filter, "alpha ")
	}
	if len(got.resumePrompt.filtered) != 1 {
		t.Fatalf("filtered count after space = %d, want 1", len(got.resumePrompt.filtered))
	}
}

func TestHandleResumeKeyEnterLoadsSelectedSession(t *testing.T) {
	m := idleModel()
	m.resumePrompt = &resumePromptState{
		sessions: []resumeSession{
			{filePath: "one.jsonl", preview: "alpha beta", age: "today"},
		},
	}
	m.resumePrompt.applyFilter()

	got, cmd := m.handleResumeKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should load the selected session")
	}
	if got.resumePrompt != nil {
		t.Fatal("enter should close the resume picker")
	}
}
