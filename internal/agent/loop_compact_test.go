package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/tool"
)

// TestLoop_AutoCompact verifies that when the API reports input tokens
// exceeding 80% of MaxTokens during a tool_use turn, the loop fires a
// compaction call before the next main turn, replacing the history.
func TestLoop_AutoCompact(t *testing.T) {
	const maxTokens = 10000
	const threshold = int(float64(maxTokens) * 0.8) // 8000

	calls := 0
	var seenMsgCounts []int

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		calls++

		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		w.Header().Set("Content-Type", "text/event-stream")

		if strings.Contains(body.Model, "haiku") {
			// Compaction call — return summary.
			fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"c\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"haiku\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")
			fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"<summary>compacted summary</summary>\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
			fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}

		seenMsgCounts = append(seenMsgCounts, len(body.Messages))

		if calls == 1 {
			// First main turn — return tool_use with high input tokens.
			inputToks := itoa(threshold + 100)
			fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"sonnet\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":%s,\"output_tokens\":10}}}\n\n", inputToks)
			fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"Bash\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"command\\\":\\\"echo hi\\\"}\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":10}}\n\n")
			fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}

		// Second main turn (after compact) — return end_turn.
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m2\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"sonnet\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":50,\"output_tokens\":5}}}\n\n")
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Register a Bash tool stub.
	bashTool := &stubTool{name: "Bash"}

	client := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client())
	reg := tool.NewRegistry()
	reg.Register(bashTool)

	lp := NewLoop(client, reg, LoopConfig{
		Model:       "claude-sonnet-4-6",
		MaxTokens:   maxTokens,
		MaxTurns:    10,
		AutoCompact: true,
	})

	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hello"}}},
		{Role: "assistant", Content: []api.ContentBlock{{Type: "text", Text: "reply1"}}},
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "continue"}}},
		{Role: "assistant", Content: []api.ContentBlock{{Type: "text", Text: "reply2"}}},
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "continue"}}},
	}

	_, err := lp.Run(context.Background(), msgs, func(LoopEvent) {})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should have: turn1 (sonnet) + compact (haiku) + turn2 (sonnet) = 3 calls.
	if calls != 3 {
		t.Errorf("calls = %d; want 3 (turn1 + compact + turn2)", calls)
	}

	// After compaction, the second main call sees a smaller history
	// (1 message: the compact summary) vs the 5 we started with.
	if len(seenMsgCounts) < 2 {
		t.Fatalf("seenMsgCounts = %v; want 2 entries", seenMsgCounts)
	}
	first, second := seenMsgCounts[0], seenMsgCounts[1]
	if second >= first {
		t.Errorf("after compact, msg count %d should be less than before compact %d", second, first)
	}
}

// stubTool is a minimal tool.Tool implementation for testing.
type stubTool struct {
	name string
}

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Description() string { return "stub" }
func (s *stubTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (s *stubTool) Execute(_ context.Context, _ json.RawMessage) (tool.Result, error) {
	return tool.Result{Content: []tool.ResultBlock{{Type: "text", Text: "stub output"}}}, nil
}
func (s *stubTool) IsReadOnly(_ json.RawMessage) bool        { return true }
func (s *stubTool) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func itoa(n int) string {
	return fmt.Sprint(n)
}

// ensure strings import is used
var _ = strings.Contains
