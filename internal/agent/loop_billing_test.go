package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/tool"
)

var billingLineRe = regexp.MustCompile(`x-anthropic-billing-header: [^\n]*`)

func billingLineOf(t *testing.T, body []byte) string {
	t.Helper()
	var req struct {
		System []api.SystemBlock `json:"system"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if len(req.System) == 0 {
		t.Fatal("request had no system blocks")
	}
	return billingLineRe.FindString(req.System[0].Text)
}

// TestLoop_BillingContext — across one tool-use prompt the billing block
// carries the same cc_prompt_id on both requests, cc_prev_req only from the
// second request onward (bound to the first response's request-id), and a
// second Run() gets a fresh prompt id while keeping the last request-id.
func TestLoop_BillingContext(t *testing.T) {
	t.Setenv("CLAUDE_GO_BILLING_HEADER", "")
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Echo", result: "ok"})

	var lines []string
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		raw, _ := io.ReadAll(r.Body)
		lines = append(lines, billingLineOf(t, raw))
		w.Header().Set("request-id", "req_"+map[int]string{1: "first", 2: "second", 3: "third"}[call])
		w.Header().Set("Content-Type", "text/event-stream")
		switch call {
		case 1:
			_, _ = w.Write([]byte(toolCallOnlySSE("Echo", "toolu_1")))
		default:
			_, _ = w.Write([]byte(textOnlySSE("done")))
		}
	}))
	defer srv.Close()

	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	lp := NewLoop(c, reg, LoopConfig{
		Model: "m", MaxTokens: 1024, IsOAuthSubscription: true,
		System: []api.SystemBlock{{Type: "text", Text: BillingHeader}, {Type: "text", Text: "base"}},
	})

	if _, err := lp.Run(context.Background(), []api.Message{user("say the word hello and nothing else")}, func(LoopEvent) {}); err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 requests in prompt 1, got %d: %v", len(lines), lines)
	}
	pid1 := extractField(lines[0], "cc_prompt_id")
	if !isCanonicalUUID(pid1) {
		t.Fatalf("request 1 missing cc_prompt_id: %q", lines[0])
	}
	if extractField(lines[0], "cc_prev_req") != "" {
		t.Errorf("request 1 must not carry cc_prev_req: %q", lines[0])
	}
	if got := extractField(lines[1], "cc_prompt_id"); got != pid1 {
		t.Errorf("request 2 prompt id = %q; want same as request 1 %q", got, pid1)
	}
	if got := extractField(lines[1], "cc_prev_req"); got != "req_first" {
		t.Errorf("request 2 cc_prev_req = %q; want req_first", got)
	}

	if _, err := lp.Run(context.Background(), []api.Message{user("again")}, func(LoopEvent) {}); err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 requests total, got %d", len(lines))
	}
	pid2 := extractField(lines[2], "cc_prompt_id")
	if pid2 == "" || pid2 == pid1 {
		t.Errorf("second Run must get a fresh prompt id; got %q (first was %q)", pid2, pid1)
	}
	if got := extractField(lines[2], "cc_prev_req"); got != "req_second" {
		t.Errorf("second Run cc_prev_req = %q; want req_second", got)
	}
}

// TestLoop_ChildInheritsPromptID — a child built from a running parent bills
// under the parent's prompt and flags itself as a sub-agent.
func TestLoop_ChildInheritsPromptID(t *testing.T) {
	t.Setenv("CLAUDE_GO_BILLING_HEADER", "")
	reg := tool.NewRegistry()
	var lines []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		lines = append(lines, billingLineOf(t, raw))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE("done")))
	}))
	defer srv.Close()

	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	parent := NewLoop(c, reg, LoopConfig{
		Model: "m", MaxTokens: 1024, IsOAuthSubscription: true,
		System: []api.SystemBlock{{Type: "text", Text: BillingHeader}},
	})
	parent.mu.Lock()
	parent.promptID = "11111111-2222-4333-8444-555555555555"
	parent.mu.Unlock()

	child, _ := parent.buildChildLoop(SubAgentSpec{})
	child.subAgentLabel = "worker"
	if _, err := child.Run(context.Background(), []api.Message{user("go")}, func(LoopEvent) {}); err != nil {
		t.Fatalf("child Run: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("expected 1 request, got %d", len(lines))
	}
	if got := extractField(lines[0], "cc_prompt_id"); got != "11111111-2222-4333-8444-555555555555" {
		t.Errorf("child cc_prompt_id = %q; want parent's", got)
	}
	if extractField(lines[0], "cc_is_subagent") != "true" {
		t.Errorf("child must send cc_is_subagent=true: %q", lines[0])
	}
}

func extractField(line, key string) string {
	m := regexp.MustCompile(`\b` + key + `=([^;]+);`).FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return m[1]
}

// toolCallOnlySSE is one assistant turn that calls a tool and stops.
func toolCallOnlySSE(toolName, toolID string) string {
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"" + toolID + "\",\"name\":\"" + toolName + "\",\"input\":{}}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{}\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}
