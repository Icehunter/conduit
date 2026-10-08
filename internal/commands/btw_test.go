package commands

import "testing"

func TestBtwCommand(t *testing.T) {
	r := New()
	RegisterMiscCommands(r)
	tests := []struct {
		name     string
		input    string
		wantType string
		wantText string
	}{
		{"question", "/btw what does this do?", "btw", "what does this do?"},
		{"trimmed", "/btw   padded  ", "btw", "padded"},
		{"missing question", "/btw", "error", "Usage: /btw <question>"},
		{"blank question", "/btw    ", "error", "Usage: /btw <question>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, ok := r.Dispatch(tt.input)
			if !ok {
				t.Fatal("not dispatched")
			}
			if res.Type != tt.wantType || res.Text != tt.wantText {
				t.Errorf("got %q %q, want %q %q", res.Type, res.Text, tt.wantType, tt.wantText)
			}
		})
	}
}
