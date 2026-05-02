package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
)

// fakeTool implements tool.Tool and records calls.
type fakeTool struct {
	name   string
	result string
	isErr  bool
}

func (f *fakeTool) Name() string                                          { return f.name }
func (f *fakeTool) Description() string                                   { return "fake" }
func (f *fakeTool) InputSchema() json.RawMessage                          { return json.RawMessage(`{"type":"object"}`) }
func (f *fakeTool) IsReadOnly(_ json.RawMessage) bool                     { return true }
func (f *fakeTool) IsConcurrencySafe(_ json.RawMessage) bool              { return true }
func (f *fakeTool) Execute(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	if f.isErr {
		return tool.ErrorResult(f.result), nil
	}
	return tool.TextResult(f.result), nil
}

// sseBody builds a minimal SSE stream with text-only response (no tools).
func textOnlySSE(text string) string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + text + "\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

// toolUseSSE builds an SSE stream where the model calls a tool then responds.
func toolUseSSE(toolName, toolID, inputJSON, responseText string) string {
	inputJSONEscaped := strings.ReplaceAll(inputJSON, `"`, `\"`)
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"" + toolID + "\",\"name\":\"" + toolName + "\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"" + inputJSONEscaped + "\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n" +
		// Second turn response after tool result
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_2\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":20,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + responseText + "\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":8}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

// newTestLoop builds a Loop backed by a test HTTP server returning the given SSE bodies in sequence.
func newTestLoop(t *testing.T, sseBodies []string, reg *tool.Registry) (*Loop, *httptest.Server) {
	t.Helper()
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if callCount < len(sseBodies) {
			_, _ = w.Write([]byte(sseBodies[callCount]))
			callCount++
		} else {
			// Fallback: end_turn with no content
			_, _ = w.Write([]byte(textOnlySSE("done")))
		}
	}))

	c := api.NewClient(api.Config{
		BaseURL:   srv.URL,
		AuthToken: "test",
	}, srv.Client())

	lp := NewLoop(c, reg, LoopConfig{
		Model:     "test-model",
		MaxTokens: 1024,
		System:    []api.SystemBlock{{Type: "text", Text: "test"}},
	})
	return lp, srv
}

func TestLoop_TextOnlyResponse(t *testing.T) {
	reg := tool.NewRegistry()
	lp, srv := newTestLoop(t, []string{textOnlySSE("Hello!")}, reg)
	defer srv.Close()

	var texts []string
	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}},
	}, func(ev LoopEvent) {
		if ev.Type == EventText {
			texts = append(texts, ev.Text)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(texts) == 0 {
		t.Error("no text events received")
	}
	full := strings.Join(texts, "")
	if !strings.Contains(full, "Hello!") {
		t.Errorf("expected 'Hello!' in text, got: %q", full)
	}
}

func TestLoop_ToolUseDispatchAndContinue(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Bash", result: "command output"})

	// First call: tool use. Second call: final text response.
	sse1 := toolUseSSE("Bash", "toolu_01", `{}`, "Done!")
	lp, srv := newTestLoop(t, []string{sse1}, reg)
	defer srv.Close()

	var texts []string
	var toolCalls []string
	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "run something"}}},
	}, func(ev LoopEvent) {
		switch ev.Type {
		case EventText:
			texts = append(texts, ev.Text)
		case EventToolUse:
			toolCalls = append(toolCalls, ev.ToolName)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(toolCalls) == 0 {
		t.Error("expected tool call event")
	}
	if toolCalls[0] != "Bash" {
		t.Errorf("tool name = %q, want Bash", toolCalls[0])
	}
	if !strings.Contains(strings.Join(texts, ""), "Done!") {
		t.Errorf("expected 'Done!' in final text")
	}
}

func TestLoop_UnknownToolReturnsError(t *testing.T) {
	reg := tool.NewRegistry()
	// Don't register any tools — model asks for "MissingTool"
	sse1 := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_99\",\"name\":\"MissingTool\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	// Second call returns end_turn
	sse2 := textOnlySSE("ok")
	lp, srv := newTestLoop(t, []string{sse1, sse2}, reg)
	defer srv.Close()

	var toolEvents []LoopEvent
	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}},
	}, func(ev LoopEvent) {
		if ev.Type == EventToolResult {
			toolEvents = append(toolEvents, ev)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(toolEvents) == 0 {
		t.Error("expected tool result event for unknown tool")
	}
	// The tool result should be an error
	if !toolEvents[0].IsError {
		t.Error("unknown tool should produce IsError=true tool result")
	}
}

func TestLoop_ContextCancellation(t *testing.T) {
	reg := tool.NewRegistry()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until context cancelled
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	lp := NewLoop(c, reg, LoopConfig{
		Model:     "m",
		MaxTokens: 1,
		System:    nil,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := lp.Run(ctx, []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}},
	}, func(ev LoopEvent) {})
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

func TestLoop_MaxTurnsRespected(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Bash", result: "out"})

	// Every response is another tool call — loop should stop at MaxTurns.
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "text/event-stream")
		// Always respond with a tool_use — infinite loop unless MaxTurns cuts it.
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_" + strings.Repeat("0", callCount) + "\",\"name\":\"Bash\",\"input\":{}}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer srv.Close()

	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	lp := NewLoop(c, reg, LoopConfig{
		Model:     "m",
		MaxTokens: 1024,
		MaxTurns:  3,
	})

	_, _ = lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "loop"}}},
	}, func(ev LoopEvent) {})

	if callCount > 3 {
		t.Errorf("MaxTurns=3 not respected: made %d API calls", callCount)
	}
}

func TestLoop_APIError(t *testing.T) {
	reg := tool.NewRegistry()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"bad token"}}`)
	}))
	defer srv.Close()

	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	lp := NewLoop(c, reg, LoopConfig{Model: "m", MaxTokens: 1})

	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}},
	}, func(ev LoopEvent) {})
	if err == nil {
		t.Error("expected error from 401")
	}
	if !strings.Contains(err.Error(), "authentication_error") {
		t.Errorf("err = %v", err)
	}
}

func TestLoop_ToolResultErrorPropagated(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Bash", result: "it failed", isErr: true})

	// One tool call then end_turn
	sse1 := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_01\",\"name\":\"Bash\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	sse2 := textOnlySSE("understood")
	lp, srv := newTestLoop(t, []string{sse1, sse2}, reg)
	defer srv.Close()

	var errResults []LoopEvent
	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "run"}}},
	}, func(ev LoopEvent) {
		if ev.Type == EventToolResult && ev.IsError {
			errResults = append(errResults, ev)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(errResults) == 0 {
		t.Error("expected error tool result event")
	}
}

// errorReader is a helper for the errors package import.
var _ = errors.New

// newTestLoopWithConfig builds a Loop with a custom LoopConfig (beyond defaults).
func newTestLoopWithConfig(t *testing.T, sseBodies []string, reg *tool.Registry, cfg LoopConfig) (*Loop, *httptest.Server) {
	t.Helper()
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if callCount < len(sseBodies) {
			_, _ = w.Write([]byte(sseBodies[callCount]))
			callCount++
		} else {
			_, _ = w.Write([]byte(textOnlySSE("done")))
		}
	}))

	c := api.NewClient(api.Config{
		BaseURL:   srv.URL,
		AuthToken: "test",
	}, srv.Client())

	cfg.Model = "test-model"
	cfg.MaxTokens = 1024
	if cfg.System == nil {
		cfg.System = []api.SystemBlock{{Type: "text", Text: "test"}}
	}

	lp := NewLoop(c, reg, cfg)
	return lp, srv
}

// singleToolUseSSE builds an SSE stream for exactly one tool_use turn (stop_reason=tool_use).
// The loop will execute tools then make a second HTTP call for the follow-up.
func singleToolUseSSE(toolName, toolID, inputJSON string) string {
	inputJSONEscaped := strings.ReplaceAll(inputJSON, `"`, `\"`)
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"" + toolID + "\",\"name\":\"" + toolName + "\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"" + inputJSONEscaped + "\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

func TestLoop_PermissionDeny(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Bash", result: "should not run"})

	// First call: tool_use turn. Second call: follow-up after tool result.
	sse1 := singleToolUseSSE("Bash", "toolu_01", `{}`)
	sse2 := textOnlySSE("understood")

	gate := newDenyGate("Bash")
	lp, srv := newTestLoopWithConfig(t, []string{sse1, sse2}, reg, LoopConfig{Gate: gate})
	defer srv.Close()

	var toolResults []LoopEvent
	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "run bash"}}},
	}, func(ev LoopEvent) {
		if ev.Type == EventToolResult {
			toolResults = append(toolResults, ev)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(toolResults) == 0 {
		t.Fatal("expected a tool result event")
	}
	if !toolResults[0].IsError {
		t.Error("denied tool should produce IsError=true result")
	}
	if !strings.Contains(toolResults[0].ResultText, "denied") {
		t.Errorf("result text should mention denial, got: %q", toolResults[0].ResultText)
	}
}

func TestLoop_PermissionAllow(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Bash", result: "allowed output"})

	sse1 := singleToolUseSSE("Bash", "toolu_02", `{}`)
	sse2 := textOnlySSE("done")

	gate := newAllowGate()
	lp, srv := newTestLoopWithConfig(t, []string{sse1, sse2}, reg, LoopConfig{Gate: gate})
	defer srv.Close()

	var toolResults []LoopEvent
	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "run bash"}}},
	}, func(ev LoopEvent) {
		if ev.Type == EventToolResult {
			toolResults = append(toolResults, ev)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(toolResults) == 0 {
		t.Fatal("expected a tool result event")
	}
	if toolResults[0].IsError {
		t.Errorf("allowed tool should not produce error result: %q", toolResults[0].ResultText)
	}
	if toolResults[0].ResultText != "allowed output" {
		t.Errorf("result text = %q, want 'allowed output'", toolResults[0].ResultText)
	}
}

func TestLoop_PreToolUseHookBlocks(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Bash", result: "should not run"})

	sse1 := singleToolUseSSE("Bash", "toolu_03", `{}`)
	sse2 := textOnlySSE("understood")

	hooksConfig := newBlockingPreToolHooks("Bash")
	lp, srv := newTestLoopWithConfig(t, []string{sse1, sse2}, reg, LoopConfig{Hooks: hooksConfig, SessionID: "test-sess"})
	defer srv.Close()

	var toolResults []LoopEvent
	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "run bash"}}},
	}, func(ev LoopEvent) {
		if ev.Type == EventToolResult {
			toolResults = append(toolResults, ev)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(toolResults) == 0 {
		t.Fatal("expected a tool result event")
	}
	if !toolResults[0].IsError {
		t.Error("blocked tool should produce IsError=true result")
	}
	if !strings.Contains(toolResults[0].ResultText, "hook") {
		t.Errorf("result text should mention hook, got: %q", toolResults[0].ResultText)
	}
}

// twoToolUseSSE builds an SSE stream where the model calls two tools in one turn.
// Only contains the tool_use message (stop_reason=tool_use); the caller provides
// a separate SSE body for the follow-up response.
func twoToolUseSSE(tool1Name, tool1ID, tool2Name, tool2ID string) string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"" + tool1ID + "\",\"name\":\"" + tool1Name + "\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"" + tool2ID + "\",\"name\":\"" + tool2Name + "\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

func TestLoop_ParallelTools_BothExecute(t *testing.T) {
	var calls []string
	var mu sync.Mutex

	reg := tool.NewRegistry()
	reg.Register(&callRecordingTool{name: "ToolA", result: "a-result", calls: &calls, mu: &mu})
	reg.Register(&callRecordingTool{name: "ToolB", result: "b-result", calls: &calls, mu: &mu})

	// Two HTTP responses: first with two tool_use blocks, second with text.
	sse1 := twoToolUseSSE("ToolA", "toolu_01", "ToolB", "toolu_02")
	sse2 := textOnlySSE("done")

	lp, srv := newTestLoopWithConfig(t, []string{sse1, sse2}, reg, LoopConfig{})
	defer srv.Close()

	var toolResults []LoopEvent
	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "go"}}},
	}, func(ev LoopEvent) {
		if ev.Type == EventToolResult {
			toolResults = append(toolResults, ev)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(toolResults) != 2 {
		t.Fatalf("expected 2 tool results; got %d", len(toolResults))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Errorf("expected both tools called; got %v", calls)
	}
}

func TestLoop_ParallelTools_ResultOrderPreserved(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "ToolA", result: "result-a"})
	reg.Register(&fakeTool{name: "ToolB", result: "result-b"})

	sse1 := twoToolUseSSE("ToolA", "toolu_01", "ToolB", "toolu_02")
	sse2 := textOnlySSE("done")

	lp, srv := newTestLoopWithConfig(t, []string{sse1, sse2}, reg, LoopConfig{})
	defer srv.Close()

	var toolResults []LoopEvent
	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "go"}}},
	}, func(ev LoopEvent) {
		if ev.Type == EventToolResult {
			toolResults = append(toolResults, ev)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(toolResults) != 2 {
		t.Fatalf("expected 2 tool results; got %d", len(toolResults))
	}
	if toolResults[0].ToolName != "ToolA" {
		t.Errorf("expected first result from ToolA; got %q", toolResults[0].ToolName)
	}
	if toolResults[1].ToolName != "ToolB" {
		t.Errorf("expected second result from ToolB; got %q", toolResults[1].ToolName)
	}
}

func TestLoop_ParallelTools_ConcurrentExecution(t *testing.T) {
	// Two slow tools should complete faster together than sequentially.
	// We use a barrier to verify both Execute calls are active simultaneously.
	started := make(chan struct{}, 2)
	barrier := make(chan struct{})

	reg := tool.NewRegistry()
	reg.Register(&barrierTool{name: "ToolA", result: "a", started: started, barrier: barrier})
	reg.Register(&barrierTool{name: "ToolB", result: "b", started: started, barrier: barrier})

	sse1 := twoToolUseSSE("ToolA", "toolu_01", "ToolB", "toolu_02")
	sse2 := textOnlySSE("done")

	lp, srv := newTestLoopWithConfig(t, []string{sse1, sse2}, reg, LoopConfig{})
	defer srv.Close()

	done := make(chan error, 1)
	go func() {
		_, err := lp.Run(context.Background(), []api.Message{
			{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "go"}}},
		}, func(LoopEvent) {})
		done <- err
	}()

	// Wait for both tools to start, then release the barrier.
	// If tools were sequential, the second would never start while the first is blocked.
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-timer.C:
			t.Fatal("timeout waiting for both tools to start (not running concurrently)")
		}
	}
	close(barrier) // release both tools

	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// barrierTool blocks until a barrier channel is closed, for concurrency testing.
type barrierTool struct {
	name    string
	result  string
	started chan<- struct{}
	barrier <-chan struct{}
}

func (b *barrierTool) Name() string             { return b.name }
func (b *barrierTool) Description() string      { return "barrier" }
func (b *barrierTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (b *barrierTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (b *barrierTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }
func (b *barrierTool) Execute(ctx context.Context, _ json.RawMessage) (tool.Result, error) {
	b.started <- struct{}{}
	select {
	case <-b.barrier:
	case <-ctx.Done():
		return tool.ErrorResult("cancelled"), nil
	}
	return tool.TextResult(b.result), nil
}

// callRecordingTool records which tool was called (for parallel concurrency checks).
type callRecordingTool struct {
	name   string
	result string
	calls  *[]string
	mu     *sync.Mutex
}

func (c *callRecordingTool) Name() string             { return c.name }
func (c *callRecordingTool) Description() string      { return "records calls" }
func (c *callRecordingTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (c *callRecordingTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (c *callRecordingTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }
func (c *callRecordingTool) Execute(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	c.mu.Lock()
	*c.calls = append(*c.calls, c.name)
	c.mu.Unlock()
	return tool.TextResult(c.result), nil
}

func TestLoop_NoGateAllowsAll(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Bash", result: "ran fine"})

	sse1 := singleToolUseSSE("Bash", "toolu_04", `{}`)
	sse2 := textOnlySSE("done")

	// No gate configured — all tools should run.
	lp, srv := newTestLoopWithConfig(t, []string{sse1, sse2}, reg, LoopConfig{})
	defer srv.Close()

	var toolResults []LoopEvent
	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "go"}}},
	}, func(ev LoopEvent) {
		if ev.Type == EventToolResult {
			toolResults = append(toolResults, ev)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(toolResults) == 0 {
		t.Fatal("expected a tool result event")
	}
	if toolResults[0].IsError {
		t.Errorf("nil gate should allow all tools; got error: %q", toolResults[0].ResultText)
	}
}

// --- helpers for M5 tests ---

func newDenyGate(toolName string) *permissions.Gate {
	return permissions.New(permissions.ModeDefault, nil, []string{toolName}, nil)
}

func newAllowGate() *permissions.Gate {
	return permissions.New(permissions.ModeBypassPermissions, nil, nil, nil)
}

func newBlockingPreToolHooks(toolName string) *settings.HooksSettings {
	return &settings.HooksSettings{
		PreToolUse: []settings.HookMatcher{{
			Matcher: toolName,
			Hooks:   []settings.Hook{{Type: "command", Command: "false"}},
		}},
	}
}
