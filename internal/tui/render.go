package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/theme"
)

const (
	prefixYou    = "▶ You"
	prefixClaude = "◀ Claude"

	// outerPad is spaces on each side of all viewport content.
	outerPad = 2
)

// renderMessage renders one message for display.
// width is the full viewport width.
func renderMessage(msg Message, width int) string {
	if width < 20 {
		width = 80
	}
	inner := width - outerPad*2
	if inner < 10 {
		inner = 10
	}
	pad := strings.Repeat(" ", outerPad)

	switch msg.Role {
	case RoleUser:
		// Wrap user text at inner width minus the prefix width ("▶ You  " = 8 cols).
		prefixStr := styleYouPrefix.Render(prefixYou) + "  "
		prefixW := lipgloss.Width(prefixStr)
		body := styleUserText.Width(inner - prefixW).Render(msg.Content)
		return pad + prefixStr + body

	case RoleAssistant:
		content := stripCompanionMarkerGlobal(msg.Content)
		if content == "" {
			return "" // pure companion quip — bubble handles display, skip chat row
		}
		body := renderMarkdown(content, inner)
		return pad + styleClaudePrefix.Render(prefixClaude) + "\n" + indentLines(body, pad)

	case RoleTool:
		return pad + "  " + styleToolBadge.Render("⚙ "+msg.ToolName) + "  " + styleToolContent.Render(msg.Content)

	case RoleError:
		// Wrap long error text — OAuth/API errors regularly run hundreds
		// of characters with URL chains. lipgloss.Width handles word
		// wrapping. The "✗ " marker sits on the first line; continuation
		// lines indent under the body so the marker stands out.
		const errPrefix = "✗ "
		prefixW := lipgloss.Width(errPrefix)
		body := styleErrorText.Width(inner - prefixW).Render(msg.Content)
		// hangIndent: prefix on the first line, blanks on the rest.
		lines := strings.Split(body, "\n")
		var sb strings.Builder
		for i, ln := range lines {
			sb.WriteString(pad)
			if i == 0 {
				sb.WriteString(styleErrorText.Render(errPrefix))
			} else {
				sb.WriteString(strings.Repeat(" ", prefixW))
			}
			sb.WriteString(ln)
			if i < len(lines)-1 {
				sb.WriteByte('\n')
			}
		}
		return sb.String()

	case RoleSystem:
		if msg.WelcomeCard {
			return renderWelcomeCard(msg.Content, width)
		}
		// If the content contains markdown (fenced block, heading), render it
		// as markdown so code blocks, diff highlighting, etc. work.
		trimmed := strings.TrimSpace(msg.Content)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "#") {
			body := renderMarkdown(msg.Content, inner)
			return pad + styleSystemText.Render("· ") + "\n" + indentLines(body, pad)
		}
		// Wrap to terminal width — /session, /status, /usage etc. emit long
		// lines (file paths, session IDs, rate-limit URLs) that otherwise
		// blow past the right edge and force horizontal scroll. The "· "
		// prefix sits on the first line; continuation rows indent under
		// the body so the prefix marks the message boundary.
		const sysPrefix = "· "
		prefixW := lipgloss.Width(sysPrefix)
		body := styleSystemText.Width(inner - prefixW).Render(msg.Content)
		lines := strings.Split(body, "\n")
		var sb strings.Builder
		for i, ln := range lines {
			sb.WriteString(pad)
			if i == 0 {
				sb.WriteString(styleSystemText.Render(sysPrefix))
			} else {
				sb.WriteString(strings.Repeat(" ", prefixW))
			}
			sb.WriteString(ln)
			if i < len(lines)-1 {
				sb.WriteByte('\n')
			}
		}
		return sb.String()
	}
	return msg.Content
}

// renderWelcomeCard renders the two-panel startup banner.
// content is tab-separated: version, modelName, cwd, displayName, email, orgName, subscriptionType.
func renderWelcomeCard(content string, width int) string {
	parts := strings.Split(content, "\t")
	get := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	version := get(0)
	modelName := get(1)
	cwd := get(2)
	displayName := get(3)
	email := get(4)
	orgName := get(5)
	subscriptionType := get(6)

	outerW := width - outerPad*2
	if outerW < 50 {
		outerW = 50
	}
	// innerW = content inside the borders, excluding the 1-space inner pad each side.
	innerW := outerW - 4 // 1 border + 1 pad on each side

	// Left column: ~38% of inner width, floored at 45 chars and capped at innerW/2.
	divW := 3 // " │ "
	leftW := innerW * 38 / 100
	if leftW < 45 {
		leftW = 45
	}
	if leftW > innerW/2 {
		leftW = innerW / 2
	}
	rightW := innerW - leftW - divW - 1 // -1 for the leading space before leftW
	if rightW < 10 {
		rightW = 10
	}

	titleStyle := fgOnBg(colorFg).Bold(true)
	metaStyle := fgOnBg(colorMuted)
	accentStyle := fgOnBg(colorAccent).Bold(true)
	dimStyle := fgOnBg(colorDim)

	// Build greeting — use display name if available.
	greeting := "Welcome back!"
	if displayName != "" {
		greeting = "Welcome back, " + displayName + "!"
	}

	// Left column: greeting, blank, subscription line, email, org, blank, model, cwd.
	// Only show lines with content; always show model+cwd.
	var leftLines []string
	leftLines = append(leftLines, padToWidth(titleStyle.Render(truncate(greeting, leftW)), leftW))
	leftLines = append(leftLines, padToWidth("", leftW))
	if subscriptionType != "" {
		sub := subscriptionType
		if orgName != "" {
			sub += " · " + orgName
		}
		leftLines = append(leftLines, padToWidth(metaStyle.Render(truncate(sub, leftW)), leftW))
	}
	if email != "" {
		leftLines = append(leftLines, padToWidth(metaStyle.Render(truncate(email, leftW)), leftW))
	}
	leftLines = append(leftLines, padToWidth("", leftW))
	leftLines = append(leftLines, padToWidth(metaStyle.Render(truncate(modelName, leftW)), leftW))
	leftLines = append(leftLines, padToWidth(metaStyle.Render(truncate(cwd, leftW)), leftW))

	divider := dimStyle.Render(" │ ")

	// Row content width = innerW. Wrapper is "│ " + row + " │" = outerW.
	rowW := innerW
	rightW = rowW - leftW - divW
	if rightW < 10 {
		rightW = 10
	}

	tr := func(s string) string { return truncate(s, rightW) }
	rightLines := []string{
		accentStyle.Render("Tips for getting started"),
		metaStyle.Render(tr("Run /init to create a CLAUDE.md for this project")),
		metaStyle.Render(tr("Use /help to see all available commands")),
		metaStyle.Render(tr("Press ↑/↓ to navigate input history")),
		metaStyle.Render(tr("Ctrl+Y copies the last code block")),
		metaStyle.Render(""),
		accentStyle.Render("What's new"),
		metaStyle.Render(tr("/release-notes for full release notes")),
	}

	// Normalise row count.
	rows := len(rightLines)
	if len(leftLines) > rows {
		rows = len(leftLines)
	}
	for len(leftLines) < rows {
		leftLines = append(leftLines, strings.Repeat(" ", leftW))
	}
	for len(rightLines) < rows {
		rightLines = append(rightLines, "")
	}

	var bodyRows []string
	for i := 0; i < rows; i++ {
		left := padToWidth(leftLines[i], leftW)
		right := padToWidth(rightLines[i], rightW)
		row := left + divider + right
		// Pad to rowW.
		rw := lipgloss.Width(row)
		if rw < rowW {
			row += strings.Repeat(" ", rowW-rw)
		}
		bodyRows = append(bodyRows, row)
	}

	// ── Manual border with title in top line ─────────────────────────────────
	borderStyle := fgOnBg(colorAccent)
	titleText := " conduit v" + version + " "
	titleRendered := fgOnBg(colorAccent).Bold(true).Render(titleText)
	titleW := len(titleText) // plain width (no ANSI) for dash counting

	// Top: ╭─ title ───────────────╮
	afterTitle := outerW - 2 - 1 - titleW // corners(2) + leading dash(1) + title
	if afterTitle < 0 {
		afterTitle = 0
	}
	topBorder := borderStyle.Render("╭─") + titleRendered +
		borderStyle.Render(strings.Repeat("─", afterTitle)+"╮")

	// Content rows flanked by │ and one space of inner padding. Each row
	// gets wrapped in a bg-painted style at the end so the inner spaces
	// (which are bare " " concatenations, not lipgloss-styled) inherit
	// the theme bg instead of exposing terminal default.
	bgWrap := lipgloss.NewStyle()
	fullRows := make([]string, 0, 2+len(bodyRows)+2)
	fullRows = append(fullRows, bgWrap.Render(topBorder))
	blankInner := strings.Repeat(" ", innerW)
	fullRows = append(fullRows, bgWrap.Render(borderStyle.Render("│")+" "+blankInner+" "+borderStyle.Render("│")))
	for _, r := range bodyRows {
		lw := lipgloss.Width(r)
		if lw < rowW {
			r += strings.Repeat(" ", rowW-lw)
		}
		fullRows = append(fullRows, bgWrap.Render(borderStyle.Render("│")+" "+r+" "+borderStyle.Render("│")))
	}
	fullRows = append(fullRows, bgWrap.Render(borderStyle.Render("│")+" "+blankInner+" "+borderStyle.Render("│")))
	fullRows = append(fullRows, bgWrap.Render(borderStyle.Render("╰"+strings.Repeat("─", outerW-2)+"╯")))

	pad := strings.Repeat(" ", outerPad)
	return indentLines(strings.Join(fullRows, "\n"), pad)
}

// padToWidth right-pads a (possibly ANSI-coloured) string to the given visible width.
func padToWidth(s string, w int) string {
	sw := lipgloss.Width(s)
	if sw < w {
		return s + strings.Repeat(" ", w-sw)
	}
	return s
}

// truncate shortens a plain string to at most w runes, appending "…" if cut.
func truncate(s string, w int) string {
	runes := []rune(s)
	if len(runes) <= w {
		return s
	}
	if w <= 1 {
		return "…"
	}
	return string(runes[:w-1]) + "…"
}

func indentLines(s, pad string) string {
	if pad == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n")
}

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

	styleCell := lipgloss.NewStyle().Foreground(lipgloss.Color("#D4D8E0"))
	styleHeader := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFCB6B"))

	var sb strings.Builder
	for ri, row := range rows {
		sb.WriteString("  ") // indent
		for i := 0; i < cols; i++ {
			cell := ""
			if i < len(row) {
				cell = row[i]
			}
			padded := cell + strings.Repeat(" ", colWidths[i]-len(cell))
			if ri == 0 {
				sb.WriteString(styleHeader.Render(padded))
			} else {
				sb.WriteString(styleCell.Render(padded))
			}
			if i < cols-1 {
				sb.WriteString("  ")
			}
		}
		sb.WriteByte('\n')
		// Underline header row.
		if ri == 0 && len(rows) > 1 {
			sb.WriteString("  ")
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
			sb.WriteString(styleSep.Render(strings.Repeat("─", total)))
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

// codePalette returns syntax highlight colors appropriate for the active
// theme. Light themes get darker token colors so they remain readable on
// light bg; dark themes use the original Material Theme palette.
type codePaletteSet struct {
	keyword, str, comment, number, operator, typ, plain color.Color
}

var (
	codePaletteDark = codePaletteSet{
		keyword:  lipgloss.Color("#C792EA"), // purple
		str:      lipgloss.Color("#C3E88D"), // pale green
		comment:  lipgloss.Color("#546E7A"), // slate
		number:   lipgloss.Color("#F78C6C"), // orange
		operator: lipgloss.Color("#89DDFF"), // cyan
		typ:      lipgloss.Color("#FFCB6B"), // amber
		plain:    lipgloss.Color("#D4D8E0"), // off-white
	}
	codePaletteLight = codePaletteSet{
		keyword:  lipgloss.Color("#7B2D88"), // dark purple
		str:      lipgloss.Color("#1A7F37"), // dark green
		comment:  lipgloss.Color("#6E7681"), // mid gray
		number:   lipgloss.Color("#B65A1A"), // dark orange
		operator: lipgloss.Color("#0550AE"), // dark blue
		typ:      lipgloss.Color("#9A6700"), // dark amber
		plain:    lipgloss.Color("#1F2328"), // near-black
	}
)

// codeStyle returns a foreground-only style for one syntax category,
// picking the palette set based on the active theme.
func codeStyle(get func(codePaletteSet) color.Color) lipgloss.Style {
	set := codePaletteDark
	if isLightTheme() {
		set = codePaletteLight
	}
	return lipgloss.NewStyle().Foreground(get(set))
}

// isLightTheme returns true when the active palette uses dark foregrounds
// (i.e. is meant for light terminal backgrounds).
func isLightTheme() bool {
	name := theme.Active().Name
	return strings.HasPrefix(name, "light")
}

// Token style accessors — call at render time so theme switches apply.
func cKeywordStyle() lipgloss.Style {
	return codeStyle(func(s codePaletteSet) color.Color { return s.keyword })
}
func cStringStyle() lipgloss.Style {
	return codeStyle(func(s codePaletteSet) color.Color { return s.str })
}
func cCommentStyle() lipgloss.Style {
	return codeStyle(func(s codePaletteSet) color.Color { return s.comment }).Italic(true)
}
func cNumberStyle() lipgloss.Style {
	return codeStyle(func(s codePaletteSet) color.Color { return s.number })
}
func cOperatorStyle() lipgloss.Style {
	return codeStyle(func(s codePaletteSet) color.Color { return s.operator })
}
func cTypeStyle() lipgloss.Style {
	return codeStyle(func(s codePaletteSet) color.Color { return s.typ })
}
func cPlainStyle() lipgloss.Style {
	return codeStyle(func(s codePaletteSet) color.Color { return s.plain })
}

var langKeywords = map[string][]string{
	"go": {
		"package", "import", "func", "var", "const", "type", "struct",
		"interface", "map", "chan", "go", "defer", "return", "if", "else",
		"for", "range", "switch", "case", "default", "break", "continue",
		"select", "fallthrough", "goto", "nil", "true", "false",
	},
	"python": {
		"def", "class", "import", "from", "return", "if", "elif", "else",
		"for", "while", "in", "not", "and", "or", "is", "lambda", "with",
		"as", "pass", "break", "continue", "try", "except", "finally",
		"raise", "yield", "global", "nonlocal", "True", "False", "None",
		"print", "range", "len", "type", "str", "int", "float",
	},
	"javascript": {
		"const", "let", "var", "function", "return", "if", "else", "for",
		"while", "switch", "case", "break", "class", "import", "export",
		"default", "from", "new", "this", "async", "await", "try", "catch",
		"throw", "null", "undefined", "true", "false", "typeof", "instanceof",
	},
	"typescript": {
		"const", "let", "var", "function", "return", "if", "else", "for",
		"while", "class", "import", "export", "interface", "type", "enum",
		"extends", "implements", "new", "async", "await", "null",
		"undefined", "true", "false", "string", "number", "boolean", "any",
		"void", "never", "readonly", "public", "private", "protected",
	},
	"rust": {
		"fn", "let", "mut", "const", "struct", "enum", "impl", "trait",
		"pub", "use", "mod", "return", "if", "else", "for", "while", "loop",
		"match", "in", "async", "await", "dyn", "where", "type", "unsafe",
		"true", "false", "None", "Some", "Ok", "Err", "self", "Self",
	},
	"kotlin": {
		"fun", "val", "var", "class", "object", "interface", "data", "sealed",
		"abstract", "open", "override", "return", "if", "else", "for", "while",
		"when", "is", "as", "in", "import", "package", "null", "true", "false",
		"this", "super", "companion", "by", "init", "constructor", "lateinit",
		"suspend", "coroutine", "launch", "async", "await", "try", "catch",
		"throw", "finally", "enum", "annotation",
	},
	"java": {
		"class", "interface", "extends", "implements", "public", "private",
		"protected", "static", "final", "void", "return", "if", "else", "for",
		"while", "new", "import", "package", "null", "true", "false", "this",
		"super", "try", "catch", "throw", "throws", "finally", "abstract",
		"synchronized", "instanceof",
	},
	"bash": {"if", "then", "else", "elif", "fi", "for", "do", "done", "while",
		"case", "esac", "function", "return", "export", "local", "echo", "exit",
	},
	"sh":   {"if", "then", "else", "elif", "fi", "for", "do", "done", "while", "case", "esac", "echo"},
	"yaml": {},
	"json": {},
	"toml": {},
	"sql":  {"SELECT", "FROM", "WHERE", "INSERT", "UPDATE", "DELETE", "CREATE", "TABLE", "JOIN", "ON", "AND", "OR", "NOT", "IN", "AS", "BY", "GROUP", "ORDER", "LIMIT", "OFFSET"},
}

var langComments = map[string][]string{
	"go": {"//", "/*"}, "python": {"#"}, "javascript": {"//", "/*"},
	"typescript": {"//", "/*"}, "rust": {"//", "/*"}, "kotlin": {"//", "/*"},
	"java": {"//", "/*"}, "bash": {"#"}, "sh": {"#"},
}

var (
	cDiffAdd    = lipgloss.NewStyle().Foreground(lipgloss.Color("#89DDFF")).Background(colorCodeBg).Bold(false) // actually green
	cDiffDel    = lipgloss.NewStyle().Foreground(lipgloss.Color("#F07178")).Background(colorCodeBg)
	cDiffHunk   = lipgloss.NewStyle().Foreground(lipgloss.Color("#89DDFF")).Background(colorCodeBg)
	cDiffHeader = lipgloss.NewStyle().Foreground(lipgloss.Color("#7986CB")).Background(colorCodeBg)
)

func init() {
	// Override with proper green — lipgloss colors set in var block above need
	// to reference each other, so we fix the add color here.
	cDiffAdd = lipgloss.NewStyle().Foreground(lipgloss.Color("#C3E88D")).Background(colorCodeBg)
}

func highlightLine(line, lang string) string {
	// Diff language: color by line prefix.
	if lang == "diff" || lang == "patch" {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			return cDiffHeader.Render(line)
		case strings.HasPrefix(line, "+"):
			return cDiffAdd.Render(line)
		case strings.HasPrefix(line, "-"):
			return cDiffDel.Render(line)
		case strings.HasPrefix(line, "@@"):
			return cDiffHunk.Render(line)
		default:
			return cPlainStyle().Render(line)
		}
	}

	// Whole-line comment check.
	if prefixes, ok := langComments[lang]; ok {
		trimmed := strings.TrimLeft(line, " \t")
		for _, p := range prefixes {
			if strings.HasPrefix(trimmed, p) {
				return cCommentStyle().Render(line)
			}
		}
	}

	// YAML/JSON/TOML and unknown — render plain but still colored.
	if _, ok := langKeywords[lang]; !ok || (lang != "" && len(langKeywords[lang]) == 0) {
		return cPlainStyle().Render(line)
	}

	return tokenizeLine(line, lang)
}

func tokenizeLine(line, lang string) string {
	kwSet := make(map[string]bool)
	for _, k := range langKeywords[lang] {
		kwSet[k] = true
	}

	var out strings.Builder
	runes := []rune(line)
	n := len(runes)
	i := 0

	for i < n {
		ch := runes[i]

		// String: " or '
		if ch == '"' || ch == '\'' {
			quote := ch
			j := i + 1
			for j < n && runes[j] != quote {
				if runes[j] == '\\' {
					j++
				}
				j++
			}
			if j < n {
				j++
			}
			out.WriteString(cStringStyle().Render(string(runes[i:j])))
			i = j
			continue
		}

		// Backtick string
		if ch == '`' {
			j := i + 1
			for j < n && runes[j] != '`' {
				j++
			}
			if j < n {
				j++
			}
			out.WriteString(cStringStyle().Render(string(runes[i:j])))
			i = j
			continue
		}

		// Number
		if ch >= '0' && ch <= '9' {
			j := i
			for j < n && (runes[j] >= '0' && runes[j] <= '9' || runes[j] == '.' ||
				runes[j] == 'x' || runes[j] == 'X' || runes[j] == '_') {
				j++
			}
			out.WriteString(cNumberStyle().Render(string(runes[i:j])))
			i = j
			continue
		}

		// Word (identifier or keyword)
		if isIdent(ch) {
			j := i
			for j < n && isIdent(runes[j]) {
				j++
			}
			word := string(runes[i:j])
			switch {
			case kwSet[word]:
				out.WriteString(cKeywordStyle().Render(word))
			case isTypeName(word):
				out.WriteString(cTypeStyle().Render(word))
			default:
				out.WriteString(cPlainStyle().Render(word))
			}
			i = j
			continue
		}

		// Operator
		if isOperator(ch) {
			out.WriteString(cOperatorStyle().Render(string(ch)))
		} else {
			out.WriteString(cPlainStyle().Render(string(ch)))
		}
		i++
	}
	return out.String()
}

func isIdent(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

// isTypeName: PascalCase words are likely type names.
func isTypeName(word string) bool {
	if len(word) < 2 {
		return false
	}
	r := rune(word[0])
	r2 := rune(word[1])
	return r >= 'A' && r <= 'Z' && ((r2 >= 'a' && r2 <= 'z') || (r2 >= 'A' && r2 <= 'Z'))
}

func isOperator(r rune) bool {
	return strings.ContainsRune("+-*/=<>!&|^~%", r)
}

// styleHeading1/2/3 are styles for GFM headings.
var (
	styleHeading1  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFCB6B")).Underline(true)
	styleHeading2  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#C792EA"))
	styleHeading3  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#89DDFF"))
	styleItalic    = lipgloss.NewStyle().Italic(true)
	styleStrike    = lipgloss.NewStyle().Strikethrough(true).Foreground(lipgloss.Color("#546E7A"))
	styleBQ        = lipgloss.NewStyle().Foreground(lipgloss.Color("#546E7A")).Italic(true)
	styleChecked   = lipgloss.NewStyle().Foreground(lipgloss.Color("#89DDFF"))
	styleUnchecked = lipgloss.NewStyle().Foreground(lipgloss.Color("#546E7A"))
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
		return styleSep.Render(strings.Repeat("─", w))
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
		return "  " + styleBQ.Render("│ "+inner)
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
	line = applyDelim(line, "**", lipgloss.NewStyle().Bold(true))
	line = applyDelim(line, "__", lipgloss.NewStyle().Bold(true))
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
	return styleSep.Render(strings.Repeat("─", width))
}
