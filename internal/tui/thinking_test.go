package tui

import (
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
)

func thinkingEv(s string) agent.LoopEvent { return agent.LoopEvent{Type: agent.EventThinking, Text: s} }
func textEv(s string) agent.LoopEvent     { return agent.LoopEvent{Type: agent.EventText, Text: s} }

func TestThinking_NeverEntersAnswer(t *testing.T) {
	m := idleModel()
	m.running = true
	m = m.applyAgentEvent(thinkingEv("secret reasoning"))
	if m.streaming != "" {
		t.Fatalf("streaming = %q, want empty", m.streaming)
	}
	m = m.applyAgentEvent(textEv("answer"))
	if m.streaming != "answer" {
		t.Errorf("streaming = %q, want %q", m.streaming, "answer")
	}
}

func TestThinking_RowOrderAndTrigger(t *testing.T) {
	tests := []struct {
		name string
		next agent.LoopEvent
	}{
		{"text starts", textEv("hi")},
		{"tool starts", agent.LoopEvent{Type: agent.EventToolStart, ToolName: "Bash", ToolID: "t1"}},
		{"tool use without start", agent.LoopEvent{Type: agent.EventToolUse, ToolName: "Bash", ToolID: "t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := idleModel()
			m.running = true
			before := len(m.messages)
			m = m.applyAgentEvent(thinkingEv("a\nb"))
			if len(m.messages) != before {
				t.Fatalf("row added while still thinking")
			}
			m = m.applyAgentEvent(tt.next)
			if len(m.messages) <= before || m.messages[before].Role != RoleThinking {
				t.Fatalf("messages[%d] = %+v, want RoleThinking first", before, m.messages[before:])
			}
			if m.messages[before].Content != "a\nb" || m.thinking != "" {
				t.Errorf("content = %q thinking = %q", m.messages[before].Content, m.thinking)
			}
		})
	}
}

func TestThinking_BlankProducesNothing(t *testing.T) {
	m := idleModel()
	m.running = true
	before := len(m.messages)
	m = m.applyAgentEvent(thinkingEv("  \n "))
	m = m.applyAgentEvent(textEv("answer"))
	for _, msg := range m.messages[before:] {
		if msg.Role == RoleThinking {
			t.Fatalf("blank thinking produced a row: %+v", msg)
		}
	}
}

func TestThinking_RetryDropsPending(t *testing.T) {
	m := idleModel()
	m.running = true
	m = m.applyAgentEvent(thinkingEv("x"))
	m = m.applyAgentEvent(agent.LoopEvent{Type: agent.EventAPIRetry, RetryStream: true})
	if m.thinking != "" {
		t.Errorf("thinking = %q, want cleared", m.thinking)
	}
}

func TestRenderThinking(t *testing.T) {
	tests := []struct {
		name     string
		msg      Message
		want     []string
		wantNone []string
	}{
		{"collapsed multi", Message{Role: RoleThinking, Content: "one\ntwo\nthree"},
			[]string{"Thinking… (3 lines) [click to expand]"}, []string{"two", "three"}},
		{"collapsed single", Message{Role: RoleThinking, Content: "one"},
			[]string{"Thinking… (1 line) [click to expand]"}, nil},
		{"expanded", Message{Role: RoleThinking, Content: "one\ntwo", ToolExpanded: true},
			[]string{"[click to collapse]", "one", "two"}, []string{"[click to expand]"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := renderMessage(tt.msg, 80, false)
			for _, w := range tt.want {
				if !strings.Contains(out, w) {
					t.Errorf("missing %q in %q", w, out)
				}
			}
			for _, w := range tt.wantNone {
				if strings.Contains(out, w) {
					t.Errorf("unexpected %q in %q", w, out)
				}
			}
		})
	}
	if out := renderMessage(Message{Role: RoleThinking, Content: " "}, 80, false); out != "" {
		t.Errorf("blank thinking rendered %q", out)
	}
}

func TestHistoryToDisplayMessages_Thinking(t *testing.T) {
	msgs := []api.Message{{Role: "assistant", Content: []api.ContentBlock{
		{Type: "thinking", Thinking: "why", Signature: "s"},
		{Type: "thinking", Thinking: "", Signature: "redacted"},
		{Type: "text", Text: "because"},
	}}}
	out := historyToDisplayMessages(msgs)
	if len(out) != 2 || out[0].Role != RoleThinking || out[0].Content != "why" || out[1].Role != RoleAssistant {
		t.Fatalf("out = %+v", out)
	}
}

func TestThinking_LateDeltaAfterCancelIgnored(t *testing.T) {
	m := idleModel()
	m.running = true
	m = m.applyAgentEvent(thinkingEv("first"))
	m.running = false // ctrl+c / esc
	m.thinking = ""
	m = m.applyAgentEvent(thinkingEv("late"))
	if m.thinking != "" {
		t.Fatalf("thinking = %q after cancel, want empty", m.thinking)
	}
	m.running = true
	m = m.applyAgentEvent(textEv("answer"))
	for _, msg := range m.messages {
		if msg.Role == RoleThinking {
			t.Fatalf("late thinking leaked into next turn: %+v", msg)
		}
	}
}

func TestThinking_AfterTextKeepsOrder(t *testing.T) {
	m := idleModel()
	m.running = true
	start := len(m.messages)
	m = m.applyAgentEvent(textEv("one"))
	m = m.applyAgentEvent(thinkingEv("hmm"))
	m = m.applyAgentEvent(textEv("two"))
	got := m.messages[start:]
	if len(got) != 2 || got[0].Role != RoleAssistant || got[0].Content != "one" || got[1].Role != RoleThinking {
		t.Fatalf("order = %+v", got)
	}
	if m.streaming != "two" {
		t.Errorf("streaming = %q", m.streaming)
	}
}
