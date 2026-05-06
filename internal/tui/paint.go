package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/theme"
)

// paintApp paints the shared surface background across the visible TUI
// region. The floating chrome intentionally uses its own dark surface even
// for ANSI themes, so repaint after lipgloss resets to avoid black holes
// behind styled text and wrapped descriptions.
//
// Two-phase paint:
//  1. Replace internal "\x1b[0m" with soft reset + bg reapply
//  2. Pad each line to width and wrap in styleAppSurface
func paintApp(w, h int, content string) string {
	if w <= 0 || h <= 0 {
		return content
	}
	bg := theme.AnsiBG(windowBgHex)
	const fullReset = "\x1b[0m"
	const softReset = "\x1b[22;23;39m"
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		line = strings.ReplaceAll(line, fullReset, softReset+bg)
		visW := lipgloss.Width(line)
		if visW < w {
			line += surfaceSpaces(w - visW)
		}
		lines[i] = bg + line + fullReset
	}
	out := strings.Join(lines, "\n")
	return styleAppSurface.Width(w).Height(h).Render(out)
}
