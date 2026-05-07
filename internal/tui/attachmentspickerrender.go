package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	commandPickerSideMargin   = 6
	commandPickerMaxItems     = 11
	commandPickerMaxDescRunes = 88
	commandPickerSelectorW    = 2
)

// renderCommandPicker renders the slash command picker dropdown.
func (m Model) renderCommandPicker() string {
	// The current query (text after "/").
	query := strings.ToLower(strings.TrimPrefix(m.input.Value(), "/"))
	bodyLines := commandPickerBodyLines()

	// Compute visible window around the selected index.
	start := m.cmdSelected - commandPickerMaxItems/2
	if start < 0 {
		start = 0
	}
	end := start + commandPickerMaxItems
	if end > len(m.cmdMatches) {
		end = len(m.cmdMatches)
		start = end - commandPickerMaxItems
		if start < 0 {
			start = 0
		}
	}

	contentW := commandPickerContentWidth(m.width)
	if contentW < 20 {
		contentW = 20
	}

	if len(m.cmdMatches) == 0 {
		lines := []string{}
		displayQuery := strings.TrimPrefix(m.input.Value(), "/")
		if displayQuery == "" {
			lines = append(lines, stylePickerDesc.Render("Type to filter commands."))
		} else {
			lines = append(lines,
				stylePickerDesc.Render(fmt.Sprintf("No commands found for %q.", displayQuery)),
			)
		}
		return renderCommandPickerFrame(lines, commandPickerFooter(0, 0, 0), bodyLines, contentW)
	}

	// Compute name column width from the longest name across all matches so
	// the column stays stable as the user scrolls through results.
	nameColW := 0
	for _, cmd := range m.cmdMatches {
		n := lipgloss.Width("/" + cmd.Name)
		if n > nameColW {
			nameColW = n
		}
	}
	const minDescW = 24
	const gap = 2
	if contentW < commandPickerSelectorW+minDescW+gap+1 {
		contentW = commandPickerSelectorW + minDescW + gap + 1
	}
	maxNameW := contentW - commandPickerSelectorW - minDescW - gap
	if nameColW > maxNameW {
		nameColW = maxNameW
	}
	descMax := contentW - commandPickerSelectorW - nameColW - gap
	if descMax > commandPickerMaxDescRunes {
		descMax = commandPickerMaxDescRunes
	}

	lines := []string{}
	for i := start; i < end; i++ {
		cmd := m.cmdMatches[i]

		// Render name: "/" + name padded to nameColW.
		rawName := "/" + cmd.Name
		rawName = padPlainToWidth(truncatePlainToWidth(rawName, nameColW), nameColW)

		prefix := "  "
		var namePart string
		if i == m.cmdSelected {
			prefix = stylePickerItemSelected.Render("❯ ")
			namePart = highlightMatch(rawName, query, stylePickerItemSelected, stylePickerHighlight)
		} else {
			namePart = highlightMatch(rawName, query, stylePickerItem, stylePickerHighlight)
		}

		desc := truncatePlainToWidth(cmd.Description, descMax)
		line := prefix + namePart + surfaceSpaces(gap) + highlightMatch(desc, query, stylePickerDesc, stylePickerHighlight)
		lines = append(lines, line)
	}

	return renderCommandPickerFrame(lines, commandPickerFooter(start, end, len(m.cmdMatches)), bodyLines, contentW)
}

func commandPickerContentWidth(termWidth int) int {
	outer := commandPickerOuterWidth(termWidth)
	frame := floatingWindowStyle().GetHorizontalFrameSize()
	inner := outer - frame - floatingBodyPadX*2
	if inner < 1 {
		return 1
	}
	return inner
}

func commandPickerOuterWidth(termWidth int) int {
	if termWidth > commandPickerSideMargin*2+floatingCommandSpec.minWidth {
		return termWidth - commandPickerSideMargin*2
	}
	return floatingOuterWidth(termWidth, floatingCommandSpec)
}

func commandPickerBodyLines() int {
	frame := floatingWindowStyle().GetVerticalFrameSize()
	innerH := floatingCommandSpec.maxHeight - frame
	headerH := 1
	lines := innerH - headerH - floatingBodyPadY*2
	if lines < 1 {
		return 1
	}
	return lines
}

func renderCommandPickerFrame(lines []string, footer string, bodyLines, contentW int) string {
	if bodyLines < 1 {
		bodyLines = 1
	}
	if len(lines) > bodyLines-1 {
		lines = lines[:bodyLines-1]
	}
	for len(lines) < bodyLines-1 {
		lines = append(lines, "")
	}
	lines = append(lines, stylePickerDesc.Render(truncatePlainToWidth(footer, contentW)))

	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render("Commands"))
	sb.WriteByte('\n')
	sb.WriteString(strings.Join(lines, "\n"))
	return sb.String()
}

func commandPickerFooter(start, end, total int) string {
	if total <= 0 {
		return "Enter sends unknown command · Esc closes"
	}
	return fmt.Sprintf("↑/↓ select · Tab complete · Enter run · Esc close · %d-%d of %d", start+1, end, total)
}

func truncatePlainToWidth(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	if maxWidth == 1 {
		return "…"
	}
	target := maxWidth - 1
	var out []rune
	width := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if width+rw > target {
			break
		}
		out = append(out, r)
		width += rw
	}
	return string(out) + "…"
}

func padPlainToWidth(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if w := lipgloss.Width(s); w < width {
		return s + strings.Repeat(" ", width-w)
	}
	return s
}

// highlightMatch renders s with every case-insensitive occurrence of query
// highlighted using highlightStyle, and the rest in baseStyle.
// Returns the base-styled string unchanged if query is empty.
func highlightMatch(s, query string, baseStyle, highlightStyle lipgloss.Style) string {
	if query == "" {
		return baseStyle.Render(s)
	}
	lower := strings.ToLower(s)
	var out strings.Builder
	pos := 0
	for {
		idx := strings.Index(lower[pos:], query)
		if idx < 0 {
			out.WriteString(baseStyle.Render(s[pos:]))
			break
		}
		abs := pos + idx
		if abs > pos {
			out.WriteString(baseStyle.Render(s[pos:abs]))
		}
		out.WriteString(highlightStyle.Render(s[abs : abs+len(query)]))
		pos = abs + len(query)
		if pos >= len(s) {
			break
		}
	}
	return out.String()
}

// renderAtPicker renders the @ file completion picker above the input box.
// Returns "" when no matches are active.
func (m Model) renderAtPicker() string {
	if len(m.atMatches) == 0 {
		return ""
	}
	const maxItems = 8
	cwd, _ := os.Getwd()
	start := m.atSelected - maxItems/2
	if start < 0 {
		start = 0
	}
	end := start + maxItems
	if end > len(m.atMatches) {
		end = len(m.atMatches)
		start = end - maxItems
		if start < 0 {
			start = 0
		}
	}

	contentW := floatingInnerWidth(m.width, floatingPickerSpec)
	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render("Files") + "\n\n")
	for i := start; i < end; i++ {
		path := m.atMatches[i]
		icon := "+"
		if info, err := os.Stat(filepath.Join(cwd, path)); err == nil && info.IsDir() {
			icon = "◇"
			path += "/"
		}
		path = truncateMiddle(path, contentW-4)
		line := fmt.Sprintf("%s %s", icon, path)
		if i == m.atSelected {
			line = stylePickerItemSelected.Render("❯ " + line)
		} else {
			line = stylePickerItem.Render("  " + line)
		}
		sb.WriteString(line)
		if i < end-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
