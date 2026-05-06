package tui

import (
	"encoding/json"
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/theme"
)

const (
	prefixYou    = "› You"
	prefixClaude = "‹ Claude"
	prefixLocal  = "‹ Local"

	// outerPad is spaces on each side of all viewport content.
	outerPad = 0
)

// renderMessage renders one message for display.
// width is the full viewport width.
func renderMessage(msg Message, width int, verbose bool) string {
	if width < 20 {
		width = 80
	}
	inner := width - outerPad*2
	if inner < 10 {
		inner = 10
	}
	pad := surfaceSpaces(outerPad)

	switch msg.Role {
	case RoleUser:
		// Wrap user text at inner width minus the prefix width ("❯ You  " = 8 cols).
		prefixStr := styleYouPrefix.Render(prefixYou) + surfaceSpaces(2)
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

	case RoleLocal:
		label := prefixLocal
		if msg.ToolName != "" {
			label += " " + msg.ToolName
		}
		body := renderMarkdown(formatLocalOutput(msg.Content), inner)
		return pad + styleToolBadge.Render(label) + "\n" + indentLines(body, pad)

	case RoleAssistantInfo:
		return pad + renderAssistantInfo(msg, inner)

	case RoleTool:
		return pad + renderToolMessage(msg, inner, verbose)

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
				sb.WriteString(surfaceSpaces(prefixW))
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
				sb.WriteString(surfaceSpaces(prefixW))
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

func renderAssistantInfo(msg Message, width int) string {
	parts := []string{}
	if msg.AssistantModel != "" {
		parts = append(parts, styleStatusAccent.Render("◇ "+msg.AssistantModel))
	}
	if msg.AssistantDuration > 0 {
		parts = append(parts, styleStatus.Render(formatMessageDuration(msg.AssistantDuration)))
	}
	if msg.AssistantCost > 0 {
		parts = append(parts, styleStatus.Render(fmt.Sprintf("$%.2f", msg.AssistantCost)))
	}
	if len(parts) == 0 {
		return ""
	}
	line := strings.Join(parts, styleStatus.Render(" · "))
	return styleStatus.Width(width).Render(surfaceSpaces(2) + line)
}

func formatLocalOutput(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.Contains(trimmed, "```") {
		return text
	}
	lang := localOutputLang(trimmed)
	if lang == "" {
		return text
	}
	return "```" + lang + "\n" + trimmed + "\n```"
}

func localOutputLang(text string) string {
	first := firstNonEmptyLine(text)
	switch {
	case strings.HasPrefix(first, "diff --git ") ||
		strings.HasPrefix(first, "--- ") ||
		strings.HasPrefix(first, "+++ ") ||
		strings.HasPrefix(first, "@@ "):
		return "diff"
	case strings.HasPrefix(first, "package "):
		return "go"
	case strings.HasPrefix(first, "func ") && strings.Contains(text, "{"):
		return "go"
	case strings.HasPrefix(first, "import ") && strings.Contains(text, "func "):
		return "go"
	}
	return ""
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func renderToolMessage(msg Message, width int, verbose bool) string {
	statusIcon := styleStatusAccent.Render("✓")
	statusText := toolDoneVerb(msg.ToolName)
	archived := msg.Content == "" && msg.ToolDuration == 0 && !msg.ToolError
	if msg.Content == "running…" {
		statusIcon = styleModeYellow.Render("●")
		statusText = "running"
	} else if msg.ToolError {
		statusIcon = styleErrorText.Render("✗")
		statusText = "failed"
	} else if archived {
		statusIcon = styleStatus.Render("◦")
		statusText = "used"
	}

	headerParts := []string{
		statusIcon,
		styleToolBadge.Render(toolDisplayName(msg.ToolName)),
		styleStatus.Render(statusText),
	}
	if msg.ToolDuration > 0 {
		headerParts = append(headerParts, styleStatus.Render(formatMessageDuration(msg.ToolDuration)))
	}
	header := strings.Join(headerParts, surfaceSpaces(1))

	running := msg.Content == "running…"
	summary := toolInputSummary(msg.ToolName, msg.ToolInput)
	if summary == "" && !msg.ToolError && !running {
		summary = toolResultSummary(msg.ToolName, msg.Content)
	}
	if !msg.ToolError && summary != "" {
		available := width - lipgloss.Width(surfaceSpaces(2)+header) - lipgloss.Width(" · ")
		if available >= 8 {
			header += styleStatus.Render(" · " + truncate(summary, available))
		}
	}
	result := strings.TrimSpace(msg.Content)
	if running {
		result = ""
	}

	bodyWidth := max(10, width-4)
	var lines []string
	lines = append(lines, surfaceSpaces(2)+header)
	if msg.ToolError && result != "" {
		lines = append(lines, indentLines(styleErrorText.Width(bodyWidth).Render(result), surfaceSpaces(4)))
	}
	if verbose && !msg.ToolError && !running && !archived && result != "" {
		lines = append(lines, indentLines(styleStatus.Width(bodyWidth).Render(result), surfaceSpaces(4)))
	}
	return strings.Join(lines, "\n")
}

func toolDisplayName(name string) string {
	if name == "" {
		return "Tool"
	}
	name = strings.TrimSuffix(name, "Tool")
	if strings.HasPrefix(name, "mcp__") {
		parts := strings.Split(strings.TrimPrefix(name, "mcp__"), "__")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[len(parts)-1]
		}
	}
	if strings.Contains(name, "__") {
		parts := strings.Split(name, "__")
		return parts[len(parts)-1]
	}
	return name
}

func toolDoneVerb(toolName string) string {
	lower := strings.ToLower(toolName)
	switch {
	case strings.Contains(lower, "bash"), strings.Contains(lower, "shell"), strings.Contains(lower, "repl"):
		return "ran"
	case strings.Contains(lower, "grep"), strings.Contains(lower, "glob"), strings.Contains(lower, "search"):
		return "searched"
	case strings.Contains(lower, "read"), strings.Contains(lower, "fetch"):
		return "read"
	case strings.Contains(lower, "edit"), strings.Contains(lower, "write"), strings.Contains(lower, "notebook"):
		return "updated"
	case strings.Contains(lower, "todo"):
		return "updated"
	case strings.Contains(lower, "task"), strings.Contains(lower, "agent"):
		return "finished"
	}
	return "done"
}

func toolInputSummary(toolName, raw string) string {
	fields := parseToolInput(raw)
	if len(fields) == 0 {
		return ""
	}
	lower := strings.ToLower(toolName)
	switch {
	case strings.Contains(lower, "bash"):
		return firstToolField(fields, "command", "cmd")
	case strings.Contains(lower, "grep"):
		pattern := firstToolField(fields, "pattern", "query")
		include := firstToolField(fields, "include", "path")
		if pattern != "" && include != "" {
			return pattern + " in " + include
		}
		return pattern
	case strings.Contains(lower, "glob"):
		pattern := firstToolField(fields, "pattern")
		path := firstToolField(fields, "path")
		if pattern != "" && path != "" {
			return pattern + " under " + path
		}
		return pattern
	case strings.Contains(lower, "edit"), strings.Contains(lower, "write"), strings.Contains(lower, "read"), strings.Contains(lower, "notebook"):
		return firstToolField(fields, "file_path", "path")
	case strings.Contains(lower, "fetch"), strings.Contains(lower, "search"):
		return firstToolField(fields, "url", "query")
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		if fields[k] == "" {
			continue
		}
		parts = append(parts, k+"="+fields[k])
		if len(parts) == 2 {
			break
		}
	}
	return strings.Join(parts, " ")
}

func toolResultSummary(toolName, content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return "no output"
	}
	lower := strings.ToLower(toolName)
	if strings.Contains(lower, "bash") || strings.Contains(lower, "shell") || strings.Contains(lower, "repl") {
		lines := strings.Split(content, "\n")
		nonEmpty := 0
		first := ""
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			nonEmpty++
			if first == "" {
				first = line
			}
		}
		if first == "" {
			return "no output"
		}
		if nonEmpty == 1 {
			return first
		}
		return fmt.Sprintf("%s +%d lines", first, nonEmpty-1)
	}
	return ""
}

func parseToolInput(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		switch t := v.(type) {
		case string:
			out[k] = truncate(t, 500)
		case float64, bool:
			out[k] = fmt.Sprint(t)
		case []any:
			out[k] = fmt.Sprintf("%d item(s)", len(t))
		case map[string]any:
			out[k] = "object"
		}
	}
	return out
}

func firstToolField(fields map[string]string, keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(fields[key]); v != "" {
			return v
		}
	}
	return ""
}

func formatMessageDuration(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	if d < time.Hour {
		min := int(d / time.Minute)
		sec := int((d % time.Minute) / time.Second)
		if sec == 0 {
			return fmt.Sprintf("%dm", min)
		}
		return fmt.Sprintf("%dm%02ds", min, sec)
	}
	return d.Round(time.Minute).String()
}

// renderWelcomeCard renders the startup banner. It is intentionally more
// graphic than normal chat rows: this is the idle/welcome state, not the
// active conversation surface.
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
	if outerW < 42 {
		outerW = 42
	}
	innerW := outerW - 4 // 1 border + 1 pad on each side

	titleStyle := fgOnBg(colorFg).Bold(true)
	metaStyle := fgOnBg(colorMuted)
	accentStyle := fgOnBg(colorWindowTitle).Bold(true)
	toolStyle := fgOnBg(colorWindowBorder).Bold(true)

	bodyRows := []string{}
	addRow := func(s string) {
		bodyRows = append(bodyRows, padToWidth(s, innerW))
	}
	addBlank := func() { addRow("") }

	if logo := renderWelcomeLogo(innerW); logo != "" {
		for _, line := range strings.Split(logo, "\n") {
			addRow(line)
		}
		addBlank()
	}

	greeting := "Welcome back"
	if displayName != "" {
		greeting += ", " + displayName
	}
	addRow(titleStyle.Render(truncate(greeting, innerW)))
	addRow(metaStyle.Render(truncate(displayPath(cwd), innerW)))
	addBlank()

	sectionW := innerW
	if sectionW > 72 {
		sectionW = 72
	}

	account := subscriptionType
	if account == "" {
		account = "Claude"
	}
	if orgName != "" {
		account += " · " + orgName
	}
	if email != "" {
		account += " · " + email
	}
	addRow(renderWelcomeSection("Session", sectionW))
	addRow(metaStyle.Render(truncate("◇ "+modelName, innerW)))
	addRow(metaStyle.Render(truncate("◇ "+account, innerW)))
	addBlank()

	addRow(renderWelcomeSection("Start", sectionW))
	startRows := []string{
		"ctrl+p    commands",
		"ctrl+m    models",
		"shift+tab mode",
	}
	for i, row := range startRows {
		marker := accentStyle.Render("› ")
		if i%2 == 1 {
			marker = toolStyle.Render("› ")
		}
		addRow(marker + metaStyle.Render(truncate(row, innerW-2)))
	}
	addBlank()
	addRow(ornamentGradientText(renderSlashFill(innerW)))

	// ── Manual border with title in top line ─────────────────────────────────
	borderStyle := fgOnBg(colorWindowBorder)
	titleText := " conduit v" + version + " "
	titleRendered := brandGradientText(titleText)
	titleW := len(titleText) // plain width (no ANSI) for dash counting

	// Top: ╭─ title ───────────────╮
	afterTitle := outerW - 2 - 1 - titleW // corners(2) + leading dash(1) + title
	if afterTitle < 0 {
		afterTitle = 0
	}
	topBorder := borderStyle.Render("╭─") + titleRendered +
		borderStyle.Render(strings.Repeat("─", afterTitle)+"╮")

	// Content rows flanked by │ and one space of inner padding. Each row
	// gets wrapped in a bg-painted style at the end so bare spaces inherit
	// the shared surface instead of exposing the terminal default.
	bgWrap := lipgloss.NewStyle().Background(colorWindowBg)
	fullRows := make([]string, 0, 2+len(bodyRows)+2)
	fullRows = append(fullRows, bgWrap.Render(topBorder))
	blankInner := surfaceSpaces(innerW)
	fullRows = append(fullRows, bgWrap.Render(borderStyle.Render("│")+surfaceSpaces(1)+blankInner+surfaceSpaces(1)+borderStyle.Render("│")))
	for _, r := range bodyRows {
		lw := lipgloss.Width(r)
		if lw < innerW {
			r += surfaceSpaces(innerW - lw)
		}
		fullRows = append(fullRows, bgWrap.Render(borderStyle.Render("│")+surfaceSpaces(1)+r+surfaceSpaces(1)+borderStyle.Render("│")))
	}
	fullRows = append(fullRows, bgWrap.Render(borderStyle.Render("│")+surfaceSpaces(1)+blankInner+surfaceSpaces(1)+borderStyle.Render("│")))
	fullRows = append(fullRows, bgWrap.Render(borderStyle.Render("╰"+strings.Repeat("─", outerW-2)+"╯")))

	pad := surfaceSpaces(outerPad)
	return indentLines(strings.Join(fullRows, "\n"), pad)
}

func renderWelcomeLogo(width int) string {
	if width < 54 {
		return brandGradientText("conduit") + surfaceSpaces(1) +
			ornamentGradientText(renderSlashFill(width-8))
	}
	logoLines := []string{
		" ██████╗ ██████╗ ███╗   ██╗██████╗ ██╗   ██╗██╗████████╗",
		"██╔════╝██╔═══██╗████╗  ██║██╔══██╗██║   ██║██║╚══██╔══╝",
		"██║     ██║   ██║██╔██╗ ██║██║  ██║██║   ██║██║   ██║   ",
		"╚██████╗╚██████╔╝██║ ╚████║██████╔╝╚██████╔╝██║   ██║   ",
		" ╚═════╝ ╚═════╝ ╚═╝  ╚═══╝╚═════╝  ╚═════╝ ╚═╝   ╚═╝   ",
	}
	maxLogoW := 0
	for _, line := range logoLines {
		if w := lipgloss.Width(line); w > maxLogoW {
			maxLogoW = w
		}
	}
	if maxLogoW > width {
		return brandGradientText("conduit") + " " +
			ornamentGradientText(renderSlashFill(width-8))
	}
	slashW := width - maxLogoW - 2
	if slashW < 0 {
		slashW = 0
	}
	var out []string
	for i, line := range logoLines {
		tail := ""
		if i == 0 || i == len(logoLines)-1 {
			tail = surfaceSpaces(2) + ornamentGradientText(renderSlashFill(slashW))
		}
		out = append(out, brandGradientText(line)+tail)
	}
	return strings.Join(out, "\n")
}

func renderWelcomeSection(label string, width int) string {
	if width < lipgloss.Width(label)+4 {
		return fgOnBg(colorWindowTitle).Bold(true).Render(label)
	}
	ruleW := width - lipgloss.Width(label) - 3
	if ruleW < 0 {
		ruleW = 0
	}
	return brandGradientText(label+" ") +
		ornamentGradientText(strings.Repeat("─", ruleW))
}

func renderSlashFill(width int) string {
	if width <= 0 {
		return ""
	}
	return strings.Repeat("/", width)
}

// padToWidth right-pads a (possibly ANSI-coloured) string to the given visible width.
func padToWidth(s string, w int) string {
	sw := lipgloss.Width(s)
	if sw < w {
		return s + surfaceSpaces(w-sw)
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

// codeStyle returns a syntax style for one category, picking the palette set
// based on the active theme. Keep the background on the shared window surface
// so styled code tokens do not punch black cells through chat.
func codeStyle(get func(codePaletteSet) color.Color) lipgloss.Style {
	set := codePaletteDark
	if isLightTheme() {
		set = codePaletteLight
	}
	return lipgloss.NewStyle().Foreground(get(set)).Background(colorWindowBg)
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
	cDiffAdd    lipgloss.Style
	cDiffDel    lipgloss.Style
	cDiffHunk   lipgloss.Style
	cDiffHeader lipgloss.Style
)

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
