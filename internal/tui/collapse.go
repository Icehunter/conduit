package tui

import (
	"strings"
	"unicode"
)

// collapsedLines is how many lines of tool or system output show before the
// rest folds behind a "lines hidden" hint.
const collapsedLines = 10

// trimOutput removes surrounding blank lines and trailing whitespace but keeps
// the first line's indentation (Read output starts with padded line numbers).
func trimOutput(s string) string {
	return strings.TrimLeft(strings.TrimRightFunc(s, unicode.IsSpace), "\r\n")
}

// outputLines splits trimmed output into lines.
func outputLines(s string) []string { return strings.Split(trimOutput(s), "\n") }

// canCollapse reports whether n lines should fold. A hint that hides a single
// line costs as much space as that line, so one extra line is shown instead.
func canCollapse(n int) bool { return n > collapsedLines+1 }
