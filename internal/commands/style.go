// Package commands — status output styling helpers.
//
// Slash commands return Result{Type: "text"} and the TUI renders the text
// as a system message. To make labeled status output readable on dark
// backgrounds we embed ANSI escape sequences directly. The TUI render
// layer detects ANSI escapes and passes them through unmodified instead
// of wrapping in styleSystemText (which would force italic+muted on top).
//
// Use these helpers from any /command handler that wants /doctor-style
// label/value output.
package commands

import "fmt"

// ANSI escape constants used by status output. Embedded directly in
// command result text — the TUI viewport detects and passes them through.
//
// Labels use bold + colorMuted (#636D7E) — bold grey for hierarchy.
// Values use the terminal default foreground (no styling) so they read
// naturally against the dimmer labels.
const (
	ansiBold   = "\033[1m"
	ansiLabel  = "\033[1;38;2;99;109;126m" // bold + #636D7E (colorMuted)
	ansiGreen  = "\033[32m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
	ansiDim    = "\033[2m"
	ansiReset  = "\033[0m"
)

// statusTitle formats a heading line ("Conduit diagnostics", "Session", etc.).
func statusTitle(s string) string {
	return ansiBold + s + ansiReset + "\n\n"
}

// statusRow formats one "Label  value  (hint)" row with a bold-grey label of
// fixed width labelW (so multiple rows line up), default-foreground value,
// and dim hint in parens.
func statusRow(label, value, hint string, labelW int) string {
	if hint != "" {
		hint = "  " + ansiDim + "(" + hint + ")" + ansiReset
	}
	return fmt.Sprintf("  %s%-*s%s %s%s\n", ansiLabel, labelW, label, ansiReset, value, hint)
}

// statusCheck returns a green ✓ or red ✗ marker.
func statusCheck(ok bool) string {
	if ok {
		return ansiGreen + "✓" + ansiReset
	}
	return ansiRed + "✗" + ansiReset
}

// statusValue accents a value in cyan (used for IDs, paths, counts).
func statusValue(s string) string {
	return ansiCyan + s + ansiReset
}
