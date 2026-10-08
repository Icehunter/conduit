package copilot

import (
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/api"
)

func userText(text string) api.Message {
	return api.Message{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: text}}}
}

func toolResults(ids ...string) api.Message {
	m := api.Message{Role: "user"}
	for _, id := range ids {
		m.Content = append(m.Content, api.ContentBlock{Type: "tool_result", ToolUseID: id, ResultContent: "ok"})
	}
	return m
}

func TestInitiator(t *testing.T) {
	assistant := api.Message{Role: "assistant", Content: []api.ContentBlock{{Type: "tool_use", ID: "c1", Name: "Read"}}}
	tests := []struct {
		name     string
		class    string // "" means no hints
		userTurn bool
		msgs     []api.Message
		want     string
	}{
		{"first prompt", "main", true, []api.Message{userText("fix the bug")}, InitiatorUser},
		{"steering after tools", "main", true, []api.Message{userText("a"), assistant, toolResults("c1"), userText("stop, use grep")}, InitiatorUser},
		{"tool follow-up", "main", false, []api.Message{userText("a"), assistant, toolResults("c1")}, InitiatorAgent},
		{"injected team message looks like typed text", "main", false, []api.Message{userText("a"), assistant, toolResults("c1"),
			userText("<team-completion from=\"w\">done</team-completion>")}, InitiatorAgent},
		{"user turn flag but only tool results", "main", true, []api.Message{userText("a"), assistant, toolResults("c1")}, InitiatorAgent},
		{"user turn flag but only a reminder", "main", true, []api.Message{userText("a"), assistant,
			userText("<system-reminder>\nuse rg\n</system-reminder>")}, InitiatorAgent},
		{"image only prompt", "main", true, []api.Message{{Role: "user", Content: []api.ContentBlock{{Type: "image"}}}}, InitiatorUser},
		{"user turn but last message from assistant", "main", true, []api.Message{userText("a"), assistant}, InitiatorAgent},
		{"no messages", "main", true, nil, InitiatorAgent},
		{"sub-agent", "subagent", true, []api.Message{userText("task")}, InitiatorAgent},
		{"workflow", "workflow", true, []api.Message{userText("task")}, InitiatorAgent},
		{"compaction", "compaction", true, []api.Message{userText("summarize")}, InitiatorAgent},
		{"auxiliary", "auxiliary", true, []api.Message{userText("search")}, InitiatorAgent},
		{"no hints", "", false, []api.Message{userText("title this")}, InitiatorAgent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &api.MessageRequest{Messages: tt.msgs}
			if tt.class != "" {
				req.Hints = &api.WireHints{RequestClass: tt.class, UserTurn: tt.userTurn}
			}
			if got := Initiator(req); got != tt.want {
				t.Errorf("Initiator = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestStaticHeadersCarryNoInitiator: x-initiator is per request now; a static
// "user" would bill every tool follow-up as a premium request.
func TestStaticHeadersCarryNoInitiator(t *testing.T) {
	for name, headers := range map[string]map[string]string{"chat": ChatHeaders(), "messages": MessagesHeaders()} {
		for k := range headers {
			if strings.EqualFold(k, "x-initiator") || strings.EqualFold(k, "X-Interaction-Id") {
				t.Errorf("%s headers carry static %s", name, k)
			}
		}
		if headers["X-GitHub-Api-Version"] != "2023-07-07" {
			t.Errorf("%s X-GitHub-Api-Version = %q, want 2023-07-07", name, headers["X-GitHub-Api-Version"])
		}
	}
}
