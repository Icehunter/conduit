package tui

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/icehunter/conduit/internal/theme"
)

// cellsAfterSurfaceReset renders s through the same path drawString uses and
// returns the parsed cells of the first line.
func cellsAfterSurfaceReset(t *testing.T, s string) uv.Line {
	t.Helper()
	lines := uv.NewStyledString(withSurfaceAfterReset(s)).Lines(ansi.GraphemeWidth)
	if len(lines) == 0 {
		t.Fatal("no lines parsed")
	}
	return lines[0]
}

func TestWithSurfaceAfterReset(t *testing.T) {
	bg := theme.AnsiBG(windowBgHex)
	wantBg := uv.NewStyledString(bg + "x").Lines(ansi.GraphemeWidth)[0][0].Style.Bg

	tests := []struct {
		name  string
		input string
	}{
		// Bubbles' virtual cursor: reverse video + fg colour, then a full reset.
		// Everything after the reset must come back to plain surface bg.
		{"reverse cursor does not leak", "\x1b[7;38;2;255;255;255m▌\x1b[m after"},
		// The original black-bleed case: a reset in the middle of a line.
		{"plain reset keeps surface bg", "\x1b[1mbold\x1b[m after"},
		{"underline does not leak", "\x1b[4mu\x1b[m after"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cells := cellsAfterSurfaceReset(t, tt.input)
			stripped := ansi.Strip(tt.input)
			byteIdx := strings.Index(stripped, "after")
			if byteIdx < 0 {
				t.Fatal("trailing text missing from input")
			}
			idx := ansi.StringWidth(stripped[:byteIdx])
			if idx+len("after") > len(cells) {
				t.Fatalf("trailing text at cell %d overruns %d cells", idx, len(cells))
			}
			for i := idx; i < idx+len("after"); i++ {
				c := cells[i]
				if c.Style.Attrs != 0 {
					t.Errorf("cell %d (%q): attrs = %v, want none", i, c.Content, c.Style.Attrs)
				}
				if c.Style.Underline != uv.UnderlineStyleNone {
					t.Errorf("cell %d (%q): underline = %v, want none", i, c.Content, c.Style.Underline)
				}
				if c.Style.Bg != wantBg {
					t.Errorf("cell %d (%q): bg = %v, want surface %v", i, c.Content, c.Style.Bg, wantBg)
				}
			}
		})
	}
}
