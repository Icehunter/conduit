package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/profile"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tools/todowritetool"
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

func TestHandleAgentDoneQueuesUnfinishedTodoContinuation(t *testing.T) {
	cleanup := seedTodos(t, []todowritetool.Todo{
		{ID: "1", Content: "finish the reconnect fix", Status: todowritetool.StatusInProgress},
		{ID: "2", Content: "verify usage footer", Status: todowritetool.StatusPending},
	})
	defer cleanup()

	m := idleModel()
	m.turnID = 1
	m.running = true
	m.history = []api.Message{{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "fix bugs"}}}}

	m2, _ := m.handleAgentDone(agentDoneMsg{
		turnID:  1,
		history: append([]api.Message(nil), m.history...),
	})

	if got := m2.input.Value(); !strings.HasPrefix(got, autoPromptPrefix) {
		t.Fatalf("unexpected continuation prompt: %q", got)
	}
	if m2.todoAutoContinues != 1 {
		t.Fatalf("todoAutoContinues = %d; want 1", m2.todoAutoContinues)
	}
}

func TestApplyAPIRateLimitToPlanUsageMarksCurrentSessionFull(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))
	if err := settings.SaveConduitRawKey("accounts", map[string]any{
		"active": "claude-ai:work@example.com",
		"accounts": map[string]any{
			"claude-ai:work@example.com": map[string]any{
				"email": "work@example.com",
				"kind":  "claude-ai",
			},
		},
	}); err != nil {
		t.Fatalf("save accounts: %v", err)
	}

	resetAt := time.Now().Add(90 * time.Minute).UTC().Truncate(time.Second)
	m := Model{
		cfg:                Config{Profile: profile.Info{Email: "work@example.com"}},
		usageStatusEnabled: true,
		activeProvider: &settings.ActiveProviderSettings{
			Kind:    settings.ProviderKindClaudeSubscription,
			Account: "work@example.com",
			Model:   "claude-sonnet-4-6",
		},
	}

	m = m.applyAgentEvent(agent.LoopEvent{
		Type:             agent.EventAPIRetry,
		RetryDelay:       time.Minute,
		RetryAfter:       time.Minute,
		RateLimitResetAt: resetAt,
		RetryErr:         context.DeadlineExceeded,
	})

	if got := m.planUsage.FiveHour.Utilization; got != 100 {
		t.Fatalf("five-hour utilization = %v; want 100", got)
	}
	if !m.planUsage.FiveHour.ResetsAt.Equal(resetAt) {
		t.Fatalf("reset = %s; want %s", m.planUsage.FiveHour.ResetsAt, resetAt)
	}
}
