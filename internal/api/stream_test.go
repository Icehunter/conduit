package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/sse"
)

// TestStreamMessage_FixtureReplay: server replays the captured SSE stream
// and our streaming reader yields the same event sequence the parser test
// validates, plus the full request shape mirrors the non-streaming path.
func TestStreamMessage_FixtureReplay(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "testdata", "fixtures", "sse", "simple_text_response.sse")
	body, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var captured *http.Request
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		captured = r.Clone(r.Context())
		// Drain so server doesn't reset.
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{
		BaseURL:     srv.URL,
		AuthToken:   "tok",
		BetaHeaders: []string{"oauth-2025-04-20"},
		SessionID:   "s",
	}, srv.Client())

	stream, err := c.StreamMessage(context.Background(), &MessageRequest{
		Model: "m", MaxTokens: 1, Stream: true,
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var types []string
	for {
		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		types = append(types, ev.Type)
	}
	want := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	if !equalSlices(types, want) {
		t.Errorf("event types\n got=%v\nwant=%v", types, want)
	}

	if captured.Header.Get("Authorization") != "Bearer tok" {
		t.Errorf("Authorization = %q", captured.Header.Get("Authorization"))
	}
}

// TestStreamMessage_ContextCancelStops verifies cancellation propagates
// into the SSE reader.
func TestStreamMessage_ContextCancelStops(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: ping\ndata: {\"type\": \"ping\"}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := c.StreamMessage(ctx, &MessageRequest{Model: "m", MaxTokens: 1, Stream: true})
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	defer func() { _ = stream.Close() }()

	cancel()

	// Drain — should hit an error or EOF promptly, not hang.
	for i := 0; i < 100; i++ {
		_, err := stream.Next()
		if err != nil {
			return
		}
	}
	t.Fatal("Next did not stop after cancel")
}

// TestStreamMessage_APIError on non-2xx returns the same error envelope as
// the non-streaming path.
func TestStreamMessage_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"oops"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	_, err := c.StreamMessage(context.Background(), &MessageRequest{Model: "m", MaxTokens: 1, Stream: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid_request_error") {
		t.Errorf("err = %v", err)
	}
}

// TestStreamMessage_SetsStreamTrue: even if the caller forgets, we force
// stream:true on the request body to avoid silent fallback to JSON.
func TestStreamMessage_SetsStreamTrue(t *testing.T) {
	var sawStream bool
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		sawStream = strings.Contains(string(raw), `"stream":true`)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	stream, err := c.StreamMessage(context.Background(), &MessageRequest{
		Model: "m", MaxTokens: 1, Stream: false, // intentionally false
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	for {
		_, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !sawStream {
		t.Error("StreamMessage did not force stream:true on the request body")
	}
}

func TestStreamMessage_OpenAICompatibleConvertsTextStream(t *testing.T) {
	var capturedPath string
	var capturedAuth string
	var capturedBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/openai/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_1\",\"model\":\"gemini-flash-latest\",\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\" there\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":123,\"completion_tokens\":4}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{
		ProviderKind: ProviderKindOpenAICompatible,
		BaseURL:      srv.URL + "/openai/",
		APIKey:       "gemini-key",
	}, srv.Client())
	stream, err := c.StreamMessage(context.Background(), &MessageRequest{
		Model:     "gemini-flash-latest",
		MaxTokens: 64,
		System:    []SystemBlock{{Type: "text", Text: "be brief"}},
		Messages: []Message{
			{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: "I am claude-3-5-sonnet-20241022."}}},
			{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}},
		},
	})
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var text strings.Builder
	var types []string
	var promptTokens, outputTokens int
	for {
		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		types = append(types, ev.Type)
		if ev.Type == "content_block_delta" {
			delta, err := ev.AsContentBlockDelta()
			if err != nil {
				t.Fatal(err)
			}
			text.WriteString(delta.Delta.Text)
		}
		if ev.Type == "message_delta" {
			delta, err := ev.AsMessageDelta()
			if err != nil {
				t.Fatal(err)
			}
			promptTokens = delta.Usage.InputTokens
			outputTokens = delta.Usage.OutputTokens
		}
	}
	if capturedPath != "/openai/chat/completions" {
		t.Fatalf("path = %q, want /openai/chat/completions", capturedPath)
	}
	if capturedAuth != "Bearer gemini-key" {
		t.Fatalf("Authorization = %q, want bearer key", capturedAuth)
	}
	if capturedBody["model"] != "gemini-flash-latest" || capturedBody["stream"] != true {
		t.Fatalf("body = %#v", capturedBody)
	}
	streamOptions, ok := capturedBody["stream_options"].(map[string]any)
	if !ok || streamOptions["include_usage"] != true {
		t.Fatalf("stream_options = %#v, want include_usage", capturedBody["stream_options"])
	}
	messages, ok := capturedBody["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %#v, want non-empty list", capturedBody["messages"])
	}
	system, ok := messages[0].(map[string]any)
	if !ok || system["role"] != "system" {
		t.Fatalf("first message = %#v, want system", messages[0])
	}
	systemText, _ := system["content"].(string)
	if strings.Contains(systemText, "Claude Agent SDK") || strings.Contains(systemText, "x-anthropic-billing-header") {
		t.Fatalf("OpenAI-compatible system prompt leaked Claude identity: %q", systemText)
	}
	if !strings.Contains(systemText, "gemini-flash-latest") {
		t.Fatalf("system prompt = %q, want configured model identity", systemText)
	}
	for _, msg := range messages {
		encoded, _ := json.Marshal(msg)
		if strings.Contains(string(encoded), "claude-3-5-sonnet-20241022") {
			t.Fatalf("OpenAI-compatible request leaked stale assistant identity: %s", encoded)
		}
	}
	if got := text.String(); got != "hi there" {
		t.Fatalf("stream text = %q, want hi there; types=%v", got, types)
	}
	if promptTokens != 123 || outputTokens != 4 {
		t.Fatalf("usage = (%d, %d), want (123, 4)", promptTokens, outputTokens)
	}
}

func TestStreamMessage_OpenAICompatibleConvertsToolCallStream(t *testing.T) {
	var capturedBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/openai/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_tool\",\"model\":\"gemini-flash-latest\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"Bash\",\"arguments\":\"{\\\"command\\\":\\\"echo\"}}]},\"finish_reason\":null}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\" hi\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{
		ProviderKind: ProviderKindOpenAICompatible,
		BaseURL:      srv.URL + "/openai/",
		APIKey:       "gemini-key",
	}, srv.Client())
	stream, err := c.StreamMessage(context.Background(), &MessageRequest{
		Model:     "gemini-flash-latest",
		MaxTokens: 64,
		System:    []SystemBlock{{Type: "text", Text: "use tools"}},
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "run echo"}}}},
		Tools: []ToolDef{{
			Name:        "Bash",
			Description: "Run a shell command",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"command": map[string]any{"type": "string"}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	defer func() { _ = stream.Close() }()

	tools, ok := capturedBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v, want one OpenAI tool", capturedBody["tools"])
	}

	var sawToolStart bool
	var args strings.Builder
	stopReason := ""
	for {
		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		switch ev.Type {
		case "content_block_start":
			start, err := ev.AsContentBlockStart()
			if err != nil {
				t.Fatal(err)
			}
			var block map[string]any
			_ = json.Unmarshal(start.ContentBlock, &block)
			if block["type"] == "tool_use" {
				sawToolStart = block["id"] == "call_1" && block["name"] == "Bash"
			}
		case "content_block_delta":
			delta, err := ev.AsContentBlockDelta()
			if err != nil {
				t.Fatal(err)
			}
			if delta.Delta.Type == "input_json_delta" {
				args.WriteString(delta.Delta.PartialJSON)
			}
		case "message_delta":
			md, err := ev.AsMessageDelta()
			if err != nil {
				t.Fatal(err)
			}
			stopReason = md.Delta.StopReason
		}
	}
	if !sawToolStart {
		t.Fatal("did not convert OpenAI tool call into Anthropic tool_use start")
	}
	if got := args.String(); got != `{"command":"echo hi"}` {
		t.Fatalf("tool args = %q, want command JSON", got)
	}
	if stopReason != "tool_use" {
		t.Fatalf("stop reason = %q, want tool_use", stopReason)
	}
}

func TestStreamMessage_OpenAIResponsesConvertsTextStream(t *testing.T) {
	var capturedPath string
	var capturedAuth string
	var capturedBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/openai/responses", func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"delta\":\"hi\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"delta\":\" there\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5\",\"status\":\"completed\",\"usage\":{\"input_tokens\":12,\"output_tokens\":3}}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{
		ProviderKind: ProviderKindOpenAIResponses,
		BaseURL:      srv.URL + "/openai/",
		APIKey:       "copilot-token",
	}, srv.Client())
	stream, err := c.StreamMessage(context.Background(), &MessageRequest{
		Model:     "gpt-5",
		MaxTokens: 64,
		System:    []SystemBlock{{Type: "text", Text: "be brief"}},
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hello"}}}},
	})
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var text strings.Builder
	var promptTokens, outputTokens int
	for {
		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		switch ev.Type {
		case "content_block_delta":
			delta, err := ev.AsContentBlockDelta()
			if err != nil {
				t.Fatal(err)
			}
			text.WriteString(delta.Delta.Text)
		case "message_delta":
			md, err := ev.AsMessageDelta()
			if err != nil {
				t.Fatal(err)
			}
			promptTokens = md.Usage.InputTokens
			outputTokens = md.Usage.OutputTokens
		}
	}
	if capturedPath != "/openai/responses" {
		t.Fatalf("path = %q, want /openai/responses", capturedPath)
	}
	if capturedAuth != "Bearer copilot-token" {
		t.Fatalf("Authorization = %q, want bearer key", capturedAuth)
	}
	if capturedBody["model"] != "gpt-5" || capturedBody["stream"] != true {
		t.Fatalf("body = %#v", capturedBody)
	}
	input, ok := capturedBody["input"].([]any)
	if !ok || len(input) != 2 {
		t.Fatalf("input = %#v, want developer + user", capturedBody["input"])
	}
	if got := text.String(); got != "hi there" {
		t.Fatalf("stream text = %q, want hi there", got)
	}
	if promptTokens != 12 || outputTokens != 3 {
		t.Fatalf("usage = (%d, %d), want (12, 3)", promptTokens, outputTokens)
	}
}

func TestStreamMessage_OpenAIResponses401RetryAfterRefresh(t *testing.T) {
	var calls int
	var seenTokens []string
	mux := http.NewServeMux()
	mux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		calls++
		seenTokens = append(seenTokens, r.Header.Get("Authorization"))
		if calls == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"type":"authentication_error","message":"expired"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"delta\":\"ok\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var c *Client
	cfg := Config{
		ProviderKind: ProviderKindOpenAIResponses,
		BaseURL:      srv.URL,
		AuthToken:    "stale",
	}
	cfg.OnAuth401 = func(ctx context.Context) error {
		c.SetAuthToken("fresh")
		return nil
	}
	c = NewClient(cfg, srv.Client())
	stream, err := c.StreamMessage(context.Background(), &MessageRequest{
		Model:     "gpt-5",
		MaxTokens: 8,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var sawTextDelta bool
	for {
		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if ev.Type == "content_block_delta" {
			sawTextDelta = true
			break
		}
	}
	if !sawTextDelta {
		t.Fatal("did not receive text delta after retry")
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if seenTokens[0] != "Bearer stale" || seenTokens[1] != "Bearer fresh" {
		t.Fatalf("tokens = %#v, want stale then fresh", seenTokens)
	}
}

func TestStreamMessage_OpenAIResponsesConvertsToolCallStream(t *testing.T) {
	var capturedBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/openai/responses", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"Bash\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"output_index\":0,\"delta\":\"{\\\"command\\\":\\\"echo\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"output_index\":0,\"delta\":\" hi\\\"}\"}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"Bash\"}}\n\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5\",\"status\":\"completed\",\"usage\":{\"input_tokens\":20,\"output_tokens\":5}}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{
		ProviderKind: ProviderKindOpenAIResponses,
		BaseURL:      srv.URL + "/openai/",
		APIKey:       "copilot-token",
	}, srv.Client())
	stream, err := c.StreamMessage(context.Background(), &MessageRequest{
		Model:     "gpt-5",
		MaxTokens: 64,
		Messages:  []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "run echo"}}}},
		Tools: []ToolDef{{
			Name:        "Bash",
			Description: "Run a shell command",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"command": map[string]any{"type": "string"}},
			},
		}},
	})
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	defer func() { _ = stream.Close() }()

	tools, ok := capturedBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v, want one Responses function tool", capturedBody["tools"])
	}

	var sawToolStart bool
	var args strings.Builder
	for {
		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		switch ev.Type {
		case "content_block_start":
			start, err := ev.AsContentBlockStart()
			if err != nil {
				t.Fatal(err)
			}
			var block map[string]any
			_ = json.Unmarshal(start.ContentBlock, &block)
			if block["type"] == "tool_use" {
				sawToolStart = block["id"] == "call_1" && block["name"] == "Bash"
			}
		case "content_block_delta":
			delta, err := ev.AsContentBlockDelta()
			if err != nil {
				t.Fatal(err)
			}
			if delta.Delta.Type == "input_json_delta" {
				args.WriteString(delta.Delta.PartialJSON)
			}
		}
	}
	if !sawToolStart {
		t.Fatal("did not convert Responses function call into Anthropic tool_use start")
	}
	if got := args.String(); got != `{"command":"echo hi"}` {
		t.Fatalf("tool args = %q, want command JSON", got)
	}
}

// silence unused for now in case parser package not auto-imported.
var _ = sse.NewParser

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
