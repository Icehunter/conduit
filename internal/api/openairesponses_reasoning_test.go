package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/sse"
)

// deltaOf returns the message_delta usage object of evs.
func deltaUsage(t *testing.T, evs []sse.Event) map[string]int {
	t.Helper()
	for _, ev := range evs {
		if ev.Type != "message_delta" {
			continue
		}
		var d struct {
			Usage map[string]int `json:"usage"`
		}
		if err := json.Unmarshal(ev.RawData, &d); err != nil {
			t.Fatal(err)
		}
		return d.Usage
	}
	t.Fatal("no message_delta")
	return nil
}

func TestResponsesStream_ReasoningSummaryAndSignature(t *testing.T) {
	tests := []struct {
		name string
		data []string
		want []string
	}{
		{
			name: "summary text and signature",
			data: []string{
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
				`{"type":"response.reasoning_summary_part.added","item_id":"rs_1","summary_index":0}`,
				`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","delta":"Check "}`,
				`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","delta":"files."}`,
				`{"type":"response.reasoning_summary_part.added","item_id":"rs_1","summary_index":1}`,
				`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","delta":"Then edit."}`,
				`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"ENC"}}`,
				`{"type":"response.output_text.delta","delta":"done"}`,
				responsesCompleted,
			},
			want: []string{
				"start 0 thinking", "think 0 Check ", "think 0 files.", "think 0 \n\n", "think 0 Then edit.",
				"delta signature_delta", "stop 0", "start 1 text", "text 1 done", "stop 1", "end end_turn",
			},
		},
		{
			name: "encrypted reasoning without summary still carries the signature",
			data: []string{
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_2"}}`,
				`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_2","encrypted_content":"ENC2"}}`,
				`{"type":"response.output_text.delta","delta":"hi"}`,
				responsesCompleted,
			},
			want: []string{"start 0 thinking", "delta signature_delta", "stop 0", "start 1 text", "text 1 hi", "stop 1", "end end_turn"},
		},
		{
			name: "reasoning without encrypted content closes cleanly",
			data: []string{
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_3"}}`,
				`{"type":"response.reasoning_summary_text.delta","delta":"hmm"}`,
				`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_3"}}`,
				`{"type":"response.reasoning_summary_text.delta","delta":"again"}`,
				responsesCompleted,
			},
			want: []string{"start 0 thinking", "think 0 hmm", "stop 0", "start 1 thinking", "think 1 again", "stop 1", "end end_turn"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs, err := convertResponses(tt.data...)
			if err != nil {
				t.Fatal(err)
			}
			if got := trace(t, evs); strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("trace = %q\nwant    %q", got, tt.want)
			}
		})
	}
}

func TestResponsesStream_SignatureValue(t *testing.T) {
	evs, err := convertResponses(
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_9","encrypted_content":"abc:def"}}`,
		responsesCompleted,
	)
	if err != nil {
		t.Fatal(err)
	}
	var sig string
	for _, ev := range evs {
		if ev.Type == "content_block_delta" {
			d, _ := ev.AsContentBlockDelta()
			sig = d.Delta.Signature
		}
	}
	id, enc, ok := decodeReasoningSignature(sig)
	if !ok || id != "rs_9" || enc != "abc:def" {
		t.Fatalf("signature %q decoded to %q %q %v", sig, id, enc, ok)
	}
}

func TestResponsesStream_CachedTokens(t *testing.T) {
	tests := []struct {
		name    string
		usage   string
		in, out int
		cached  int
	}{
		{"cached split from input", `{"input_tokens":1000,"output_tokens":7,"input_tokens_details":{"cached_tokens":600}}`, 400, 7, 600},
		{"no details", `{"input_tokens":50,"output_tokens":3}`, 50, 3, 0},
		{"cached above input is clamped", `{"input_tokens":10,"output_tokens":1,"input_tokens_details":{"cached_tokens":99}}`, 0, 1, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs, err := convertResponses(`{"type":"response.completed","response":{"status":"completed","usage":` + tt.usage + `}}`)
			if err != nil {
				t.Fatal(err)
			}
			u := deltaUsage(t, evs)
			if u["input_tokens"] != tt.in || u["output_tokens"] != tt.out || u["cache_read_input_tokens"] != tt.cached {
				t.Errorf("usage = %v, want in=%d out=%d cached=%d", u, tt.in, tt.out, tt.cached)
			}
		})
	}
}

func TestResponsesRequest_ReasoningCacheKeyInclude(t *testing.T) {
	think := &ThinkingConfig{Type: "enabled", BudgetTokens: 4000}
	tests := []struct {
		name        string
		cfg         Config
		req         MessageRequest
		wantSummary string
		wantInclude bool
		wantKey     string
	}{
		{"codex-style: store false, hint session", Config{OpenAIResponsesStore: boolPtr(false), OpenAIResponsesReplayReasoning: true, OpenAIResponsesPromptCacheKey: true, SessionID: "client-id"},
			MessageRequest{Model: "gpt-5.4", Thinking: think, Hints: &WireHints{SessionID: "sess-1"}}, "auto", true, "sess-1"},
		{"falls back to client session id", Config{OpenAIResponsesPromptCacheKey: true, SessionID: "client-id"},
			MessageRequest{Model: "gpt-5.4", Thinking: think}, "auto", false, "client-id"},
		{"store true: no include, no cache key unless enabled", Config{OpenAIResponsesStore: boolPtr(true), OpenAIResponsesReplayReasoning: true},
			MessageRequest{Model: "gpt-5.4", Thinking: think, Hints: &WireHints{SessionID: "s"}}, "auto", false, ""},
		{"no thinking: no summary", Config{OpenAIResponsesStore: boolPtr(false), OpenAIResponsesReplayReasoning: true},
			MessageRequest{Model: "gpt-5.4"}, "", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.cfg.BaseURL = "https://api.example.com"
			got := openAIResponsesRequestFromMessageRequest(&tt.req, true, tt.cfg)
			summary := ""
			if got.Reasoning != nil {
				summary = got.Reasoning.Summary
			}
			if summary != tt.wantSummary {
				t.Errorf("summary = %q, want %q", summary, tt.wantSummary)
			}
			if has := len(got.Include) == 1 && got.Include[0] == "reasoning.encrypted_content"; has != tt.wantInclude {
				t.Errorf("include = %v, want encrypted_content=%v", got.Include, tt.wantInclude)
			}
			if got.PromptCacheKey != tt.wantKey {
				t.Errorf("prompt_cache_key = %q, want %q", got.PromptCacheKey, tt.wantKey)
			}
		})
	}
}

func TestResponsesInputs_ReasoningReplay(t *testing.T) {
	sig := encodeReasoningSignature("rs_1", "ENC")
	assistant := Message{Role: "assistant", Content: []ContentBlock{
		{Type: "thinking", Thinking: "why", Signature: sig},
		{Type: "thinking", Thinking: "foreign", Signature: "anthropic-sig"},
		{Type: "text", Text: "ok"},
		{Type: "tool_use", ID: "c1", Name: "Bash", Input: map[string]any{}},
	}}
	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{"replayed first, in order", Config{OpenAIResponsesReplayReasoning: true}, []string{"reasoning", "assistant", "function_call"}},
		{"not replayed when disabled", Config{}, []string{"assistant", "function_call"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			items := openAIResponsesInputsFromMessage(assistant, tt.cfg, "m")
			for _, it := range items {
				if it.Type != "" {
					got = append(got, it.Type)
				} else {
					got = append(got, it.Role)
				}
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("items = %v, want %v", got, tt.want)
			}
			raw, _ := json.Marshal(items[0])
			if tt.cfg.OpenAIResponsesReplayReasoning && string(raw) != `{"type":"reasoning","summary":[],"encrypted_content":"ENC"}` {
				t.Errorf("reasoning item = %s", raw)
			}
		})
	}
}

func TestResponsesCreateMessage_ReasoningAndCachedUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"r1","model":"gpt-5","status":"completed","output":[
			{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"a"},{"type":"summary_text","text":"b"}],"encrypted_content":"ENC"},
			{"type":"message","content":[{"type":"output_text","text":"hi"}]}],
			"usage":{"input_tokens":100,"output_tokens":5,"input_tokens_details":{"cached_tokens":40}}}`)
	}))
	defer srv.Close()
	c := NewClient(Config{ProviderKind: ProviderKindOpenAIResponses, BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	resp, err := c.CreateMessage(context.Background(), &MessageRequest{Model: "gpt-5", MaxTokens: 10, Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "x"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Content) != 2 || resp.Content[0].Type != "thinking" || resp.Content[0].Thinking != "a\n\nb" ||
		resp.Content[0].Signature != encodeReasoningSignature("rs_1", "ENC") || resp.Content[1].Text != "hi" {
		t.Errorf("content = %+v", resp.Content)
	}
	if resp.Usage.InputTokens != 60 || resp.Usage.CacheReadInputTokens != 40 || resp.Usage.PromptInputTokens() != 100 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}
