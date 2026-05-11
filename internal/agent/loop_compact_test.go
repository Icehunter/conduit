package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/tool"
)

// makeCompactServer returns an httptest.Server whose handler simulates the
// Anthropic API. It produces:
//   - A first main turn with stop_reason=tool_use (or end_turn when toolUse=false)
//     and the given inputTokens in the usage field.
//   - A compaction call that returns a summary.
//   - A second main turn with stop_reason=end_turn.
//
// compactCalls is incremented atomically on every compaction request.
func makeCompactServer(t *testing.T, inputTokens int, toolUse bool, compactCalls *atomic.Int32) *httptest.Server {
	return makeCompactServerWithCache(t, inputTokens, 0, toolUse, compactCalls)
}

func makeCompactServerWithCache(t *testing.T, inputTokens, cacheReadTokens int, toolUse bool, compactCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	mainCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model  string `json:"model"`
			System []struct {
				Text string `json:"text"`
			} `json:"system"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		w.Header().Set("Content-Type", "text/event-stream")

		if isCompactRequest(body.System) {
			compactCalls.Add(1)
			// Compaction call — return a summary.
			fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"c\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"haiku\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")
			fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"<summary>compacted summary</summary>\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
			fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}

		mainCalls++
		toks := itoa(inputTokens)
		cache := itoa(cacheReadTokens)

		if mainCalls == 1 && toolUse {
			// First main turn: tool_use with the given input token count.
			fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"sonnet\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":%s,\"cache_read_input_tokens\":%s,\"output_tokens\":10}}}\n\n", toks, cache)
			fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"Bash\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"command\\\":\\\"echo hi\\\"}\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":10}}\n\n")
			fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}

		// end_turn response (first turn for non-toolUse, second turn otherwise).
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m2\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"sonnet\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":%s,\"cache_read_input_tokens\":%s,\"output_tokens\":5}}}\n\n", toks, cache)
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	return httptest.NewServer(mux)
}

// TestLoop_AutoCompact_ThresholdAccurate verifies that the threshold is based
// on the model's actual context window, not MaxTokens * 0.8. Specifically:
//   - 13K input tokens must NOT trigger compact (old bug fired at 12,800).
//   - A token count above the real threshold DOES trigger compact.
//
// We use CLAUDE_CODE_AUTO_COMPACT_WINDOW to set a small window (50K) so the
// test doesn't have to fake 167K+ tokens.
func TestLoop_AutoCompact_ThresholdAccurate(t *testing.T) {
	// Set a small window so we can test with reasonable token counts.
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "50000")

	modelName := "claude-sonnet-4-6"
	threshold := internalmodel.AutoCompactThresholdFor(modelName) // 50000 - 20000 - 13000 = 17000

	tests := []struct {
		name          string
		inputTokens   int
		wantCompacted bool
	}{
		{
			name:          "below threshold (13K) must not compact",
			inputTokens:   13_000,
			wantCompacted: false,
		},
		{
			name:          "above threshold fires compact",
			inputTokens:   threshold + 1000,
			wantCompacted: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var compactCalls atomic.Int32
			srv := makeCompactServer(t, tt.inputTokens, false, &compactCalls)
			defer srv.Close()

			client := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client())
			reg := tool.NewRegistry()

			lp := NewLoop(client, reg, LoopConfig{
				Model:       modelName,
				MaxTokens:   internalmodel.MaxTokens,
				MaxTurns:    10,
				AutoCompact: true,
				BackgroundModel: func() string {
					return "background-model"
				},
			})

			msgs := []api.Message{
				{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hello"}}},
			}

			var gotCompacted bool
			_, err := lp.Run(context.Background(), msgs, func(ev LoopEvent) {
				if ev.Type == EventCompacted {
					gotCompacted = true
				}
			})
			if err != nil {
				t.Fatalf("Run: %v", err)
			}

			if gotCompacted != tt.wantCompacted {
				t.Errorf("compacted=%v; want %v (inputTokens=%d, threshold=%d)",
					gotCompacted, tt.wantCompacted, tt.inputTokens, threshold)
			}
			wantCompactCalls := int32(0)
			if tt.wantCompacted {
				wantCompactCalls = 1
			}
			if got := compactCalls.Load(); got != wantCompactCalls {
				t.Errorf("compactCalls=%d; want %d", got, wantCompactCalls)
			}
		})
	}
}

func TestLoop_AutoCompact_CountsCachedPromptTokens(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "50000")

	modelName := "claude-sonnet-4-6"
	threshold := internalmodel.AutoCompactThresholdFor(modelName) // 17000
	var compactCalls atomic.Int32
	srv := makeCompactServerWithCache(t, 1, threshold+1000, false, &compactCalls)
	defer srv.Close()

	client := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client())
	lp := NewLoop(client, tool.NewRegistry(), LoopConfig{
		Model:       modelName,
		MaxTokens:   internalmodel.MaxTokens,
		MaxTurns:    10,
		AutoCompact: true,
		BackgroundModel: func() string {
			return "background-model"
		},
	})

	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hello"}}},
	}
	var gotCompacted bool
	_, err := lp.Run(context.Background(), msgs, func(ev LoopEvent) {
		if ev.Type == EventCompacted {
			gotCompacted = true
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !gotCompacted {
		t.Fatal("expected cached prompt tokens to trigger auto-compact")
	}
	if got := compactCalls.Load(); got != 1 {
		t.Errorf("compactCalls=%d; want 1", got)
	}
}

// TestLoop_AutoCompact_SingleCompactPerEndTurn verifies that compact fires
// exactly once per end_turn — not a second time after tool_results
// (M10: duplicate compact removed).
func TestLoop_AutoCompact_SingleCompactPerEndTurn(t *testing.T) {
	// Set a small window so we can test with manageable token counts.
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "50000")

	modelName := "claude-sonnet-4-6"
	threshold := internalmodel.AutoCompactThresholdFor(modelName) // 17000
	inputTokens := threshold + 1000                               // above threshold

	var compactCalls atomic.Int32
	// toolUse=true: first turn is tool_use, second is end_turn.
	// Compact should fire once at end_turn only.
	srv := makeCompactServer(t, inputTokens, true, &compactCalls)
	defer srv.Close()

	bashTool := &stubTool{name: "Bash"}
	client := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client())
	reg := tool.NewRegistry()
	reg.Register(bashTool)

	lp := NewLoop(client, reg, LoopConfig{
		Model:       modelName,
		MaxTokens:   internalmodel.MaxTokens,
		MaxTurns:    10,
		AutoCompact: true,
		BackgroundModel: func() string {
			return "background-model"
		},
	})

	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hello"}}},
	}

	compactedEvents := 0
	_, err := lp.Run(context.Background(), msgs, func(ev LoopEvent) {
		if ev.Type == EventCompacted {
			compactedEvents++
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Compact fires at end_turn only — exactly once.
	if compactCalls.Load() != 1 {
		t.Errorf("compactCalls=%d; want 1 (end_turn only, no duplicate after tool_results)", compactCalls.Load())
	}
	if compactedEvents != 1 {
		t.Errorf("EventCompacted count=%d; want 1", compactedEvents)
	}
}

// TestLoop_AutoCompact_DisableEnv verifies that DISABLE_AUTO_COMPACT suppresses compact.
func TestLoop_AutoCompact_DisableEnv(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "50000")
	t.Setenv("DISABLE_AUTO_COMPACT", "1")

	modelName := "claude-sonnet-4-6"
	threshold := internalmodel.AutoCompactThresholdFor(modelName)
	inputTokens := threshold + 10_000 // well above threshold

	var compactCalls atomic.Int32
	srv := makeCompactServer(t, inputTokens, false, &compactCalls)
	defer srv.Close()

	client := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client())
	reg := tool.NewRegistry()

	lp := NewLoop(client, reg, LoopConfig{
		Model:       modelName,
		MaxTokens:   internalmodel.MaxTokens,
		MaxTurns:    10,
		AutoCompact: true,
		BackgroundModel: func() string {
			return "background-model"
		},
	})

	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hello"}}},
	}

	var gotCompacted bool
	_, err := lp.Run(context.Background(), msgs, func(ev LoopEvent) {
		if ev.Type == EventCompacted {
			gotCompacted = true
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if gotCompacted {
		t.Error("compact fired with DISABLE_AUTO_COMPACT set; want no compact")
	}
	if compactCalls.Load() != 0 {
		t.Errorf("compactCalls=%d; want 0", compactCalls.Load())
	}
}

// TestLoop_AutoCompact verifies that auto-compact fires at end_turn when input
// tokens exceed the model's context threshold, and that the active model is
// used for the compaction call. Uses CLAUDE_CODE_AUTO_COMPACT_WINDOW to set a
// small window so we can test with low token counts.
func TestLoop_AutoCompact(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "50000")

	modelName := "claude-sonnet-4-6"
	threshold := internalmodel.AutoCompactThresholdFor(modelName) // 17000
	inputTokens := threshold + 1000                               // above threshold → compact must fire

	calls := 0
	var compactModel string

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		calls++

		var body struct {
			Model  string `json:"model"`
			System []struct {
				Text string `json:"text"`
			} `json:"system"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		w.Header().Set("Content-Type", "text/event-stream")

		if isCompactRequest(body.System) {
			compactModel = body.Model
			// Compaction call — return summary.
			fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"c\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"haiku\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")
			fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"<summary>compacted summary</summary>\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
			fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}

		// Main turn — end_turn with above-threshold input tokens.
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"sonnet\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":%d,\"output_tokens\":5}}}\n\n", inputTokens)
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client())
	reg := tool.NewRegistry()

	lp := NewLoop(client, reg, LoopConfig{
		Model:       modelName,
		MaxTokens:   internalmodel.MaxTokens,
		MaxTurns:    10,
		AutoCompact: true,
		BackgroundModel: func() string {
			return "background-model"
		},
	})

	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hello"}}},
	}

	var gotCompacted bool
	_, err := lp.Run(context.Background(), msgs, func(ev LoopEvent) {
		if ev.Type == EventCompacted {
			gotCompacted = true
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Should have: turn1 + compact on the active model = 2 calls.
	if calls != 2 {
		t.Errorf("calls = %d; want 2 (main turn + compact call)", calls)
	}
	if compactModel != modelName {
		t.Errorf("compact model = %q, want %q", compactModel, modelName)
	}
	if !gotCompacted {
		t.Error("EventCompacted not fired; want it fired on auto-compact")
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

func isCompactRequest(system []struct {
	Text string `json:"text"`
}) bool {
	for _, block := range system {
		if strings.Contains(block.Text, "conversation summarizer") {
			return true
		}
	}
	return false
}
