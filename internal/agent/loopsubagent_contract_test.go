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

// replyServer returns each supplied text as the assistant's final message, in
// order, and records the prompts it was sent.
func replyServer(t *testing.T, replies []string, seen *[]string) *httptest.Server {
	t.Helper()
	var n atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			System []struct {
				Text string `json:"text"`
			} `json:"system"`
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var sb strings.Builder
		for _, b := range body.System {
			sb.WriteString(b.Text)
		}
		for _, m := range body.Messages {
			sb.Write(m.Content)
		}
		*seen = append(*seen, sb.String())

		i := int(n.Add(1)) - 1
		reply := "{}"
		if i < len(replies) {
			reply = replies[i]
		}
		esc, _ := json.Marshal(reply)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"sonnet\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":5,\"output_tokens\":5}}}\n\n")
		fmt.Fprintf(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", esc)
		fmt.Fprintf(w, "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n")
		fmt.Fprintf(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n")
		fmt.Fprintf(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
}

func contractLoop(t *testing.T, srv *httptest.Server) *Loop {
	t.Helper()
	return NewLoop(api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "tok"}, srv.Client()), tool.NewRegistry(), LoopConfig{
		Model:           "claude-sonnet-4-6",
		MaxTokens:       internalmodel.MaxTokens,
		MaxTurns:        4,
		BackgroundModel: func() string { return "background-model" },
	})
}

const contractSchema = `{"type":"object","required":["verdict"],"properties":{"verdict":{"type":"string","enum":["keep","drop"]}},"additionalProperties":false}`

// A conforming first answer is returned as validated JSON, with no retry.
func TestRunSubAgentTyped_ContractSatisfiedFirstTry(t *testing.T) {
	var seen []string
	srv := replyServer(t, []string{`{"verdict":"keep"}`}, &seen)
	defer srv.Close()

	res, err := contractLoop(t, srv).RunSubAgentTyped(context.Background(), "judge it", SubAgentSpec{
		OutputSchema: json.RawMessage(contractSchema),
	})
	if err != nil {
		t.Fatalf("RunSubAgentTyped: %v", err)
	}
	if res.OutputError != "" {
		t.Fatalf("unexpected OutputError: %s", res.OutputError)
	}
	if string(res.Output) != `{"verdict":"keep"}` {
		t.Errorf("Output = %q", res.Output)
	}
	if len(seen) != 1 {
		t.Errorf("made %d requests, want 1 (no retry needed)", len(seen))
	}
	if !strings.Contains(seen[0], "Output contract") {
		t.Error("child was not told about the contract")
	}
}

// A non-conforming answer triggers exactly one retry, and the retry prompt
// carries the actual validation complaint rather than a generic nudge.
func TestRunSubAgentTyped_RetriesOnceThenAccepts(t *testing.T) {
	var seen []string
	srv := replyServer(t, []string{
		"Sure! I think we should keep it.", // no JSON at all
		`{"verdict":"drop"}`,
	}, &seen)
	defer srv.Close()

	res, err := contractLoop(t, srv).RunSubAgentTyped(context.Background(), "judge it", SubAgentSpec{
		OutputSchema: json.RawMessage(contractSchema),
	})
	if err != nil {
		t.Fatalf("RunSubAgentTyped: %v", err)
	}
	if res.OutputError != "" {
		t.Fatalf("unexpected OutputError: %s", res.OutputError)
	}
	if string(res.Output) != `{"verdict":"drop"}` {
		t.Errorf("Output = %q", res.Output)
	}
	if len(seen) != 2 {
		t.Fatalf("made %d requests, want exactly 2", len(seen))
	}
	if !strings.Contains(seen[1], "rejected") {
		t.Error("retry prompt did not tell the child it was rejected")
	}
}

// Two failures stop. The caller is told plainly rather than handed prose that
// looks like it satisfied the contract.
func TestRunSubAgentTyped_GivesUpAfterOneRetry(t *testing.T) {
	var seen []string
	srv := replyServer(t, []string{
		`{"verdict":"maybe"}`, // violates the enum
		`{"verdict":"also not valid"}`,
	}, &seen)
	defer srv.Close()

	res, err := contractLoop(t, srv).RunSubAgentTyped(context.Background(), "judge it", SubAgentSpec{
		OutputSchema: json.RawMessage(contractSchema),
	})
	if err != nil {
		t.Fatalf("RunSubAgentTyped: %v", err)
	}
	if res.OutputError == "" {
		t.Fatal("contract failed twice but OutputError is empty — the caller would treat this as valid")
	}
	if res.Output != nil {
		t.Errorf("Output should be nil on failure, got %q", res.Output)
	}
	if len(seen) != 2 {
		t.Errorf("made %d requests, want 2 (one retry, then stop)", len(seen))
	}
}

// A schema that cannot compile fails before any sub-agent is spawned.
func TestRunSubAgentTyped_RejectsBadSchemaWithoutSpawning(t *testing.T) {
	var seen []string
	srv := replyServer(t, []string{`{}`}, &seen)
	defer srv.Close()

	_, err := contractLoop(t, srv).RunSubAgentTyped(context.Background(), "judge it", SubAgentSpec{
		OutputSchema: json.RawMessage(`{"type": 12}`),
	})
	if err == nil {
		t.Fatal("invalid schema accepted")
	}
	if len(seen) != 0 {
		t.Errorf("spawned %d request(s) for a schema that could never be satisfied", len(seen))
	}
}
