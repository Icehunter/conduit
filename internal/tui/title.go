package tui

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// setWindowTitleCmd emits an OSC 2 escape sequence to set the terminal title.
// Recognised by xterm, macOS Terminal, iTerm2, Alacritty, kitty, WezTerm,
// Windows Terminal, and conhost >= Win10 1809.
func setWindowTitleCmd(title string) tea.Cmd {
	safe := sanitizeTitle(title)
	return func() tea.Msg {
		fmt.Fprintf(os.Stdout, "\x1b]2;%s\x07", safe)
		return nil
	}
}

// clearWindowTitleCmd resets the terminal title to "conduit".
func clearWindowTitleCmd() tea.Cmd {
	return setWindowTitleCmd("conduit")
}

func sanitizeTitle(s string) string {
	// Strip control characters; clamp to 120 chars.
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == 0x9c || r == 0x9d {
			return -1
		}
		return r
	}, s)
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}
