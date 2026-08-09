package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/tool"
)

// systemCapturingServer behaves like makeCompactServer but records the system
// prompt text of every non-compaction request, so a test can assert what the
// model actually saw on each turn.
func systemCapturingServer(t *testing.T, inputTokens int, seen *[]string, mu *sync.Mutex, compactCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	mainCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			System []struct {
				Text string `json:"text"`
			} `json:"system"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")

		if isCompactRequest(body.System) {
			compactCalls.Add(1)
			fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"c\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"haiku\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")
			fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"<summary>compacted summary</summary>\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
			fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}

		var sb strings.Builder
		for _, b := range body.System {
			sb.WriteString(b.Text)
			sb.WriteString("\n")
		}
		mu.Lock()
		*seen = append(*seen, sb.String())
		mu.Unlock()

		mainCalls++
		toks := itoa(inputTokens)
		if mainCalls == 1 {
			fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"sonnet\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":%s,\"output_tokens\":10}}}\n\n", toks)
			fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"Bash\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"command\\\":\\\"echo hi\\\"}\"}}\n\n")
			fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
			fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":10}}\n\n")
			fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m2\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"sonnet\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":%s,\"output_tokens\":5}}}\n\n", toks)
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	return httptest.NewServer(mux)
}

// A memory written between turns must reach the model on the next turn. Before
// RebuildSystem the system blocks were snapshotted once and never refreshed, so
// everything memdir extracted during a session stayed invisible until the next
// process start — the self-learning loop wrote to a store the running
// conversation could not read.
func TestLoop_RebuildSystem_PicksUpMemoryWrittenBetweenTurns(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "50000")
	modelName := "claude-sonnet-4-6"
	threshold := internalmodel.AutoCompactThresholdFor(modelName)

	var (
		mu           sync.Mutex
		seen         []string
		compactCalls atomic.Int32
	)
	srv := systemCapturingServer(t, threshold+1000, &seen, &mu, &compactCalls)
	defer srv.Close()

	// Stands in for the memory directory on disk.
	var memory atomic.Value
	memory.Store("memory: none yet")
	rebuild := func() []api.SystemBlock {
		return []api.SystemBlock{{Type: "text", Text: memory.Load().(string)}}
	}

	lp := NewLoop(api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client()), tool.NewRegistry(), LoopConfig{
		Model:           modelName,
		MaxTokens:       internalmodel.MaxTokens,
		MaxTurns:        10,
		AutoCompact:     true,
		System:          rebuild(),
		RebuildSystem:   rebuild,
		BackgroundModel: func() string { return "background-model" },
	})

	run := func() {
		t.Helper()
		if _, err := lp.Run(context.Background(), []api.Message{
			{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hello"}}},
		}, func(LoopEvent) {}); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}

	run()
	mu.Lock()
	first := len(seen)
	if first == 0 || !strings.Contains(seen[0], "memory: none yet") {
		mu.Unlock()
		t.Fatalf("first turn system = %q, want the original memory", seen)
	}
	mu.Unlock()

	// memdir.RunExtract writes a memory after the turn.
	memory.Store("memory: user prefers tabs")

	run()
	mu.Lock()
	defer mu.Unlock()
	if len(seen) <= first {
		t.Fatalf("second Run issued no requests")
	}
	for _, s := range seen[first:] {
		if !strings.Contains(s, "memory: user prefers tabs") {
			t.Errorf("post-write system = %q, want the refreshed memory", s)
		}
	}
	if compactCalls.Load() == 0 {
		t.Error("expected compaction to fire; setup no longer exercises that path")
	}
}

// Compaction invalidates the cached prefix anyway, so it is a free moment to
// pick up whatever was written earlier in the same run.
func TestLoop_RebuildSystem_RefreshedAfterCompaction(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "50000")
	modelName := "claude-sonnet-4-6"
	threshold := internalmodel.AutoCompactThresholdFor(modelName)

	var (
		mu           sync.Mutex
		seen         []string
		compactCalls atomic.Int32
	)
	srv := systemCapturingServer(t, threshold+1000, &seen, &mu, &compactCalls)
	defer srv.Close()

	var memory atomic.Value
	memory.Store("memory: before")
	lp := NewLoop(api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client()), tool.NewRegistry(), LoopConfig{
		Model:           modelName,
		MaxTokens:       internalmodel.MaxTokens,
		MaxTurns:        10,
		AutoCompact:     true,
		System:          []api.SystemBlock{{Type: "text", Text: "memory: before"}},
		RebuildSystem:   func() []api.SystemBlock { return []api.SystemBlock{{Type: "text", Text: memory.Load().(string)}} },
		BackgroundModel: func() string { return "background-model" },
	})

	// Written while the run is in flight, as a background extraction would.
	go func() { memory.Store("memory: after") }()

	if _, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hello"}}},
	}, func(LoopEvent) {}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if compactCalls.Load() == 0 {
		t.Fatal("expected compaction to fire; test setup is wrong")
	}

	lp.mu.RLock()
	defer lp.mu.RUnlock()
	if got := lp.cfg.System[0].Text; got != memory.Load().(string) {
		t.Errorf("cfg.System after compaction = %q, want the rebuilt value %q", got, memory.Load())
	}
}

// RefreshSystem is what a surface calls after memdir.RunExtract writes a memory.
func TestLoop_RefreshSystem(t *testing.T) {
	lp := NewLoop(nil, tool.NewRegistry(), LoopConfig{
		System:        []api.SystemBlock{{Type: "text", Text: "old"}},
		RebuildSystem: func() []api.SystemBlock { return []api.SystemBlock{{Type: "text", Text: "new"}} },
	})
	if got := lp.RefreshSystem(); len(got) != 1 || got[0].Text != "new" {
		t.Fatalf("RefreshSystem() = %+v, want the rebuilt blocks", got)
	}
	lp.mu.RLock()
	defer lp.mu.RUnlock()
	if lp.cfg.System[0].Text != "new" {
		t.Errorf("cfg.System = %q, want it replaced", lp.cfg.System[0].Text)
	}
}

// With no RebuildSystem configured nothing changes — surfaces that don't supply
// one keep the existing behaviour.
func TestLoop_RefreshSystem_NilIsNoOp(t *testing.T) {
	lp := NewLoop(nil, tool.NewRegistry(), LoopConfig{
		System: []api.SystemBlock{{Type: "text", Text: "old"}},
	})
	if got := lp.RefreshSystem(); got != nil {
		t.Errorf("RefreshSystem() = %+v, want nil", got)
	}
	lp.mu.RLock()
	defer lp.mu.RUnlock()
	if lp.cfg.System[0].Text != "old" {
		t.Errorf("cfg.System = %q, want it untouched", lp.cfg.System[0].Text)
	}
}
