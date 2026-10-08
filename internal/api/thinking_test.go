package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestContentBlockMarshal_ThinkingFieldAlwaysPresent — the API rejects a
// thinking block that has no `thinking` key with
// `messages.N.content.M.thinking.thinking: Field required`. Fable emits signed
// thinking blocks with no visible reasoning text, so the empty case is real.
func TestContentBlockMarshal_ThinkingFieldAlwaysPresent(t *testing.T) {
	tests := []struct {
		name         string
		block        ContentBlock
		wantThinking string
		wantPresent  bool
	}{
		{
			name:         "signed block with no text still emits thinking",
			block:        ContentBlock{Type: "thinking", Signature: "CAQSkBUKEAgR"},
			wantThinking: "",
			wantPresent:  true,
		},
		{
			name:         "signed block with text",
			block:        ContentBlock{Type: "thinking", Thinking: "reasoning", Signature: "sig"},
			wantThinking: "reasoning",
			wantPresent:  true,
		},
		{
			name:        "non-thinking block omits the key",
			block:       ContentBlock{Type: "text", Text: "hi"},
			wantPresent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(tt.block)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got, present := m["thinking"]
			if present != tt.wantPresent {
				t.Fatalf("thinking present = %v; want %v (json: %s)", present, tt.wantPresent, raw)
			}
			if tt.wantPresent && got != tt.wantThinking {
				t.Errorf("thinking = %q; want %q", got, tt.wantThinking)
			}
		})
	}
}

// TestDropNonAnthropicThinkingBlocks — an unsigned thinking block cannot be
// replayed (the API requires `signature`), so it is stripped before the wire.
func TestDropNonAnthropicThinkingBlocks(t *testing.T) {
	tests := []struct {
		name        string
		in          []Message
		want        []Message
		wantChanged bool
	}{
		{
			name: "all signed — untouched",
			in: []Message{{Role: "assistant", Content: []ContentBlock{
				{Type: "thinking", Signature: "sig"},
				{Type: "text", Text: "hi"},
			}}},
			want: []Message{{Role: "assistant", Content: []ContentBlock{
				{Type: "thinking", Signature: "sig"},
				{Type: "text", Text: "hi"},
			}}},
			wantChanged: false,
		},
		{
			name: "unsigned thinking dropped, siblings kept",
			in: []Message{{Role: "assistant", Content: []ContentBlock{
				{Type: "thinking", Thinking: "unsigned"},
				{Type: "text", Text: "hi"},
			}}},
			want: []Message{{Role: "assistant", Content: []ContentBlock{
				{Type: "text", Text: "hi"},
			}}},
			wantChanged: true,
		},
		{
			name: "message left empty is dropped entirely",
			in: []Message{
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "q"}}},
				{Role: "assistant", Content: []ContentBlock{{Type: "thinking", Thinking: "unsigned"}}},
			},
			want: []Message{
				{Role: "user", Content: []ContentBlock{{Type: "text", Text: "q"}}},
			},
			wantChanged: true,
		},
		{
			name: "responses-signed thinking dropped, anthropic-signed kept",
			in: []Message{{Role: "assistant", Content: []ContentBlock{
				{Type: "thinking", Thinking: "codex", Signature: encodeReasoningSignature("rs_1", "ENC")},
				{Type: "thinking", Thinking: "claude", Signature: "sig"},
				{Type: "text", Text: "hi"},
			}}},
			want: []Message{{Role: "assistant", Content: []ContentBlock{
				{Type: "thinking", Thinking: "claude", Signature: "sig"},
				{Type: "text", Text: "hi"},
			}}},
			wantChanged: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := dropNonAnthropicThinkingBlocks(tt.in)
			if changed != tt.wantChanged {
				t.Fatalf("changed = %v; want %v", changed, tt.wantChanged)
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tt.want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("messages =\n %s\nwant\n %s", gotJSON, wantJSON)
			}
		})
	}
}

// TestSanitizeAnthropicRequest_StripsUnsignedThinking — the sanitizer runs on
// both the streaming and non-streaming paths, so the drop must happen there
// even when no model suffix or cache-scope rewrite is in play.
func TestSanitizeAnthropicRequest_StripsUnsignedThinking(t *testing.T) {
	req := &MessageRequest{
		Model: "claude-fable-5-1",
		Messages: []Message{{Role: "assistant", Content: []ContentBlock{
			{Type: "thinking", Thinking: "unsigned"},
			{Type: "text", Text: "hi"},
		}}},
	}
	got := sanitizeAnthropicRequest(req, Config{})
	if len(got.Messages[0].Content) != 1 || got.Messages[0].Content[0].Type != "text" {
		t.Fatalf("content = %+v; want only the text block", got.Messages[0].Content)
	}
	if len(req.Messages[0].Content) != 2 {
		t.Errorf("input mutated: %+v", req.Messages[0].Content)
	}
}

// TestAnthropicWire_NeverSendsResponsesSignature: a history that crossed from
// Codex (resumed session, model switch) must not put oai-resp: signatures on
// the Anthropic wire, streaming or not, including a Copilot-style client with
// StripCacheControlScope.
func TestAnthropicWire_NeverSendsResponsesSignature(t *testing.T) {
	history := []Message{
		{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}},
		{Role: "assistant", Content: []ContentBlock{
			{Type: "thinking", Thinking: "s", Signature: encodeReasoningSignature("rs_1", "ENCRYPTED")},
			{Type: "tool_use", ID: "t1", Name: "Read", Input: map[string]any{}},
		}},
		{Role: "user", Content: []ContentBlock{{Type: "tool_result", ToolUseID: "t1", ResultContent: "x"}}},
	}
	for _, tt := range []struct {
		name   string
		cfg    Config
		stream bool
	}{
		{"create", Config{}, false},
		{"stream", Config{}, true},
		{"copilot messages route", Config{StripCacheControlScope: true}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var body string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				body = string(b)
				if tt.stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			}))
			defer srv.Close()
			tt.cfg.BaseURL, tt.cfg.AuthToken = srv.URL, "t"
			c := NewClient(tt.cfg, srv.Client())
			req := &MessageRequest{Model: "claude-sonnet-4-6", MaxTokens: 16, Messages: history}
			if tt.stream {
				st, err := c.StreamMessage(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				_ = st.Close()
			} else if _, err := c.CreateMessage(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(body, "oai-resp:") || strings.Contains(body, "ENCRYPTED") {
				t.Errorf("responses signature on the Anthropic wire: %s", body)
			}
			if !strings.Contains(body, "tool_use") {
				t.Errorf("tool_use lost: %s", body)
			}
		})
	}
}
