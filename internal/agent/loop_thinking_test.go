package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/tool"
)

// TestLoop_ThinkingBudget verifies that when ThinkingBudget > 0 is set in
// LoopConfig, the API request includes thinking:{type:"enabled",budget_tokens:N}.
// Mirrors the interleaved-thinking-2025-05-14 beta behavior from the real CLI.
func TestLoop_ThinkingBudget(t *testing.T) {
	var capturedThinking json.RawMessage

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Thinking json.RawMessage `json:"thinking"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedThinking = body.Thinking

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"x\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client())
	reg := tool.NewRegistry()

	lp := NewLoop(client, reg, LoopConfig{
		Model:          "claude-sonnet-4-6",
		MaxTokens:      8000,
		MaxTurns:       1,
		ThinkingBudget: 5000,
	})

	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}},
	}, func(LoopEvent) {})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if capturedThinking == nil {
		t.Fatal("thinking field was not sent in API request")
	}

	var thinking struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens"`
	}
	if err := json.Unmarshal(capturedThinking, &thinking); err != nil {
		t.Fatalf("unmarshal thinking: %v", err)
	}
	if thinking.Type != "enabled" {
		t.Errorf("thinking.type = %q; want \"enabled\"", thinking.Type)
	}
	if thinking.BudgetTokens != 5000 {
		t.Errorf("thinking.budget_tokens = %d; want 5000", thinking.BudgetTokens)
	}
}

// TestLoop_NoThinkingByDefault verifies thinking is not sent when budget is 0.
func TestLoop_NoThinkingByDefault(t *testing.T) {
	var capturedBody map[string]json.RawMessage

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&capturedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"x\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n")
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client())
	reg := tool.NewRegistry()

	lp := NewLoop(client, reg, LoopConfig{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 8000,
		MaxTurns:  1,
		// No ThinkingBudget — field absent = 0.
	})

	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hi"}}},
	}, func(LoopEvent) {})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if _, ok := capturedBody["thinking"]; ok {
		t.Error("thinking field should not be present when ThinkingBudget == 0")
	}
}
