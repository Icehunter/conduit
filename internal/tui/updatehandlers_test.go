package tui

import (
	"context"
	"testing"

	"github.com/icehunter/conduit/internal/api"
)

func TestHandleAgentDone_CancelledAdoptsResolvedHistory(t *testing.T) {
	m := idleModel()
	m.turnID = 1
	m.running = true
	m.history = []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "run it"}}},
	}

	resolved := []api.Message{
		m.history[0],
		{Role: "assistant", Content: []api.ContentBlock{{
			Type:  "tool_use",
			ID:    "toolu_cancelled",
			Name:  "Bash",
			Input: map[string]any{"command": "sleep 30"},
		}}},
		{Role: "user", Content: []api.ContentBlock{{
			Type:          "tool_result",
			ToolUseID:     "toolu_cancelled",
			IsError:       true,
			ResultContent: "Command cancelled.",
		}}},
	}

	m2, _ := m.handleAgentDone(agentDoneMsg{
		turnID:    1,
		history:   resolved,
		err:       context.Canceled,
		cancelled: true,
	})

	if len(m2.history) != len(resolved) {
		t.Fatalf("history len = %d; want %d", len(m2.history), len(resolved))
	}
	got := m2.history[2].Content[0]
	if got.Type != "tool_result" || got.ToolUseID != "toolu_cancelled" || !got.IsError {
		t.Fatalf("cancelled tool_result not preserved: %+v", got)
	}
	if m2.running {
		t.Fatal("running should be false after cancelled agentDone")
	}
}
