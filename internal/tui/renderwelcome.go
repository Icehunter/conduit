package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

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
	orgName := get(5)
	subscriptionType := get(6)

	outerW := width - outerPad*2
	outerW = max(outerW, 42)
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
	sectionW = min(sectionW, 72)

	account := subscriptionType
	if account == "" {
		account = "Claude"
	}
	if orgName != "" {
		account += " · " + orgName
	}
	addRow(renderWelcomeSection("Session", sectionW))
	if modelName != "" {
		addRow(metaStyle.Render(truncate("◇ "+modelName, innerW)))
	}
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
	afterTitle = max(afterTitle, 0)
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
	slashW = max(slashW, 0)
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
	ruleW = max(ruleW, 0)
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
