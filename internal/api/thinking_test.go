package api

import (
	"encoding/json"
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

// TestDropUnsignedThinkingBlocks — an unsigned thinking block cannot be
// replayed (the API requires `signature`), so it is stripped before the wire.
func TestDropUnsignedThinkingBlocks(t *testing.T) {
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, changed := dropUnsignedThinkingBlocks(tt.in)
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
