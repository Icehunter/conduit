package transcript

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/icehunter/conduit/internal/api"
)

func txt(role, s string) api.Message {
	return api.Message{Role: role, Content: []api.ContentBlock{{Type: "text", Text: s}}}
}

func TestRender(t *testing.T) {
	long := strings.Repeat("é", 700)
	tests := []struct {
		name string
		msgs []api.Message
		o    Options
		want []string
		not  []string
	}{
		{"empty", nil, Options{}, nil, []string{"---"}},
		{"text roles", []api.Message{txt("user", "hi"), txt("assistant", "yo")}, Options{},
			[]string{"USER:\nhi", "ASSISTANT:\nyo"}, []string{"omitted"}},
		{"tool call args", []api.Message{{Role: "assistant", Content: []api.ContentBlock{
			{Type: "tool_use", Name: "Bash", Input: map[string]any{"command": "ls", "a": 1}}}}}, Options{},
			[]string{`[tool_use Bash {"a":1,"command":"ls"}]`}, nil},
		{"tool args truncated", []api.Message{{Role: "assistant", Content: []api.ContentBlock{
			{Type: "tool_use", Name: "W", Input: map[string]any{"c": strings.Repeat("x", 500)}}}}}, Options{ToolInputChars: 20},
			[]string{"…]"}, nil},
		{"result ok", []api.Message{{Role: "user", Content: []api.ContentBlock{
			{Type: "tool_result", ResultContent: "out"}}}}, Options{}, []string{"[tool_result]\nout"}, []string{"(error)"}},
		{"result error", []api.Message{{Role: "user", Content: []api.ContentBlock{
			{Type: "tool_result", IsError: true, ResultContent: "boom"}}}}, Options{}, []string{"[tool_result (error)]\nboom"}, nil},
		{"result blocks", []api.Message{{Role: "user", Content: []api.ContentBlock{
			{Type: "tool_result", ResultBlocks: []api.ContentBlock{{Type: "text", Text: "a"}, {Type: "image"}, {Type: "text", Text: "b"}}}}}},
			Options{}, []string{"a\n[image]\nb"}, nil},
		{"image and thinking", []api.Message{{Role: "user", Content: []api.ContentBlock{
			{Type: "image"}, {Type: "thinking", Thinking: "secret"}}}}, Options{}, []string{"[image]"}, []string{"secret"}},
		{"rune safe result", []api.Message{{Role: "user", Content: []api.ContentBlock{
			{Type: "tool_result", ResultContent: long}}}}, Options{ToolResultChars: 501}, []string{"…"}, nil},
		{"lastN marker", []api.Message{txt("user", "a"), txt("user", "b"), txt("user", "c")}, Options{LastN: 1},
			[]string{"[… 2 earlier messages omitted …]", "\nc"}, []string{"\na\n"}},
		{"maxchars drops oldest", []api.Message{txt("user", strings.Repeat("a", 100)), txt("user", "newest")}, Options{MaxChars: 40},
			[]string{"[… 1 earlier messages omitted …]", "newest"}, []string{"aaaa"}},
		{"maxchars truncates sole newest", []api.Message{txt("user", strings.Repeat("é", 100))}, Options{MaxChars: 30},
			[]string{"…"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Render(tt.msgs, tt.o)
			if !utf8.ValidString(got) {
				t.Fatalf("invalid UTF-8 output")
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q in:\n%s", w, got)
				}
			}
			for _, n := range tt.not {
				if strings.Contains(got, n) {
					t.Errorf("unexpected %q in:\n%s", n, got)
				}
			}
		})
	}
}

func TestRender_MaxCharsBound(t *testing.T) {
	var msgs []api.Message
	for i := 0; i < 50; i++ {
		msgs = append(msgs, txt("user", strings.Repeat("ü", 40)))
	}
	got := Render(msgs, Options{MaxChars: 500})
	body := got[strings.Index(got, "\n")+1:]
	if n := utf8.RuneCountInString(body); n > 500 {
		t.Errorf("body is %d runes, want <= 500", n)
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 0, "abc"},
		{"abc", 3, "abc"},
		{"abcd", 3, "abc…"},
		{"日本語です", 3, "日本語…"},
		{"日本語", 3, "日本語"},
	}
	for _, tt := range tests {
		if got := Truncate(tt.in, tt.n); got != tt.want {
			t.Errorf("Truncate(%q,%d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}
