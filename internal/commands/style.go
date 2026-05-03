// Package commands — status output styling helpers.
//
// Slash commands return Result{Type: "text"} and the TUI renders the text
// as a system message. To make labeled status output readable on dark
// backgrounds we embed ANSI escape sequences directly. The TUI render
// layer renders the "· " prefix dim and lets these escapes show through.
//
// All ANSI escape constants derive from theme.Active() and rebuild on
// theme switch via theme.OnChange.
package commands

import (
	"fmt"
	"sync"

	"github.com/icehunter/conduit/internal/theme"
)

var (
	styleMu       sync.RWMutex
	ansiLabel     string // bold + Secondary color (label text)
	ansiSuccess   string // Success color
	ansiDanger    string // Danger color
	ansiAccent    string // Accent color
	ansiInfo      string // Info color
	ansiSecondary string // Secondary color (no bold)
)

// Stable references to theme constants — these don't change with palette swap.
const (
	ansiBold  = theme.AnsiBold
	ansiDim   = theme.AnsiDim
	ansiReset = theme.AnsiReset
)

func rebuildStyles() {
	p := theme.Active()
	styleMu.Lock()
	defer styleMu.Unlock()
	ansiLabel = ansiBold + theme.AnsiFG(p.Secondary)
	ansiSuccess = theme.AnsiFG(p.Success)
	ansiDanger = theme.AnsiFG(p.Danger)
	ansiAccent = theme.AnsiFG(p.Accent)
	ansiInfo = theme.AnsiFG(p.Info)
	ansiSecondary = theme.AnsiFG(p.Secondary)
}

func init() {
	rebuildStyles()
	theme.OnChange(rebuildStyles)
}

// statusTitle formats a heading line ("Conduit diagnostics", "Session", etc.).
func statusTitle(s string) string {
	return ansiBold + s + ansiReset + "\n\n"
}

// statusRow formats one "Label  value  (hint)" row with a bold-secondary
// label of fixed width labelW (so multiple rows line up), default-foreground
// value, and dim hint in parens.
func statusRow(label, value, hint string, labelW int) string {
	styleMu.RLock()
	lbl := ansiLabel
	styleMu.RUnlock()
	if hint != "" {
		hint = "  " + ansiDim + "(" + hint + ")" + ansiReset
	}
	return fmt.Sprintf("  %s%-*s%s %s%s\n", lbl, labelW, label, ansiReset, value, hint)
}

// statusCheck returns a green ✓ or red ✗ marker (theme Success/Danger).
func statusCheck(ok bool) string {
	styleMu.RLock()
	defer styleMu.RUnlock()
	if ok {
		return ansiSuccess + "✓" + ansiReset
	}
	return ansiDanger + "✗" + ansiReset
}

// statusValue accents a value with theme.Info (used for IDs, paths, counts).
func statusValue(s string) string {
	styleMu.RLock()
	defer styleMu.RUnlock()
	return ansiInfo + s + ansiReset
}
