package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTokenizeLine_KeepsText(t *testing.T) {
	tests := []struct {
		name string
		line string
	}{
		{"unterminated string ending in backslash", `path := "C:\Users\me\`},
		{"single quote ending in backslash", `x = '\`},
		{"escaped quote inside string", `s := "a\"b"`},
		{"unterminated backtick", "raw := `abc"},
		{"lone backslash", `\`},
		{"empty", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ansi.Strip(tokenizeLine(tt.line, "go"))
			if got != tt.line {
				t.Errorf("tokenizeLine(%q) text = %q, want the input unchanged", tt.line, got)
			}
		})
	}
}

func FuzzTokenizeLine(f *testing.F) {
	for _, seed := range []string{`"a\`, `'\`, "`x", `"\"\\"`, "0x1F_00", "héllo \"wörld\\"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, line string) {
		if strings.ContainsAny(line, "\x1b\n\r\t") {
			t.Skip("escapes and newlines never reach one highlighted line; lipgloss expands tabs")
		}
		out := tokenizeLine(line, "go")
		if got, want := ansi.Strip(out), string([]rune(line)); got != want {
			t.Errorf("text = %q, want %q", got, want)
		}
	})
}
