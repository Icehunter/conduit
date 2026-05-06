package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// renderMarkdown does lightweight GFM rendering with syntax highlighting.
// Supported: headings, bold, italic, strikethrough, inline code, fenced code,
// bullet/ordered lists, task lists, blockquotes, tables, horizontal rules.
// width is the inner usable width (after outer padding is removed).
func renderMarkdown(text string, width int) string {
	lines := strings.Split(text, "\n")
	var out strings.Builder
	inCode := false
	var codeBuf strings.Builder
	var codeLang string

	// tableBuf collects consecutive table lines for block rendering.
	var tableBuf []string
	flushTable := func() {
		if len(tableBuf) > 0 {
			out.WriteString(renderTable(tableBuf, width))
			out.WriteByte('\n')
			tableBuf = nil
		}
	}

	for _, line := range lines {
		// Fenced code block toggle.
		if strings.HasPrefix(line, "```") {
			flushTable()
			if inCode {
				code := strings.TrimRight(codeBuf.String(), "\n")
				out.WriteString(renderCodeBlock(code, codeLang, width))
				out.WriteByte('\n')
				codeBuf.Reset()
				codeLang = ""
				inCode = false
			} else {
				codeLang = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "```")))
				inCode = true
			}
			continue
		}
		if inCode {
			codeBuf.WriteString(line)
			codeBuf.WriteByte('\n')
			continue
		}

		// Table detection: lines starting with "|".
		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			tableBuf = append(tableBuf, line)
			continue
		}
		flushTable()

		out.WriteString(renderLine(line, width))
		out.WriteByte('\n')
	}
	flushTable()
	if inCode && codeBuf.Len() > 0 {
		code := strings.TrimRight(codeBuf.String(), "\n")
		out.WriteString(renderCodeBlock(code, codeLang, width))
		out.WriteByte('\n')
	}
	return strings.TrimRight(out.String(), "\n")
}

// renderTable renders GFM table lines as aligned columns.
func renderTable(lines []string, width int) string {
	// Parse rows, skipping separator lines (|---|---|).
	var rows [][]string
	for _, line := range lines {
		// Separator row: only contains |, -, :, space.
		trimmed := strings.TrimSpace(line)
		isSep := true
		for _, r := range trimmed {
			if r != '|' && r != '-' && r != ':' && r != ' ' {
				isSep = false
				break
			}
		}
		if isSep && strings.Contains(trimmed, "-") {
			continue
		}
		cells := splitTableRow(line)
		if len(cells) > 0 {
			rows = append(rows, cells)
		}
	}
	if len(rows) == 0 {
		return ""
	}

	// Find max columns and column widths.
	cols := 0
	for _, row := range rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	colWidths := make([]int, cols)
	for _, row := range rows {
		for i, cell := range row {
			if i < cols && len(cell) > colWidths[i] {
				colWidths[i] = len(cell)
			}
		}
	}

	styleCell := lipgloss.NewStyle().Foreground(lipgloss.Color("#D4D8E0")).Background(colorWindowBg)
	styleHeader := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFCB6B")).Background(colorWindowBg)

	var sb strings.Builder
	for ri, row := range rows {
		sb.WriteString(surfaceSpaces(2))
		for i := 0; i < cols; i++ {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			padded := cell
			if pad := colWidths[i] - len(cell); pad > 0 {
				padded += surfaceSpaces(pad)
			}
			if ri == 0 {
				sb.WriteString(styleHeader.Render(padded))
			} else {
				sb.WriteString(styleCell.Render(padded))
			}
			if i < cols-1 {
				sb.WriteString(surfaceSpaces(2))
			}
		}
		sb.WriteByte('\n')
		// Underline header row.
		if ri == 0 && len(rows) > 1 {
			sb.WriteString(surfaceSpaces(2))
			total := 0
			for _, w := range colWidths {
				total += w + 2
			}
			if total > 2 {
				total -= 2
			}
			if total > width-4 {
				total = width - 4
			}
			sb.WriteString(styleSep.Render(ornamentGradientText(strings.Repeat("─", total))))
			sb.WriteByte('\n')
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// renderCodeBlock renders a fenced code block.
// width is the usable inner width (outer padding already excluded).
// Language label appears as a dim line above the rounded box.
func renderCodeBlock(code, lang string, width int) string {
	highlighted := highlightCode(code, lang)
	// No border — styleCodeBorder is just a left-padding indent.
	// Width() here sets max content width to prevent long lines overflowing.
	block := styleCodeBorder.Width(width).Render(highlighted)

	if lang != "" {
		label := styleCodeLang.Render(lang)
		return label + "\n" + block
	}
	return block
}

// highlightCode colorizes code by language.
// All token styles include Background(colorCodeBg) so they don't reset
// the parent container's background between tokens.
func highlightCode(code, lang string) string {
	lines := strings.Split(code, "\n")
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = highlightLine(line, lang)
	}
	return strings.Join(out, "\n")
}

// styleHeading1/2/3 are styles for GFM headings.
var (
	styleHeading1  lipgloss.Style
	styleHeading2  lipgloss.Style
	styleHeading3  lipgloss.Style
	styleItalic    lipgloss.Style
	styleStrike    lipgloss.Style
	styleBQ        lipgloss.Style
	styleChecked   lipgloss.Style
	styleUnchecked lipgloss.Style
)

// renderLine applies block-level and inline styling, word-wrapping to width.
func renderLine(line string, width int) string {
	// Horizontal rule: ---, ***, ___
	trimmed := strings.TrimSpace(line)
	if trimmed == "---" || trimmed == "***" || trimmed == "___" ||
		(len(trimmed) >= 3 && strings.Count(trimmed, string(trimmed[0])) == len(trimmed) &&
			(trimmed[0] == '-' || trimmed[0] == '*' || trimmed[0] == '_')) {
		w := width
		if w < 1 {
			w = 1
		}
		return styleSep.Render(ornamentGradientText(strings.Repeat("─", w)))
	}

	// Headings.
	if strings.HasPrefix(line, "### ") {
		return styleHeading3.Render(strings.TrimPrefix(line, "### "))
	}
	if strings.HasPrefix(line, "## ") {
		return styleHeading2.Render(strings.TrimPrefix(line, "## "))
	}
	if strings.HasPrefix(line, "# ") {
		return styleHeading1.Render(strings.TrimPrefix(line, "# "))
	}

	// Blockquote.
	if strings.HasPrefix(line, "> ") {
		inner := strings.TrimPrefix(line, "> ")
		innerW := width - 4 // "  │ " = 4 cols
		if innerW < 10 {
			innerW = 10
		}
		wrapped := styleBQ.Width(innerW).Render(inner)
		bqLines := strings.Split(wrapped, "\n")
		var sb strings.Builder
		for i, l := range bqLines {
			if i > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(surfaceSpaces(2) + styleBQ.Render("│ ") + l)
		}
		return sb.String()
	}

	// Task list items.
	for _, prefix := range []string{"- [ ] ", "* [ ] ", "- [ ]", "* [ ]"} {
		if strings.HasPrefix(line, prefix) {
			rest := strings.TrimPrefix(line, prefix)
			rest = applyInline(rest)
			return styleUnchecked.Render("☐ ") + rest
		}
	}
	for _, prefix := range []string{"- [x] ", "* [x] ", "- [X] ", "* [X] ", "- [x]", "* [x]"} {
		if strings.HasPrefix(line, prefix) {
			rest := strings.TrimPrefix(line, prefix)
			rest = applyInline(rest)
			return styleChecked.Render("☑ ") + rest
		}
	}

	// Apply inline styles then word-wrap.
	line = applyInline(line)
	return styleAssistantText.Width(width).Render(line)
}

// applyInline applies all inline GFM styles: bold, italic, strikethrough, code.
func applyInline(line string) string {
	line = applyDelim(line, "**", lipgloss.NewStyle().Background(colorWindowBg).Bold(true))
	line = applyDelim(line, "__", lipgloss.NewStyle().Background(colorWindowBg).Bold(true))
	line = applyDelim(line, "~~", styleStrike)
	line = applyDelim(line, "`", styleInlineCode)
	// Italic: single * or _ (applied after ** to avoid conflict)
	line = applyDelimSingle(line, "*", styleItalic)
	line = applyDelimSingle(line, "_", styleItalic)
	return line
}

// applyDelimSingle applies a single-char delimiter for italic.
// Skips if the delimiter appears as part of ** or __.
func applyDelimSingle(line, delim string, style lipgloss.Style) string {
	double := delim + delim
	if !strings.Contains(line, delim) || strings.Contains(line, double) {
		return line
	}
	return applyDelim(line, delim, style)
}

// splitTableRow splits a GFM table row into cells (strips leading/trailing |).
func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "|")
	parts := strings.Split(line, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func applyDelim(line, delim string, style lipgloss.Style) string {
	var out strings.Builder
	for {
		i := strings.Index(line, delim)
		if i < 0 {
			out.WriteString(line)
			break
		}
		j := strings.Index(line[i+len(delim):], delim)
		if j < 0 {
			out.WriteString(line)
			break
		}
		j += i + len(delim)
		out.WriteString(line[:i])
		out.WriteString(style.Render(line[i+len(delim) : j]))
		line = line[j+len(delim):]
	}
	return out.String()
}

// separator returns a full-width dim rule.
func separator(width int) string {
	if width < 1 {
		width = 1
	}
	return styleSep.Render(ornamentGradientText(strings.Repeat("─", width)))
}
