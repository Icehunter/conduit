package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/subagent"
	"github.com/icehunter/conduit/internal/team"
)

// TestKillSelectedSubagent_CancelsRunningTeammate verifies that pressing "x"
// in the sub-agent list panel cancels the selected teammate's context without
// touching the rest of the team.
func TestKillSelectedSubagent_CancelsRunningTeammate(t *testing.T) {
	tm := team.New("test")
	orig := team.Default
	team.Default = tm
	defer func() { team.Default = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	if _, err := tm.Register("worker-1", cancel); err != nil {
		t.Fatalf("Register: %v", err)
	}

	subagent.Default.Add(subagent.Entry{
		ID:          "teammate-1",
		Label:       "worker-1",
		StartedAt:   time.Now(),
		TeammateFor: "worker-1",
	})
	defer subagent.Default.Remove("teammate-1")

	m := Model{width: 100, height: 40}
	m.subagentPanel = &subagentPanelState{
		entryIDs: []string{"teammate-1"},
		selected: 0,
		view:     "list",
	}

	m2, _ := m.handleSubagentListKey(tea.KeyPressMsg{Text: "x"})

	select {
	case <-ctx.Done():
		// good — kill cancelled the teammate's context
	case <-time.After(200 * time.Millisecond):
		t.Fatal("kill did not cancel the teammate context")
	}
	if m2.flashMsg != "killed worker-1" {
		t.Errorf("flashMsg = %q, want %q", m2.flashMsg, "killed worker-1")
	}
}

// TestKillSelectedSubagent_NonTeammateIsNoop verifies that a plain (non-team)
// sub-agent entry cannot be killed, since it has no independent cancellation
// handle.
func TestKillSelectedSubagent_NonTeammateIsNoop(t *testing.T) {
	subagent.Default.Add(subagent.Entry{
		ID:        "sub-1",
		Label:     "plain sub-agent",
		StartedAt: time.Now(),
	})
	defer subagent.Default.Remove("sub-1")

	m := Model{width: 100, height: 40}
	m.subagentPanel = &subagentPanelState{
		entryIDs: []string{"sub-1"},
		selected: 0,
		view:     "list",
	}

	m2, _ := m.handleSubagentListKey(tea.KeyPressMsg{Text: "x"})
	if m2.flashMsg != "only teammates can be killed" {
		t.Errorf("flashMsg = %q, want %q", m2.flashMsg, "only teammates can be killed")
	}
}

// TestKillSelectedSubagent_DoneEntryIsNoop verifies killing a completed entry
// reports it is not running rather than erroring.
func TestKillSelectedSubagent_DoneEntryIsNoop(t *testing.T) {
	subagent.Default.Add(subagent.Entry{
		ID:          "teammate-2",
		Label:       "worker-2",
		StartedAt:   time.Now(),
		TeammateFor: "worker-2",
	})
	subagent.Default.Remove("teammate-2") // moves to completed (DoneAt set)

	m := Model{width: 100, height: 40}
	m.subagentPanel = &subagentPanelState{
		entryIDs: []string{"teammate-2"},
		selected: 0,
		view:     "list",
	}

	m2, _ := m.handleSubagentListKey(tea.KeyPressMsg{Text: "x"})
	if m2.flashMsg != "agent is not running" {
		t.Errorf("flashMsg = %q, want %q", m2.flashMsg, "agent is not running")
	}
}
