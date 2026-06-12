package agent

import (
	"testing"

	"github.com/icehunter/conduit/internal/api"
)

func TestLiveZoneBoundary(t *testing.T) {
	ephemeral := &api.CacheControl{Type: "ephemeral"}

	tests := []struct {
		name string
		msgs []api.Message
		want int
	}{
		{
			name: "no messages returns 0",
			msgs: nil,
			want: 0,
		},
		{
			name: "no cache_control anywhere returns 0",
			msgs: []api.Message{
				{Role: "user", Content: []api.ContentBlock{
					{Type: "text", Text: "hello"},
				}},
				{Role: "assistant", Content: []api.ContentBlock{
					{Type: "text", Text: "world"},
				}},
			},
			want: 0,
		},
		{
			name: "only first message has cache_control returns 1",
			msgs: []api.Message{
				{Role: "user", Content: []api.ContentBlock{
					{Type: "text", Text: "hello", CacheControl: ephemeral},
				}},
				{Role: "assistant", Content: []api.ContentBlock{
					{Type: "text", Text: "world"},
				}},
			},
			// Protect messages[0] inclusive → boundary = last+1 = 0+1 = 1.
			want: 1,
		},
		{
			name: "second message has cache_control returns 2",
			msgs: []api.Message{
				{Role: "user", Content: []api.ContentBlock{
					{Type: "text", Text: "first"},
				}},
				{Role: "assistant", Content: []api.ContentBlock{
					{Type: "text", Text: "second", CacheControl: ephemeral},
				}},
				{Role: "user", Content: []api.ContentBlock{
					{Type: "text", Text: "third"},
				}},
			},
			// last breakpoint at index 1 → boundary = 2.
			want: 2,
		},
		{
			name: "two breakpoints — returns boundary past the last one",
			msgs: []api.Message{
				{Role: "user", Content: []api.ContentBlock{
					{Type: "text", Text: "a"},
				}},
				{Role: "assistant", Content: []api.ContentBlock{
					{Type: "text", Text: "b", CacheControl: ephemeral}, // first breakpoint at 1
				}},
				{Role: "user", Content: []api.ContentBlock{
					{Type: "text", Text: "c"},
				}},
				{Role: "assistant", Content: []api.ContentBlock{
					{Type: "text", Text: "d"},
				}},
				{Role: "user", Content: []api.ContentBlock{
					{Type: "tool_result", ToolUseID: "t1", CacheControl: ephemeral}, // last breakpoint at 4
				}},
				{Role: "assistant", Content: []api.ContentBlock{
					{Type: "text", Text: "live zone"},
				}},
			},
			// last breakpoint at index 4 → boundary = 5. Messages 2 and 3
			// (between the two breakpoints) are protected because they form
			// part of the cached prefix up to index 4.
			want: 5,
		},
		{
			name: "last message has cache_control returns len(msgs)",
			msgs: []api.Message{
				{Role: "user", Content: []api.ContentBlock{
					{Type: "text", Text: "a"},
				}},
				{Role: "user", Content: []api.ContentBlock{
					{Type: "text", Text: "b"},
				}},
				{Role: "user", Content: []api.ContentBlock{
					{Type: "tool_result", ToolUseID: "t1", CacheControl: ephemeral},
				}},
			},
			// last breakpoint at index 2 → boundary = 3 = len(msgs).
			want: 3,
		},
		{
			name: "cache_control on second of multiple blocks in message",
			msgs: []api.Message{
				{Role: "user", Content: []api.ContentBlock{
					{Type: "text", Text: "no cache"},
				}},
				{Role: "assistant", Content: []api.ContentBlock{
					{Type: "text", Text: "no cache"},
					{Type: "tool_use", ID: "t1", CacheControl: ephemeral},
				}},
			},
			// last breakpoint at index 1 → boundary = 2.
			want: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := liveZoneBoundary(tt.msgs)
			if got != tt.want {
				t.Errorf("liveZoneBoundary() = %d; want %d", got, tt.want)
			}
		})
	}
}
