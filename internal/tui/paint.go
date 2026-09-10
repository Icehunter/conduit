package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/theme"
)

// fullReset is the SGR reset lipgloss v2 and ultraviolet emit. It is always
// the bare "\x1b[m" form, never "\x1b[0m".
const fullReset = "\x1b[m"

// withSurfaceReset rewrites every full reset in s so that the surface
// background (and optional foreground) is reapplied immediately after it.
//
// The reset must stay a full reset. An earlier "soft" variant (22;23;39)
// cleared bold/italic/fg but left reverse video, underline and blink set, so
// the textarea's reverse-video cursor bled across the rest of its row.
func withSurfaceReset(s, bgEsc, fgEsc string) string {
	return strings.ReplaceAll(s, fullReset, fullReset+bgEsc+fgEsc)
}

// paintApp paints the shared surface background across the visible TUI
// region. The floating chrome intentionally uses its own dark surface even
// for ANSI themes, so repaint after lipgloss resets to avoid black holes
// behind styled text and wrapped descriptions.
//
// Two-phase paint:
//  1. Reapply the surface bg after every internal reset
//  2. Pad each line to width and wrap in styleAppSurface
func paintApp(w, h int, content string) string {
	if w <= 0 || h <= 0 {
		return content
	}
	bg := theme.AnsiBG(windowBgHex)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		line = withSurfaceReset(line, bg, "")
		visW := lipgloss.Width(line)
		if visW < w {
			line += surfaceSpaces(w - visW)
		}
		lines[i] = bg + line + fullReset
	}
	out := strings.Join(lines, "\n")
	return styleAppSurface.Width(w).Height(h).Render(out)
}
