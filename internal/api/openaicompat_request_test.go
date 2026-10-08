package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOpenAIMessages_ToolResultsComeFirst(t *testing.T) {
	msgs := openAIMessagesFromMessage(Message{Role: "user", Content: []ContentBlock{
		{Type: "text", Text: "also look at main.go"},
		{Type: "tool_result", ToolUseID: "c1", ResultContent: "ok"},
		{Type: "tool_result", ToolUseID: "c2"},
		{Type: "tool_result", ToolUseID: "c3", ResultBlocks: []ContentBlock{{Type: "image"}, {Type: "text", Text: "caption"}}},
	}}, reasoningPolicy{}, Config{}, "m")
	want := []openAIChatMessage{
		{Role: "tool", Content: "ok", ToolCallID: "c1"},
		{Role: "tool", Content: "(no output)", ToolCallID: "c2"},
		{Role: "tool", Content: "caption", ToolCallID: "c3"},
		{Role: "user", Content: "also look at main.go"},
	}
	if len(msgs) != len(want) {
		t.Fatalf("messages = %+v, want %+v", msgs, want)
	}
	for i := range want {
		if msgs[i].Role != want[i].Role || msgs[i].Content != want[i].Content || msgs[i].ToolCallID != want[i].ToolCallID {
			t.Errorf("message %d = %+v, want %+v", i, msgs[i], want[i])
		}
	}
}

// TestSystemText_KeepsAgentPromptAndInstructions: only the billing header and
// the exact Claude Code identity blocks are dropped; the agent prompt and
// CLAUDE.md instructions reach the model even though they mention both.
func TestSystemText_KeepsAgentPromptAndInstructions(t *testing.T) {
	blocks := []SystemBlock{
		{Text: "x-anthropic-billing-header: cc_version=2.1.292.abc; cc_entrypoint=sdk-cli; cch=00000;\n"},
		{Text: "You are a Claude agent, built on Anthropic's Claude Agent SDK."},
		{Text: "You are Claude Code, Anthropic's official CLI for Claude."},
		{Text: "\nYou are an interactive agent.\n - Do not duplicate what is already in CLAUDE.md or the diff.\n"},
		{Text: "Codebase and user instructions are shown below.\n\nContents of /repo/CLAUDE.md:\n\nAlways run make verify."},
		{Text: "You are Claude Code, an AI assistant that orchestrates software engineering tasks across multiple workers."},
	}
	got := systemText("qwen3", blocks)
	for _, keep := range []string{"You are an interactive agent.", "Contents of /repo/CLAUDE.md", "Always run make verify.", "orchestrates software engineering tasks"} {
		if !strings.Contains(got, keep) {
			t.Errorf("system text lacks %q", keep)
		}
	}
	for _, drop := range []string{"x-anthropic-billing-header", "built on Anthropic's Claude Agent SDK", "Anthropic's official CLI"} {
		if strings.Contains(got, drop) {
			t.Errorf("system text leaks %q", drop)
		}
	}
	if !strings.HasPrefix(got, "You are Conduit") {
		t.Errorf("system text = %q, want the Conduit identity first", got)
	}
}

func TestReasoningEffort(t *testing.T) {
	tests := []struct {
		budget int
		want   string
	}{
		{0, ""}, {1, "low"}, {1000, "low"}, {1999, "low"}, {2000, "medium"}, {7999, "medium"}, {8000, "high"}, {16000, "high"},
	}
	for _, tt := range tests {
		var cfg *ThinkingConfig
		if tt.budget > 0 {
			cfg = &ThinkingConfig{Type: "enabled", BudgetTokens: tt.budget}
		}
		if got := reasoningEffort(cfg); got != tt.want {
			t.Errorf("reasoningEffort(%d) = %q, want %q", tt.budget, got, tt.want)
		}
	}
}

func TestOpenAIChatBody_ReasoningEffortAndExtraBody(t *testing.T) {
	high := &ThinkingConfig{Type: "enabled", BudgetTokens: 8000}
	tests := []struct {
		name     string
		thinking *ThinkingConfig
		extra    map[string]any
		want     any // reasoning_effort in the body; nil means absent
	}{
		{"thinking off", nil, nil, nil},
		{"mapped", high, nil, "high"},
		{"extra body value wins", high, map[string]any{"reasoning_effort": "minimal"}, "minimal"},
		{"extra body null deletes", high, map[string]any{"reasoning_effort": nil}, nil},
		{"chat_template_kwargs disables thinking", high, map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}}, nil},
		{"top-level enable_thinking false", high, map[string]any{"enable_thinking": false}, nil},
		{"thinking.type disabled", high, map[string]any{"thinking": map[string]any{"type": "disabled"}}, nil},
		{"reasoning.enabled false", high, map[string]any{"reasoning": map[string]any{"enabled": false}}, nil},
		{"enable_thinking true keeps effort", high, map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": true}}, "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient(Config{ProviderKind: ProviderKindOpenAICompatible, BaseURL: "https://api.example.com/v1", ExtraBody: tt.extra}, nil)
			raw, err := c.openAIChatBody(&MessageRequest{Model: "gpt-5", Thinking: tt.thinking}, true)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if got, ok := body["reasoning_effort"]; tt.want == nil && ok || tt.want != nil && got != tt.want {
				t.Errorf("reasoning_effort = %v (present %v), want %v", got, ok, tt.want)
			}
		})
	}
}

func TestMergePatch(t *testing.T) {
	target := map[string]any{
		"model":                "m",
		"stream_options":       map[string]any{"include_usage": true},
		"chat_template_kwargs": map[string]any{"preserve_thinking": true, "keep": 1},
		"tools":                []any{"t"},
	}
	patch := map[string]any{
		"chat_template_kwargs": map[string]any{"enable_thinking": false, "keep": nil},
		"stream_options":       nil,
		"tools":                map[string]any{"replaced": true},
		"top_k":                20.0,
		"new":                  map[string]any{"a": 1, "gone": nil},
	}
	patchCopy, _ := json.Marshal(patch)
	got := mergePatch(target, patch)
	raw, _ := json.Marshal(got)
	want := `{"chat_template_kwargs":{"enable_thinking":false,"preserve_thinking":true},"model":"m","new":{"a":1},"tools":{"replaced":true},"top_k":20}`
	if string(raw) != want {
		t.Errorf("merged = %s\nwant     %s", raw, want)
	}
	if after, _ := json.Marshal(patch); string(after) != string(patchCopy) {
		t.Errorf("patch modified: %s, was %s", after, patchCopy)
	}
}

func TestReasoningPolicy(t *testing.T) {
	tests := []struct {
		name     string
		mode     string
		baseURL  string
		model    string
		tool     bool
		all      bool
		preserve bool
	}{
		{"auto cloud generic", "", "https://api.openai.com/v1", "gpt-5", false, false, false},
		{"auto localhost", "", "http://localhost:8080/v1", "qwen3.8-27b", true, true, true},
		{"auto loopback ip", "auto", "http://127.0.0.1:1234/v1", "llama-3", true, true, false},
		{"auto LAN", "", "http://192.168.0.71:8080/v1", "Qwen/Qwen3-32B", true, true, true},
		{"auto 10/8", "", "http://10.1.2.3/v1", "m", true, true, false},
		{"auto mdns", "", "http://box.local:8000/v1", "m", true, true, false},
		{"auto public IP", "", "http://8.8.8.8/v1", "qwen3", false, false, false},
		{"auto deepseek cloud", "", "https://api.deepseek.com", "deepseek-reasoner", true, true, false},
		{"auto mimo", "", "https://api.example.com", "MiMo-7B-RL", true, true, false},
		{"auto kimi tool turns", "", "https://api.moonshot.ai/v1", "kimi-k2-thinking", true, false, false},
		{"auto openrouter tool turns", "", "https://openrouter.ai/api/v1", "z-ai/glm-4.6", true, false, false},
		{"auto claude never", "", "https://openrouter.ai/api/v1", "anthropic/claude-sonnet-4.5", false, false, false},
		{"auto claude local never", "", "http://localhost:4000", "claude-proxy", false, false, false},
		{"on", "on", "https://api.example.com", "qwen3", true, true, true},
		{"off local", "off", "http://localhost:8080", "qwen3", false, false, false},
		{"unknown is auto", "sometimes", "http://localhost:8080", "m", true, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClient(Config{ProviderKind: ProviderKindOpenAICompatible, BaseURL: tt.baseURL, ReasoningReplay: tt.mode}, nil)
			p := c.reasoningPolicy(tt.model)
			if p.toolTurns != tt.tool || p.allTurns != tt.all || p.preserveThinking != tt.preserve {
				t.Errorf("policy = %+v, want toolTurns=%v allTurns=%v preserveThinking=%v", p, tt.tool, tt.all, tt.preserve)
			}
		})
	}
}

func TestOpenAIMessages_ReasoningReplay(t *testing.T) {
	thinking := ContentBlock{Type: "thinking", Thinking: "think hard"}
	signed := ContentBlock{Type: "thinking", Thinking: "claude thought", Signature: "sig"}
	call := ContentBlock{Type: "tool_use", ID: "c1", Name: "Read", Input: map[string]any{}}
	text := ContentBlock{Type: "text", Text: "answer"}
	tests := []struct {
		name      string
		policy    reasoningPolicy
		content   []ContentBlock
		wantField string
		want      string
	}{
		{"off", reasoningPolicy{field: "reasoning_content"}, []ContentBlock{thinking, call}, "", ""},
		{"tool turn", reasoningPolicy{toolTurns: true, field: "reasoning_content"}, []ContentBlock{thinking, call}, "reasoning_content", "think hard"},
		{"tool policy skips plain turn", reasoningPolicy{toolTurns: true, field: "reasoning_content"}, []ContentBlock{thinking, text}, "", ""},
		{"all turns", reasoningPolicy{allTurns: true, field: "reasoning_content"}, []ContentBlock{thinking, text}, "reasoning_content", "think hard"},
		{"reasoning field", reasoningPolicy{allTurns: true, field: "reasoning"}, []ContentBlock{thinking, text}, "reasoning", "think hard"},
		{"signed thinking never replays", reasoningPolicy{allTurns: true, toolTurns: true, field: "reasoning_content"}, []ContentBlock{signed, call}, "", ""},
		{"thinking-only turn is left out", reasoningPolicy{allTurns: true, field: "reasoning_content"}, []ContentBlock{thinking}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := openAIMessagesFromMessage(Message{Role: "assistant", Content: tt.content}, tt.policy, Config{}, "m")
			if strings.HasSuffix(tt.name, "left out") {
				if len(msgs) != 0 {
					t.Fatalf("messages = %+v, want none (llama.cpp rejects an assistant message without content or tool_calls)", msgs)
				}
				return
			}
			if len(msgs) == 0 {
				t.Fatal("no assistant message")
			}
			raw, _ := json.Marshal(msgs[0])
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			for _, field := range []string{"reasoning_content", "reasoning"} {
				got, ok := m[field]
				if field == tt.wantField {
					if got != tt.want {
						t.Errorf("%s = %v, want %q", field, got, tt.want)
					}
				} else if ok {
					t.Errorf("unexpected %s = %v in %s", field, got, raw)
				}
			}
		})
	}
}

// TestOpenAIChatBody_LocalQwen: a Qwen model on a LAN server replays plain
// turns' reasoning and asks the template to keep it, merged with ExtraBody's
// own chat_template_kwargs instead of being replaced by them.
func TestOpenAIChatBody_LocalQwen(t *testing.T) {
	c := NewClient(Config{
		ProviderKind: ProviderKindOpenAICompatible,
		BaseURL:      "http://192.168.0.71:8080/v1",
		ExtraBody:    map[string]any{"chat_template_kwargs": map[string]any{"enable_thinking": false}},
	}, nil)
	raw, err := c.openAIChatBody(&MessageRequest{
		Model: "qwen3.8-27b",
		Messages: []Message{
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
			{Role: "assistant", Content: []ContentBlock{{Type: "thinking", Thinking: "greet back"}, {Type: "text", Text: "hello"}}},
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "again"}}},
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		ChatTemplateKwargs map[string]any      `json:"chat_template_kwargs"`
		Messages           []openAIChatMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.ChatTemplateKwargs["preserve_thinking"] != true || body.ChatTemplateKwargs["enable_thinking"] != false {
		t.Errorf("chat_template_kwargs = %v, want preserve_thinking and ExtraBody's enable_thinking", body.ChatTemplateKwargs)
	}
	if len(body.Messages) != 3 || body.Messages[1].ReasoningContent != "greet back" {
		t.Errorf("messages = %+v, want the assistant turn's reasoning_content", body.Messages)
	}
}

func TestDecodeOpenAIError(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		header     http.Header
		body       string
		wantType   string
		wantMsg    string
		wantText   string
		wantRetry  time.Duration
		wantClass  ErrorClass
		wantReason string
	}{
		{"openai overflow", 400, nil,
			`{"error":{"message":"This model's maximum context length is 128000 tokens.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`,
			"invalid_request_error", "context_length_exceeded: This model's maximum context length is 128000 tokens.",
			"api: 400 Bad Request: invalid_request_error: context_length_exceeded: This model's", 0, ClassContextOverflow, ReasonPromptTooLong},
		{"numeric code rate limit", 429, http.Header{"Retry-After": {"7"}},
			`{"error":{"code":429,"message":"Rate limit exceeded: free-models-per-min"}}`,
			"", "Rate limit exceeded: free-models-per-min", "api: 429 Too Many Requests: Rate limit exceeded: free-models-per-min [retry-after=7s]", 7 * time.Second, ClassRateLimited, ReasonRateLimit},
		{"llama.cpp n_ctx 500", 500, nil,
			`{"error":{"code":500,"message":"the request exceeds the available context size, try increasing it","type":"exceed_context_size_error"}}`,
			"exceed_context_size_error", "the request exceeds the available context size, try increasing it", "api: 500 Internal Server Error: exceed_context_size_error: the request", 0, ClassContextOverflow, ReasonPromptTooLong},
		{"bare string error", 404, nil, `{"error":"model 'qwen9' not found"}`,
			"", "model 'qwen9' not found", "api: 404 Not Found: model 'qwen9' not found", 0, ClassInvalidRequest, ReasonModelNotFound},
		{"codex plan gate", 403, nil, `{"error":{"code":"usage_not_included","message":"nope"}}`,
			"usage_not_included", "To use Codex with your ChatGPT plan, upgrade to Plus: https://chatgpt.com/explore/plus", "upgrade to Plus", 0, ClassAuth, ReasonAuth},
		{"not json", 502, nil, "<html>bad gateway</html>",
			"", "<html>bad gateway</html>", "api: 502 Bad Gateway: <html>bad gateway</html>", 0, ClassTransient, ReasonServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tt.status,
				Status:     http.StatusText(tt.status),
				Header:     tt.header,
				Body:       io.NopCloser(strings.NewReader(tt.body)),
			}
			if resp.Header == nil {
				resp.Header = http.Header{}
			}
			err := (&Client{}).decodeOpenAIError(resp)
			var se *APIStatusError
			if !errors.As(err, &se) {
				t.Fatalf("err = %T %v, want *APIStatusError", err, err)
			}
			if se.Status != tt.status || se.Type != tt.wantType || se.Message != tt.wantMsg {
				t.Errorf("got {%d %q %q}, want {%d %q %q}", se.Status, se.Type, se.Message, tt.status, tt.wantType, tt.wantMsg)
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Errorf("Error() = %q, want it to contain %q", err.Error(), tt.wantText)
			}
			if se.RetryAfter != tt.wantRetry {
				t.Errorf("RetryAfter = %v, want %v", se.RetryAfter, tt.wantRetry)
			}
			if c := Classify(err); c.Class != tt.wantClass || c.Reason != tt.wantReason {
				t.Errorf("Classify = %v/%s, want %v/%s", c.Class, c.Reason, tt.wantClass, tt.wantReason)
			}
		})
	}
}
