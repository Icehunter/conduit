// Package commands — status output styling helpers.
//
// Slash commands return Result{Type: "text"} and the TUI renders the text
// as a system message (styleSystemText: italic, colorMuted). On dark
// backgrounds this is too dim. To restore contrast we embed ANSI escape
// sequences directly in the result text — the TUI's render layer leaves
// them untouched (it only adds a "· " prefix and indents continuations).
//
// Use these helpers from any /command handler that wants /doctor-style
// label/value output. Keep label widths aligned so columns line up.
package commands

import "fmt"

// ANSI escape constants used by status output. Embedded directly in command
// result text — the TUI viewport does not interpret or strip them.
const (
	ansiBold   = "\033[1m"
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

// statusRow formats one "Label:  value  hint" row with a bold label of fixed
// width labelW (so multiple rows line up). hint is rendered dim (use ""
// for none).
func statusRow(label, value, hint string, labelW int) string {
	if hint != "" {
		hint = "  " + ansiDim + hint + ansiReset
	}
	return fmt.Sprintf("  %s%-*s%s %s%s\n", ansiBold, labelW, label, ansiReset, value, hint)
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
