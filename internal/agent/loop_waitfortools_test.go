package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/tool"
)

// lateTool stands in for a tool whose source (an MCP server) is still
// starting when the first prompt is sent.
type lateTool struct{ tool.NotDeferrable }

func (lateTool) Name() string                           { return "LateTool" }
func (lateTool) Description() string                    { return "registered while Run waits" }
func (lateTool) InputSchema() json.RawMessage           { return json.RawMessage(`{"type":"object"}`) }
func (lateTool) IsReadOnly(json.RawMessage) bool        { return true }
func (lateTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (lateTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.TextResult("ok"), nil
}

// toolNamesServer ends every turn with a short text reply and records the
// tool names each request carried.
func toolNamesServer(t *testing.T, mu *sync.Mutex, seen *[][]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		names := make([]string, 0, len(body.Tools))
		for _, tl := range body.Tools {
			names = append(names, tl.Name)
		}
		mu.Lock()
		*seen = append(*seen, names)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"sonnet\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":5,\"output_tokens\":5}}}\n\n")
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
}

// MCP servers connect in the background, so the first prompt can arrive
// before their tools are registered. Run lets WaitForTools settle them before
// it snapshots the tool list; a wait that fails (the user cancelled) ends the
// Run without sending anything.
func TestLoop_WaitForToolsRunsBeforeToolSnapshot(t *testing.T) {
	waitErr := errors.New("cancelled while waiting")
	tests := []struct {
		name      string
		wait      func(reg *tool.Registry) func(context.Context) error
		wantErr   error
		wantTools []string // nil: no request expected
	}{
		{
			name:      "no hook",
			wait:      func(*tool.Registry) func(context.Context) error { return nil },
			wantTools: []string{},
		},
		{
			name: "tools registered during the wait are sent",
			wait: func(reg *tool.Registry) func(context.Context) error {
				return func(context.Context) error {
					reg.Register(lateTool{})
					return nil
				}
			},
			wantTools: []string{"LateTool"},
		},
		{
			name: "a failed wait ends the Run before any request",
			wait: func(*tool.Registry) func(context.Context) error {
				return func(context.Context) error { return waitErr }
			},
			wantErr: waitErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				mu   sync.Mutex
				seen [][]string
			)
			srv := toolNamesServer(t, &mu, &seen)
			defer srv.Close()
			reg := tool.NewRegistry()
			lp := NewLoop(api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client()), reg, LoopConfig{
				Model:           "claude-sonnet-4-6",
				MaxTokens:       internalmodel.MaxTokens,
				MaxTurns:        2,
				WaitForTools:    tt.wait(reg),
				BackgroundModel: func() string { return "background-model" },
			})
			_, err := lp.Run(context.Background(), []api.Message{
				{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "hello"}}},
			}, func(LoopEvent) {})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Run err = %v, want %v", err, tt.wantErr)
			}
			mu.Lock()
			defer mu.Unlock()
			if tt.wantTools == nil {
				if len(seen) != 0 {
					t.Fatalf("sent %d requests, want none", len(seen))
				}
				return
			}
			if len(seen) == 0 {
				t.Fatal("no request sent")
			}
			if got := seen[0]; !slices.Equal(got, tt.wantTools) {
				t.Errorf("first request tools = %v, want %v", got, tt.wantTools)
			}
		})
	}
}
