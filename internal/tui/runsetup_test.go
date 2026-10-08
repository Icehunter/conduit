package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/icehunter/conduit/internal/api"
)

func TestSummarizeMessages(t *testing.T) {
	// Tool results live in ResultContent/ResultBlocks, never Text; a result
	// whose 500th byte lands inside a multi-byte rune must not be cut mid-rune.
	hist := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "old"}}},
		{Role: "assistant", Content: []api.ContentBlock{{Type: "tool_use", Name: "Read", Input: map[string]any{"file_path": "/a.go"}}}},
		{Role: "user", Content: []api.ContentBlock{
			{Type: "tool_result", ResultContent: strings.Repeat("é", 400)},
			{Type: "tool_result", IsError: true, ResultBlocks: []api.ContentBlock{{Type: "text", Text: "block text"}}},
		}},
	}
	got := SummarizeMessages(hist, 2)
	if !utf8.ValidString(got) {
		t.Fatal("output is not valid UTF-8")
	}
	for _, want := range []string{`[tool_use Read {"file_path":"/a.go"}]`, "éééé", "[tool_result (error)]\nblock text", "1 earlier messages omitted"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "old") {
		t.Errorf("message outside the window leaked:\n%s", got)
	}
	if SummarizeMessages(nil, 5) != "" {
		t.Error("empty history should render empty")
	}
}
