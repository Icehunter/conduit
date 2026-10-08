package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestFormatToolDurations(t *testing.T) {
	tests := []struct {
		name string
		in   []ToolDuration
		want string
	}{
		{"empty", nil, ""},
		{"basic", []ToolDuration{{"Bash", 1234 * time.Millisecond}, {"Read", 7 * time.Millisecond}}, "Bash=1234;Read=7"},
		{"rounds to ms", []ToolDuration{{"Grep", 1500 * time.Microsecond}}, "Grep=2"},
		{"negative clamps to zero", []ToolDuration{{"Glob", -time.Second}}, "Glob=0"},
		{"escapes separators", []ToolDuration{{"a;b=c,d e%", time.Millisecond}}, "a%3Bb%3Dc%2Cd%20e%25=1"},
		{"escapes non-ascii as utf-8 bytes", []ToolDuration{{"mcp__é", time.Millisecond}}, "mcp__%C3%A9=1"},
		{"invalid utf-8 becomes U+FFFD", []ToolDuration{{"x\xffy", time.Millisecond}}, "x%EF%BF%BDy=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatToolDurations(tt.in); got != tt.want {
				t.Errorf("FormatToolDurations() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatToolDurations_Limits(t *testing.T) {
	many := make([]ToolDuration, 40)
	for i := range many {
		many[i] = ToolDuration{"T", time.Millisecond}
	}
	if got := strings.Count(FormatToolDurations(many), ";") + 1; got != maxToolDurationEntries {
		t.Errorf("entries = %d, want %d", got, maxToolDurationEntries)
	}

	long := []ToolDuration{
		{strings.Repeat("a", 3000), time.Millisecond},
		{strings.Repeat("b", 3000), time.Millisecond},
	}
	got := FormatToolDurations(long)
	if len(got) > maxToolDurationsLen || strings.Contains(got, "b") {
		t.Errorf("len = %d, want the second entry dropped to stay within %d", len(got), maxToolDurationsLen)
	}
}

func TestIsFirstPartyBaseURL(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"https://api.anthropic.com", true},
		{"https://api.anthropic.com/", true},
		{"https://proxy.example.com", false},
		{"http://127.0.0.1:8080", false},
		{"https://api.anthropic.com.evil.test", false},
	}
	for _, tt := range tests {
		if got := isFirstPartyBaseURL(tt.in); got != tt.want {
			t.Errorf("isFirstPartyBaseURL(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestHintHeadersEnabled(t *testing.T) {
	tests := []struct {
		name    string
		env     string
		set     bool
		baseURL string
		want    bool
	}{
		{"unset, first party", "", false, "https://api.anthropic.com", true},
		{"unset, proxy", "", false, "http://127.0.0.1:9", false},
		{"forced on for proxy", "1", true, "http://127.0.0.1:9", true},
		{"forced on with yes", "Yes", true, "http://127.0.0.1:9", true},
		{"forced on with on", " ON ", true, "http://127.0.0.1:9", true},
		{"forced off for first party", "false", true, "https://api.anthropic.com", false},
		{"forced off with off", "off", true, "https://api.anthropic.com", false},
		{"forced off with no", "NO", true, "https://api.anthropic.com", false},
		{"unrecognized falls back (proxy)", "maybe", true, "http://127.0.0.1:9", false},
		{"unrecognized falls back (first party)", "maybe", true, "https://api.anthropic.com", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv("CLAUDE_CODE_GATEWAY_HINT_HEADERS", tt.env)
			} else {
				t.Setenv("CLAUDE_CODE_GATEWAY_HINT_HEADERS", "")
				_ = os.Unsetenv("CLAUDE_CODE_GATEWAY_HINT_HEADERS") // t.Setenv restores it after the test
			}
			if got := hintHeadersEnabled(tt.baseURL); got != tt.want {
				t.Errorf("hintHeadersEnabled(%q) = %v, want %v", tt.baseURL, got, tt.want)
			}
		})
	}
}

func TestApplyWireHints(t *testing.T) {
	const promptID = "964702b8-08db-4077-ba04-bfadc6600035"
	hints := &WireHints{
		RequestClass:      "subagent",
		AgentType:         "general-purpose",
		AgentID:           "sub-1",
		ParentAgentID:     "sub-0",
		PromptID:          promptID,
		Compaction:        "auto",
		ContextCompacted:  "manual",
		PrevToolDurations: "Bash=12",
		UserTurn:          true,
		SessionID:         "session-1", // never a CC header; the counts below prove it is not sent
	}

	all := map[string]string{
		"x-claude-code-agent-id":            "sub-1",
		"x-claude-code-parent-agent-id":     "sub-0",
		"x-cc-compaction-request":           "auto",
		"x-cc-context-compacted":            "manual",
		"x-claude-code-request-class":       "subagent",
		"x-claude-code-agent-type":          "general-purpose",
		"x-claude-code-prompt-id":           promptID,
		"x-claude-code-compaction":          "auto",
		"x-claude-code-context-compacted":   "manual",
		"x-claude-code-prev-tool-durations": "Bash=12",
	}
	agentIDs := []string{"x-claude-code-agent-id", "x-claude-code-parent-agent-id"}
	legacy := []string{"x-cc-compaction-request", "x-cc-context-compacted"}
	gated := []string{
		"x-claude-code-request-class", "x-claude-code-agent-type", "x-claude-code-prompt-id",
		"x-claude-code-compaction", "x-claude-code-context-compacted", "x-claude-code-prev-tool-durations",
	}
	gates := []struct {
		name                        string
		firstPartyURL, hintsEnabled bool
		want                        []string
	}{
		// api.anthropic.com, no override: everything (CC yl() and DEn() both true).
		{"first party", true, true, append(append(append([]string{}, agentIDs...), legacy...), gated...)},
		// api.anthropic.com with the env var off: DEn() false, yl() still true.
		{"first party, env off", true, false, append(append([]string{}, agentIDs...), legacy...)},
		// gateway with the env var on: DEn() true, yl() false.
		{"gateway, env on", false, true, append(append([]string{}, agentIDs...), gated...)},
		{"gateway", false, false, agentIDs},
	}
	for _, g := range gates {
		t.Run(g.name, func(t *testing.T) {
			h := http.Header{}
			applyWireHints(h, hints, g.firstPartyURL, g.hintsEnabled)
			if len(h) != len(g.want) {
				t.Errorf("sent %d headers, want %d: %v", len(h), len(g.want), h)
			}
			for _, k := range g.want {
				if got := h.Get(k); got != all[k] {
					t.Errorf("%s = %q, want %q", k, got, all[k])
				}
			}
		})
	}

	t.Run("invalid prompt id omitted", func(t *testing.T) {
		h := http.Header{}
		applyWireHints(h, &WireHints{PromptID: "not-a-uuid"}, true, true)
		if v := h.Get("x-claude-code-prompt-id"); v != "" {
			t.Errorf("prompt id = %q, want omitted", v)
		}
	})

	t.Run("agent id escaped", func(t *testing.T) {
		h := http.Header{}
		applyWireHints(h, &WireHints{AgentID: "a b%\n"}, false, false)
		if got := h.Get("x-claude-code-agent-id"); got != "a b%25%0A" {
			t.Errorf("agent id = %q, want %q", got, "a b%25%0A")
		}
	})

	t.Run("nil hints", func(t *testing.T) {
		h := http.Header{}
		applyWireHints(h, nil, true, true)
		if len(h) != 0 {
			t.Errorf("headers = %v, want none", h)
		}
	})
}

// TestStreamMessage_SendsWireHints checks req.Hints reaches the wire. The
// test server is not api.anthropic.com, so only the ungated agent id header
// is expected; the first-party set is covered by TestApplyWireHints.
func TestStreamMessage_SendsWireHints(t *testing.T) {
	t.Setenv("CLAUDE_CODE_GATEWAY_HINT_HEADERS", "0")
	gotAgentID := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		gotAgentID <- r.Header.Get("x-claude-code-agent-id")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	stream, err := c.StreamMessage(context.Background(), &MessageRequest{
		Model: "m", MaxTokens: 1,
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
		Hints:    &WireHints{AgentID: "sub-7", RequestClass: "subagent"},
	})
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	_ = stream.Close()
	if got := <-gotAgentID; got != "sub-7" {
		t.Errorf("x-claude-code-agent-id = %q, want %q", got, "sub-7")
	}
}
