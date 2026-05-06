package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
)

// renderCommandPicker renders the slash command picker dropdown.
func (m Model) renderCommandPicker() string {
	const maxItems = 8

	// The current query (text after "/").
	query := strings.ToLower(strings.TrimPrefix(m.input.Value(), "/"))

	// Compute visible window around the selected index.
	start := m.cmdSelected - maxItems/2
	if start < 0 {
		start = 0
	}
	end := start + maxItems
	if end > len(m.cmdMatches) {
		end = len(m.cmdMatches)
		start = end - maxItems
		if start < 0 {
			start = 0
		}
	}

	contentW := floatingInnerWidth(m.width, floatingCommandSpec) - floatingBodyPadX*2
	if contentW < 20 {
		contentW = 20
	}

	if len(m.cmdMatches) == 0 {
		var sb strings.Builder
		sb.WriteString(styleStatusAccent.Render("Commands") + "\n\n")
		displayQuery := strings.TrimPrefix(m.input.Value(), "/")
		if displayQuery == "" {
			sb.WriteString(stylePickerDesc.Render("Type to filter commands."))
		} else {
			sb.WriteString(stylePickerDesc.Render(fmt.Sprintf("No commands found for %q.", displayQuery)))
			sb.WriteByte('\n')
			sb.WriteString(stylePickerDesc.Render("Enter sends it as an unknown command · Esc closes"))
		}
		return sb.String()
	}

	// Compute name column width from the longest name across all matches so
	// the column stays stable as the user scrolls through results.
	nameColW := 0
	for _, cmd := range m.cmdMatches {
		n := len([]rune(cmd.Name)) + 1 // +1 for leading "/"
		if n > nameColW {
			nameColW = n
		}
	}
	const minDescW = 20
	const gap = 2
	if contentW < minDescW+gap+1 {
		contentW = minDescW + gap + 1
	}
	if nameColW > contentW-minDescW-gap {
		nameColW = contentW - minDescW - gap
	}
	descMax := contentW - nameColW - gap
	indent := strings.Repeat(" ", nameColW+gap)

	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render("Commands") + "\n\n")
	for i := start; i < end; i++ {
		if i > start {
			sb.WriteByte('\n')
		}
		cmd := m.cmdMatches[i]

		// Render name: "/" + name padded to nameColW.
		rawName := "/" + cmd.Name
		runes := []rune(rawName)
		if len(runes) > nameColW {
			runes = runes[:nameColW]
		}
		rawName = string(runes) + strings.Repeat(" ", nameColW-len(runes))

		prefix := "  "
		var namePart string
		if i == m.cmdSelected {
			prefix = stylePickerItemSelected.Render("❯ ")
			namePart = highlightMatch(rawName, query, stylePickerItemSelected, stylePickerHighlight)
		} else {
			namePart = highlightMatch(rawName, query, stylePickerItem, stylePickerHighlight)
		}

		// Word-wrap description so it flows to additional lines instead of being cut off.
		descLines := cmdDescWrap(cmd.Description, descMax)
		sb.WriteString(prefix + namePart + surfaceSpaces(gap) + highlightMatch(descLines[0], query, stylePickerDesc, stylePickerHighlight))
		for _, dl := range descLines[1:] {
			sb.WriteByte('\n')
			sb.WriteString(surfaceSpaces(2+lipgloss.Width(indent)) + highlightMatch(dl, query, stylePickerDesc, stylePickerHighlight))
		}
	}

	return sb.String()
}

// cmdDescWrap splits a description into lines of at most maxW runes, breaking
// on word boundaries. Always returns at least one element.
func cmdDescWrap(s string, maxW int) []string {
	if maxW <= 0 || len([]rune(s)) <= maxW {
		return []string{s}
	}
	var lines []string
	words := strings.Fields(s)
	var cur strings.Builder
	for _, w := range words {
		wlen := len([]rune(w))
		if cur.Len() == 0 {
			cur.WriteString(w)
		} else if cur.Len()+1+wlen <= maxW {
			cur.WriteByte(' ')
			cur.WriteString(w)
		} else {
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(w)
		}
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	if len(lines) == 0 {
		return []string{s}
	}
	return lines
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
