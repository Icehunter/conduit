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
			name: "first message has cache_control returns 0",
			msgs: []api.Message{
				{Role: "user", Content: []api.ContentBlock{
					{Type: "text", Text: "hello", CacheControl: ephemeral},
				}},
				{Role: "assistant", Content: []api.ContentBlock{
					{Type: "text", Text: "world"},
				}},
			},
			want: 0,
		},
		{
			name: "second message has cache_control returns 1",
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
			want: 1,
		},
		{
			name: "last message has cache_control returns last index",
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
			want: 2,
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
			want: 1,
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
