package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	anthropicJSON = `{"id":"m1","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
	anthropicSSE  = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"usage\":{\"input_tokens\":1}}}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	chatJSON      = `{"id":"c1","model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	chatSSE       = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	responsesJSON = `{"id":"r1","model":"m","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`
	responsesSSE  = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n"
)

// TestRequestHeaders_AllTransports: Config.RequestHeaders reaches every
// transport, streaming or not, wins over ExtraHeaders, and is evaluated once
// per call: the retry after a 401 resends the same values.
func TestRequestHeaders_AllTransports(t *testing.T) {
	transports := []struct {
		name, kind, path, json, sse string
	}{
		{"anthropic messages", "", "/v1/messages", anthropicJSON, anthropicSSE},
		{"chat completions", ProviderKindOpenAICompatible, "/chat/completions", chatJSON, chatSSE},
		{"responses", ProviderKindOpenAIResponses, "/responses", responsesJSON, responsesSSE},
	}
	for _, tr := range transports {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s stream=%v", tr.name, stream), func(t *testing.T) {
				var mu sync.Mutex
				var got []http.Header
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					mu.Lock()
					got = append(got, r.Header.Clone())
					n := len(got)
					mu.Unlock()
					if r.URL.Path != tr.path {
						http.NotFound(w, r)
						return
					}
					if n == 1 {
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, tr.sse)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, tr.json)
				}))
				defer srv.Close()

				calls := 0
				c := NewClient(Config{
					ProviderKind: tr.kind,
					BaseURL:      srv.URL,
					AuthToken:    "tok",
					ExtraHeaders: map[string]string{"X-Initiator": "static", "X-Static": "kept"},
					RequestHeaders: func(req *MessageRequest) map[string]string {
						calls++
						return map[string]string{"x-initiator": "agent", "X-Call": fmt.Sprint(calls), "Authorization": "Bearer hijack"}
					},
					OnAuth401: func(context.Context) error { return nil },
				}, srv.Client())
				req := &MessageRequest{Model: "m", MaxTokens: 8, Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}}}
				if stream {
					s, err := c.StreamMessage(context.Background(), req)
					if err != nil {
						t.Fatalf("StreamMessage: %v", err)
					}
					for {
						if _, err := s.Next(); errors.Is(err, io.EOF) {
							break
						} else if err != nil {
							t.Fatalf("Next: %v", err)
						}
					}
					_ = s.Close()
				} else if _, err := c.CreateMessage(context.Background(), req); err != nil {
					t.Fatalf("CreateMessage: %v", err)
				}

				if calls != 1 {
					t.Errorf("RequestHeaders called %d times, want once per call", calls)
				}
				mu.Lock()
				defer mu.Unlock()
				if len(got) != 2 {
					t.Fatalf("requests = %d, want 401 then retry", len(got))
				}
				for i, h := range got {
					if h.Get("X-Initiator") != "agent" || h.Get("X-Call") != "1" || h.Get("X-Static") != "kept" {
						t.Errorf("request %d headers = %v, want x-initiator agent, X-Call 1, X-Static kept", i+1, h)
					}
				}
				if tr.kind == "" && got[0].Get("Authorization") != "Bearer tok" {
					t.Errorf("Authorization = %q: RequestHeaders must not override locked headers on the Anthropic wire", got[0].Get("Authorization"))
				}
			})
		}
	}
}

// TestRequestHeaders_Unset: without a hook nothing extra is sent.
func TestRequestHeaders_Unset(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, chatJSON)
	}))
	defer srv.Close()
	c := NewClient(Config{ProviderKind: ProviderKindOpenAICompatible, BaseURL: srv.URL, APIKey: "k"}, srv.Client())
	if _, err := c.CreateMessage(context.Background(), &MessageRequest{Model: "m", Hints: &WireHints{RequestClass: "main", SessionID: "s1"}}); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"X-Initiator", "X-Interaction-Id"} {
		if v := got.Get(h); v != "" {
			t.Errorf("%s = %q, want unset", h, v)
		}
	}
	if strings.Contains(fmt.Sprint(got), "s1") {
		t.Errorf("session id leaked into headers: %v", got)
	}
}
