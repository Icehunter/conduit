package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/sse"
)

func TestReasoningEffortGate(t *testing.T) {
	tests := []struct {
		baseURL, model string
		want           bool
	}{
		{"https://api.openai.com/v1", "gpt-4o", false},
		{"https://api.openai.com/v1", "gpt-4.1-mini", false},
		{"https://api.openai.com/v1", "gpt-5", true},
		{"https://api.openai.com/v1", "o3-mini", true},
		{"https://api.openai.com/v1", "o4-mini", true},
		{"https://api.openai.com/v1", "o1", true},
		{"https://api.openai.com/v1", "omni-moderation", false},
		{"https://openrouter.ai/api/v1", "openai/gpt-5-mini", true},
		{"https://openrouter.ai/api/v1", "meta-llama/llama-3.3-70b", false},
		{"https://api.deepseek.com", "deepseek-reasoner", true},
		{"https://api.x.ai/v1", "grok-3", false},
		{"https://api.x.ai/v1", "grok-3-mini", true},
		{"https://api.x.ai/v1", "grok-4", true},
		{"https://generativelanguage.googleapis.com/v1beta/openai", "gemini-2.5-pro", true},
		{"https://generativelanguage.googleapis.com/v1beta/openai", "gemini-2.0-flash", false},
		{"https://api.z.ai/v1", "glm-4.6", true},
		{"https://api.z.ai/v1", "glm-4-plus", false},
		{"https://api.moonshot.ai/v1", "kimi-k2-thinking", true},
		{"https://api.moonshot.ai/v1", "kimi-k2-0905", false},
		{"https://dashscope.example.com/v1", "qwen3-235b", true},
		{"https://dashscope.example.com/v1", "qwen-max", false},
		{"http://127.0.0.1:8080/v1", "anything", true},
		{"http://192.168.0.71:8080/v1", "llama-3", true},
	}
	for _, tt := range tests {
		t.Run(tt.model+"@"+tt.baseURL, func(t *testing.T) {
			c := NewClient(Config{ProviderKind: ProviderKindOpenAICompatible, BaseURL: tt.baseURL}, nil)
			raw, err := c.openAIChatBody(&MessageRequest{Model: tt.model, Thinking: &ThinkingConfig{Type: "enabled", BudgetTokens: 8000}}, true)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if _, got := body["reasoning_effort"]; got != tt.want {
				t.Errorf("reasoning_effort present = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestOpenAIResponsesEffortGate(t *testing.T) {
	cfg := Config{BaseURL: "https://api.openai.com/v1"}
	out := openAIResponsesRequestFromMessageRequest(&MessageRequest{Model: "gpt-4o", Thinking: &ThinkingConfig{Type: "enabled", BudgetTokens: 8000}}, true, cfg)
	if out.Reasoning != nil {
		t.Errorf("gpt-4o got reasoning %+v", out.Reasoning)
	}
}

func TestOpenAIChat_RetriesWithoutRejectedReasoningEffort(t *testing.T) {
	var bodies []string
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(raw))
		calls.Add(1)
		if strings.Contains(string(raw), "reasoning_effort") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Unrecognized request argument supplied: reasoning_effort","type":"invalid_request_error"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"x","model":"m","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	// A private-range host would always send the effort; use a name-based
	// capable model on a loopback URL so the first attempt includes it.
	c := NewClient(Config{ProviderKind: ProviderKindOpenAICompatible, BaseURL: srv.URL}, nil)
	req := &MessageRequest{Model: "m", MaxTokens: 10, Thinking: &ThinkingConfig{Type: "enabled", BudgetTokens: 8000},
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}}}
	for i := 0; i < 2; i++ {
		resp, err := c.CreateMessage(context.Background(), req)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if resp.Content[0].Text != "ok" {
			t.Fatalf("call %d text = %q", i, resp.Content[0].Text)
		}
	}
	// First call: rejected then retried. Second call: remembered, sent once.
	if got := calls.Load(); got != 3 {
		t.Fatalf("server calls = %d, want 3", got)
	}
	if !strings.Contains(bodies[0], "reasoning_effort") || strings.Contains(bodies[1], "reasoning_effort") || strings.Contains(bodies[2], "reasoning_effort") {
		t.Errorf("bodies = %q", bodies)
	}
}

func TestOpenAIChat_UnrelatedBadRequestNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"context length exceeded","type":"invalid_request_error"}}`)
	}))
	defer srv.Close()
	c := NewClient(Config{ProviderKind: ProviderKindOpenAICompatible, BaseURL: srv.URL}, nil)
	req := &MessageRequest{Model: "m", MaxTokens: 10, Thinking: &ThinkingConfig{Type: "enabled", BudgetTokens: 8000},
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}}}
	_, err := c.CreateMessage(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "context length") {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestConvertOpenAIStream_ThinkTagInProse(t *testing.T) {
	evs, err := convertChat(openAIStreamConfig{model: "m", splitThinkTags: true},
		`{"choices":[{"delta":{"content":"The parser turns <think> into a thinking block. "}}]}`,
		`{"choices":[{"delta":{"content":"Here is the rest of my answer."}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`[DONE]`)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range trace(t, evs) {
		if strings.HasPrefix(l, "think") {
			t.Fatalf("prose became thinking: %v", trace(t, evs))
		}
	}
}

func TestConvertOpenAIStream_ImplicitThink(t *testing.T) {
	evs, err := convertChat(openAIStreamConfig{model: "m", splitThinkTags: true, implicitThink: true},
		`{"choices":[{"delta":{"content":"reasoning"}}]}`,
		`{"choices":[{"delta":{"content":" more</think>\n\nanswer"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		`[DONE]`)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(trace(t, evs), "|")
	want := "start 0 thinking|think 0 reasoning more|stop 0|start 1 text|text 1 answer|stop 1|end end_turn"
	if got != want {
		t.Errorf("trace = %q\nwant    %q", got, want)
	}
}

func TestConvertOpenAIStream_ParallelCallsSharingIndex(t *testing.T) {
	evs, err := convertChat(openAIStreamConfig{model: "m"},
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"Read","arguments":"{\"p\":1}"}},{"index":0,"id":"b","type":"function","function":{"name":"Bash","arguments":"{\"c\":2}"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":""}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(trace(t, evs), "|")
	want := "start 0 tool_use a Read|json 0 {\"p\":1}|start 1 tool_use b Bash|json 1 {\"c\":2}|stop 0|stop 1|end tool_use"
	if got != want {
		t.Errorf("trace = %q\nwant    %q", got, want)
	}
}

func TestConvertOpenAIStream_ArgumentDeltasFollowLatestBlockAtIndex(t *testing.T) {
	evs, err := convertChat(openAIStreamConfig{model: "m"},
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"Read","arguments":"{\"p\""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"b","function":{"name":"Bash","arguments":"{\"c\""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":2}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`[DONE]`)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(trace(t, evs), "|")
	if !strings.Contains(got, `json 1 :2}`) {
		t.Errorf("trace = %q", got)
	}
}

// A host that sends a finish_reason and then neither usage nor [DONE], and
// keeps the connection open, must not stall the stream.
func TestConvertOpenAIStream_FinishWithoutUsageOrDoneDoesNotStall(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	go func() {
		_, _ = io.WriteString(pw, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(pw, "data: {\"choices\":[]}\n\n")
		// Connection stays open.
	}()
	reader, writer := io.Pipe()
	go convertOpenAIStream(pr, writer, openAIStreamConfig{model: "m", postFinishWait: 20 * time.Millisecond})
	type result struct {
		trace []string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		evs, err := drainConverted(reader)
		done <- result{trace(t, evs), err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if got := r.trace[len(r.trace)-1]; got != "end end_turn" {
			t.Errorf("last = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream stalled after finish_reason")
	}
}

func TestOpenAIChat_StreamSplitsImplicitReasoningWhenRequestedLocally(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"thinking it over</think>answer\"}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	think := &ThinkingConfig{Type: "enabled", BudgetTokens: 8000}
	for name, tc := range map[string]struct {
		thinking     *ThinkingConfig
		wantThinking bool
	}{"thinking requested": {think, true}, "thinking not requested": {nil, false}} {
		model, wantThinking := name, tc.wantThinking
		c := NewClient(Config{ProviderKind: ProviderKindOpenAICompatible, BaseURL: srv.URL}, nil)
		st, err := c.StreamMessage(context.Background(), &MessageRequest{Model: "qwen3-8b", MaxTokens: 10, Thinking: tc.thinking,
			Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}}})
		if err != nil {
			t.Fatal(err)
		}
		var evs []sse.Event
		for {
			ev, err := st.Next()
			if err != nil {
				break
			}
			evs = append(evs, ev)
		}
		_ = st.Close()
		got := strings.Join(trace(t, evs), "|")
		if has := strings.Contains(got, "think 0 thinking it over"); has != wantThinking {
			t.Errorf("%s: trace = %q, thinking = %v want %v", model, got, has, wantThinking)
		}
	}
}

// Implicit-reasoning mode is only for local hosts with thinking requested.
// Elsewhere text streams as it arrives.
func TestConvertOpenAIStream_NonImplicitStreamsProgressively(t *testing.T) {
	got := make([]string, 0, 3)
	var s thinkSplitter
	for _, chunk := range []string{"Here is ", "the answer ", "streaming."} {
		segs := s.feed(chunk)
		if len(segs) == 0 {
			t.Fatalf("feed(%q) emitted nothing", chunk)
		}
		got = append(got, segs[0].text)
	}
	if strings.Join(got, "") != "Here is the answer streaming." {
		t.Errorf("got %q", got)
	}
}

func TestConvertOpenAIStream_ImplicitEndsOnReasoningFieldOrToolCall(t *testing.T) {
	cfg := openAIStreamConfig{model: "m", splitThinkTags: true, implicitThink: true}
	tests := []struct {
		name string
		data []string
		want string
	}{
		{"reasoning_content", []string{
			`{"choices":[{"delta":{"content":"held text"}}]}`,
			`{"choices":[{"delta":{"reasoning_content":"why"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`},
			"start 0 text|text 0 held text|stop 0|start 1 thinking|think 1 why|stop 1|end end_turn"},
		{"tool call keeps order", []string{
			`{"choices":[{"delta":{"content":"let me check"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"Read","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`, `[DONE]`},
			"start 0 text|text 0 let me check|stop 0|start 1 tool_use c Read|json 1 {}|stop 1|end tool_use"},
		{"hold limit", []string{
			`{"choices":[{"delta":{"content":"` + strings.Repeat("x", implicitHoldLimit+1000) + `"}}]}`,
			`{"choices":[{"delta":{"content":" more"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`, `[DONE]`},
			"start 0 text|text 0 " + strings.Repeat("x", implicitHoldLimit+1000) + "|text 0  more|stop 0|end end_turn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs, err := convertChat(cfg, tt.data...)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(trace(t, evs), "|"); got != tt.want {
				t.Errorf("trace = %q\nwant    %q", got, tt.want)
			}
		})
	}
}

func TestThinkSplitterImplicitIgnoresCloseInCode(t *testing.T) {
	got := splitAllWith(thinkSplitter{implicit: true}, "use `</think>` to close it", " and more")
	if len(got) != 1 || got[0] != "V:use `</think>` to close it and more" {
		t.Errorf("got %q", got)
	}
	got = splitAllWith(thinkSplitter{implicit: true}, "```\n</think>\n```\nreal</think>answer")
	if len(got) != 2 || got[0] != "T:```\n</think>\n```\nreal" || got[1] != "V:answer" {
		t.Errorf("got %q", got)
	}
}

// After the finish_reason, an empty chunk does not end the read: a usage
// chunk behind it is still taken.
func TestConvertOpenAIStream_UsageAfterEmptyChunkKept(t *testing.T) {
	evs, err := convertChat(openAIStreamConfig{model: "m"},
		`{"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`,
		`{"choices":[]}`,
		`{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2}}`)
	if err != nil {
		t.Fatal(err)
	}
	var out int
	for _, ev := range evs {
		if ev.Type == "message_delta" {
			md, _ := ev.AsMessageDelta()
			out = md.Usage.OutputTokens
		}
	}
	if out != 2 {
		t.Errorf("output tokens = %d, want 2", out)
	}
}
