package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/profile"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
)

func TestRetryStatusText(t *testing.T) {
	tests := []struct {
		name string
		ev   agent.LoopEvent
		want string
	}{
		{"rate limit", agent.LoopEvent{RetryReason: api.ReasonRateLimit, RetryDelay: 8 * time.Second, RetryAttempt: 2, RetryMax: 5}, "Rate limited · retrying in 8s (2/5)"},
		{"overloaded", agent.LoopEvent{RetryReason: api.ReasonOverloaded, RetryDelay: 2 * time.Second, RetryAttempt: 1, RetryMax: 5}, "API overloaded · retrying in 2s (1/5)"},
		{"server error with status", agent.LoopEvent{RetryReason: api.ReasonServerError, RetryStatus: 502, RetryDelay: time.Second, RetryAttempt: 1, RetryMax: 5}, "Server error (502) · retrying in 1s (1/5)"},
		{"server error without status", agent.LoopEvent{RetryReason: api.ReasonServerError, RetryDelay: time.Second}, "Server error · retrying in 1s"},
		{"network", agent.LoopEvent{RetryReason: api.ReasonNetwork, RetryDelay: 4 * time.Second, RetryAttempt: 3, RetryMax: 5}, "Connection error · retrying in 4s (3/5)"},
		{"idle timeout", agent.LoopEvent{RetryReason: api.ReasonIdleTimeout, RetryDelay: time.Second, RetryAttempt: 1, RetryMax: 3, RetryStream: true}, "Stream stalled · retrying in 1s (1/3)"},
		{"truncated", agent.LoopEvent{RetryReason: api.ReasonStreamTruncated, RetryDelay: time.Second, RetryAttempt: 2, RetryMax: 3}, "Response interrupted · retrying in 1s (2/3)"},
		{"sub-second delay rounds up", agent.LoopEvent{RetryReason: api.ReasonRateLimit, RetryDelay: 200 * time.Millisecond}, "Rate limited · retrying in 1s"},
		{"repair has no delay", agent.LoopEvent{RetryReason: api.ReasonToolFlow, RetryAttempt: 1, RetryMax: 2}, "Repairing conversation history · retrying (1/2)"},
		{"unknown reason", agent.LoopEvent{RetryDelay: 3 * time.Second}, "Request failed · retrying in 3s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := retryStatusText(tt.ev); got != tt.want {
				t.Errorf("retryStatusText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A retried stream restarts from the top, so the failed attempt's text must
// not stay in the live buffer where the retry would append to it.
func TestApplyAgentEvent_RetryStreamResetsStreaming(t *testing.T) {
	tests := []struct {
		name        string
		retryStream bool
		want        string
	}{
		{"stream retry drops the partial text", true, ""},
		{"request retry keeps it", false, "partial answer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := idleModel()
			m.streaming = "partial answer"
			m = m.applyAgentEvent(agent.LoopEvent{
				Type:        agent.EventAPIRetry,
				RetryReason: api.ReasonStreamTruncated,
				RetryDelay:  time.Second,
				RetryStream: tt.retryStream,
			})
			if m.streaming != tt.want {
				t.Errorf("streaming = %q, want %q", m.streaming, tt.want)
			}
			if m.apiRetryStatus == "" {
				t.Error("apiRetryStatus not set")
			}
		})
	}
}

func TestApplyAgentEvent_RetryPlanUsageOnlyOnRateLimit(t *testing.T) {
	setup := func(t *testing.T) Model {
		t.Helper()
		t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(t.TempDir(), ".conduit"))
		if err := settings.SaveConduitRawKey("accounts", map[string]any{
			"active": "claude-ai:work@example.com",
			"accounts": map[string]any{
				"claude-ai:work@example.com": map[string]any{"email": "work@example.com", "kind": "claude-ai"},
			},
		}); err != nil {
			t.Fatalf("save accounts: %v", err)
		}
		m := idleModel()
		m.cfg.Profile = profile.Info{Email: "work@example.com"}
		m.usageStatusEnabled = true
		m.providers = map[string]settings.ActiveProviderSettings{
			"claude-subscription.work@example.com.claude-sonnet-4-6": {Kind: settings.ProviderKindClaudeSubscription, Account: "work@example.com", Model: "claude-sonnet-4-6"},
		}
		m.roles = map[string]string{settings.RoleDefault: "claude-subscription.work@example.com.claude-sonnet-4-6"}
		return m
	}
	tests := []struct {
		name   string
		reason string
		want   float64
	}{
		{"rate limit fills the meter", api.ReasonRateLimit, 100},
		{"server error with retry-after leaves it", api.ReasonServerError, 0},
		{"overload leaves it", api.ReasonOverloaded, 0},
		{"network error leaves it", api.ReasonNetwork, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := setup(t).applyAgentEvent(agent.LoopEvent{
				Type:        agent.EventAPIRetry,
				RetryDelay:  time.Minute,
				RetryAfter:  time.Minute,
				RetryErr:    errors.New("api: rate_limit_error"),
				RetryReason: tt.reason,
			})
			if got := m.planUsage.FiveHour.Utilization; got != tt.want {
				t.Errorf("utilization = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyAgentEvent_InfoRows(t *testing.T) {
	tests := []struct {
		name string
		ev   agent.LoopEvent
		want string
	}{
		{"notice", agent.LoopEvent{Type: agent.EventNotice, Notice: "Output limit reached — continuing (1/3)"}, "Output limit reached — continuing (1/3)"},
		{"failover", agent.LoopEvent{Type: agent.EventProviderFailover, FailoverProvider: "openai.gpt-5"}, "Switched provider → openai.gpt-5"},
		{"ttsr", agent.LoopEvent{Type: agent.EventTTSR, TTSRRule: "no-todo"}, `Rule "no-todo" fired`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := idleModel()
			before := len(m.messages)
			m = m.applyAgentEvent(tt.ev)
			if len(m.messages) != before+1 {
				t.Fatalf("messages = %d, want %d", len(m.messages), before+1)
			}
			got := m.messages[len(m.messages)-1]
			if got.Role != RoleSystem || !strings.Contains(got.Content, tt.want) {
				t.Errorf("row = %+v, want a system row containing %q", got, tt.want)
			}
		})
	}
}

// EventPartial used to append the partial assistant message to the session
// file, and the finished turn appended it again from the returned history.
func TestApplyAgentEvent_PartialDoesNotWriteSession(t *testing.T) {
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(t.TempDir(), ".conduit"))
	s, err := session.New(t.TempDir(), "partial-test")
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	m := idleModel()
	m.cfg.Session = s
	m = m.applyAgentEvent(agent.LoopEvent{
		Type:          agent.EventPartial,
		PartialBlocks: []api.ContentBlock{{Type: "text", Text: "half an answer"}},
		PartialErr:    errors.New("boom"),
	})
	if _, err := os.Stat(s.FilePath); err == nil {
		t.Errorf("EventPartial wrote to the session file %s", s.FilePath)
	}
	_ = m
}

func TestFriendlyAgentError(t *testing.T) {
	reset := time.Date(2026, 10, 8, 15, 30, 0, 0, time.Local)
	tests := []struct {
		name     string
		err      error
		contains []string
		exact    string
	}{
		{"auth", fmt.Errorf("agent: stream: %w", &api.APIStatusError{Status: 401, Type: "authentication_error", Message: "bad token"}), []string{"/login"}, ""},
		{"overflow", fmt.Errorf("agent: stream: %w", &api.APIStatusError{Status: 400, Type: "invalid_request_error", Message: "prompt is too long: 250000 tokens > 200000 maximum"}), []string{"/compact", "/clear"}, ""},
		{"rate limit with reset", fmt.Errorf("agent: stream: %w", &api.APIStatusError{Status: 429, Type: "rate_limit_error", Message: "limit", ResetAt: reset}), []string{"resets at 15:30"}, ""},
		{"rate limit with retry-after", fmt.Errorf("agent: stream: %w", &api.APIStatusError{Status: 429, Type: "rate_limit_error", Message: "limit", RetryAfter: 90 * time.Second}), []string{"Try again in 1m30s"}, ""},
		{"plain error untouched", errors.New("agent: something else"), nil, "agent: something else"},
		{"server error untouched", &api.APIStatusError{Status: 500, Message: "oops"}, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := friendlyAgentError(tt.err)
			if !strings.HasPrefix(got, tt.err.Error()) {
				t.Errorf("friendlyAgentError() = %q, want it to start with the error text", got)
			}
			for _, c := range tt.contains {
				if !strings.Contains(got, c) {
					t.Errorf("friendlyAgentError() = %q, missing %q", got, c)
				}
			}
			if tt.exact != "" && got != tt.exact {
				t.Errorf("friendlyAgentError() = %q, want %q", got, tt.exact)
			}
			if len(tt.contains) == 0 && got != tt.err.Error() {
				t.Errorf("friendlyAgentError() = %q, want the bare error", got)
			}
		})
	}
}

func TestHandleAgentDone_RefusalAddsNoErrorRow(t *testing.T) {
	m := idleModel()
	m.turnID = 1
	m.running = true
	m.history = []api.Message{{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "do the thing"}}}}
	before := len(m.messages)

	m2, _ := m.handleAgentDone(agentDoneMsg{turnID: 1, history: m.history, err: fmt.Errorf("wrapped: %w", agent.ErrModelRefusal)})
	for _, msg := range m2.messages[before:] {
		if msg.Role == RoleError {
			t.Errorf("refusal produced an error row: %+v", msg)
		}
	}
	if len(m2.history) != 0 {
		t.Errorf("history = %+v; want the refused prompt dropped", m2.history)
	}
}

func TestIsCancelError_IdleTimeoutIsNot(t *testing.T) {
	idle := fmt.Errorf("agent: drain: %w", fmt.Errorf("stream: %w: %w", api.ErrStreamIdleTimeout, context.Canceled))
	if isCancelError(idle) {
		t.Error("an exhausted idle-timeout error was read as a user cancel")
	}
	if !isCancelError(context.Canceled) {
		t.Error("context.Canceled must still be a cancel")
	}
}

func TestApplyAgentEvent_FailoverAndTTSRResetStreaming(t *testing.T) {
	for _, ev := range []agent.LoopEvent{
		{Type: agent.EventProviderFailover, FailoverProvider: "b"},
		{Type: agent.EventTTSR, TTSRRule: "r"},
	} {
		m := idleModel()
		m = m.applyAgentEvent(agent.LoopEvent{Type: agent.EventText, Text: "failed attempt "})
		m = m.applyAgentEvent(ev)
		m = m.applyAgentEvent(agent.LoopEvent{Type: agent.EventText, Text: "retry"})
		if m.streaming != "retry" {
			t.Errorf("event %d: streaming = %q, want \"retry\"", ev.Type, m.streaming)
		}
	}
}

// A retried stream restarts the response: the text rows the failed attempt
// committed are dropped, its closed tool rows stay, and rows from earlier
// finished tool rounds are never touched.
func TestRetryStreamDropsFailedAttemptRows(t *testing.T) {
	roles := func(m Model) string {
		var sb strings.Builder
		for _, row := range m.messages {
			switch row.Role {
			case RoleUser:
				sb.WriteString("U")
			case RoleAssistant:
				sb.WriteString("A")
			case RoleTool:
				sb.WriteString("T")
			default:
				sb.WriteString("?")
			}
		}
		return sb.String()
	}
	retry := agent.LoopEvent{Type: agent.EventAPIRetry, RetryStream: true, RetryReason: api.ReasonNetwork}
	dangling := agent.LoopEvent{Type: agent.EventToolResult, ToolID: "t2", IsError: true, ResultText: agent.DanglingToolMessage}

	t.Run("first response of the turn", func(t *testing.T) {
		m := idleModel()
		m.messages = []Message{{Role: RoleUser, Content: "go"}}
		for _, ev := range []agent.LoopEvent{
			{Type: agent.EventText, Text: "I will run it"},
			{Type: agent.EventToolStart, ToolID: "t1", ToolName: "Bash"},
			dangling, retry,
		} {
			m = m.applyAgentEvent(ev)
		}
		if got := roles(m); got != "UT" {
			t.Errorf("rows = %s, want UT (text dropped, closed tool row kept)", got)
		}
	})
	t.Run("after a finished tool round", func(t *testing.T) {
		m := idleModel()
		m.messages = []Message{{Role: RoleUser, Content: "go"}}
		for _, ev := range []agent.LoopEvent{
			{Type: agent.EventText, Text: "round one"},
			{Type: agent.EventToolStart, ToolID: "t1", ToolName: "Bash"},
			{Type: agent.EventToolResult, ToolID: "t1", ResultText: "ok"},
			{Type: agent.EventText, Text: "round two"},
			{Type: agent.EventToolStart, ToolID: "t2", ToolName: "Bash"},
			{Type: agent.EventToolResult, ToolID: "t2", IsError: true, ResultText: agent.DanglingToolMessage},
			retry,
		} {
			m = m.applyAgentEvent(ev)
		}
		if got := roles(m); got != "UATT" {
			t.Errorf("rows = %s, want UATT (round one kept; round two's text dropped)", got)
		}
	})
	t.Run("text only", func(t *testing.T) {
		m := idleModel()
		m.messages = []Message{{Role: RoleUser, Content: "go"}}
		m = m.applyAgentEvent(agent.LoopEvent{Type: agent.EventText, Text: "partial"})
		m = m.applyAgentEvent(retry)
		if m.streaming != "" || roles(m) != "U" {
			t.Errorf("streaming %q rows %s, want nothing carried over", m.streaming, roles(m))
		}
	})
}
