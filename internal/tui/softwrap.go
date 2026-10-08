package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// joinSelectedRows stitches selected screen rows back into text. parts are the
// selected slices of each row and fulls the complete plain rows they came
// from. A boundary is a soft wrap, joined with one space, when the renderer
// must have broken there: the next row starts at the left margin, does not
// start a new block, and its first word would not have fit on the current row
// within limit columns.
// Anything else keeps its newline, so an unrecognised layout degrades to the
// old behaviour rather than gluing lines together.
func joinSelectedRows(parts, fulls []string, limit int) string {
	var sb strings.Builder
	for i, part := range parts {
		sb.WriteString(strings.TrimRight(part, " "))
		if i == len(parts)-1 {
			break
		}
		if isSoftWrap(fulls[i], fulls[i+1], limit) {
			sb.WriteByte(' ')
			parts[i+1] = strings.TrimLeft(parts[i+1], " ")
			continue
		}
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func isSoftWrap(cur, next string, limit int) bool {
	cur = strings.TrimRight(cur, " ")
	nextTrim := strings.TrimLeft(next, " ")
	if cur == "" || nextTrim == "" || limit <= 0 || startsBlockRow(nextTrim) {
		return false
	}
	// Wrapped continuations start at the left margin. An indented row is a
	// code line, a nested list item or a quote, so its newline is real.
	if nextTrim != next {
		return false
	}
	word := nextTrim
	if i := strings.IndexByte(word, ' '); i >= 0 {
		word = word[:i]
	}
	return ansi.StringWidth(cur)+1+ansi.StringWidth(word) > limit
}

// startsBlockRow reports whether a (left-trimmed) row begins a list item,
// heading, quote, table row, or code fence rather than continuing a paragraph.
func startsBlockRow(row string) bool {
	switch {
	case strings.HasPrefix(row, "- "), strings.HasPrefix(row, "* "),
		strings.HasPrefix(row, "+ "), strings.HasPrefix(row, "• "),
		strings.HasPrefix(row, "#"), strings.HasPrefix(row, ">"),
		strings.HasPrefix(row, "|"), strings.HasPrefix(row, "```"),
		strings.HasPrefix(row, "│"), strings.HasPrefix(row, "├"), strings.HasPrefix(row, "╰"):
		return true
	}
	i := 0
	for i < len(row) && i < 4 && unicode.IsDigit(rune(row[i])) {
		i++
	}
	return i > 0 && i < len(row) && (row[i] == '.' || row[i] == ')') && i+1 < len(row) && row[i+1] == ' '
}
