package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// renderPanel renders the unified panel as a full-screen takeover.
// Height = terminal height minus 1 (status bar). Width = full terminal width.
func (m Model) renderPanel() string {
	p := m.panel
	if p == nil {
		return ""
	}

	w := m.width
	w = max(w, 10)
	// Available height for the panel content = terminal height minus the
	// shared footer stack.
	panelH := m.panelHeight()
	// lipgloss v2's Width() is total block width (including border + padding).
	innerW := panelContentWidth(w)

	var sb strings.Builder

	// Panel title — always shown.
	title := panelTitle("MCP")
	ornW := innerW - lipgloss.Width(title) - 6
	sb.WriteString(title + surfaceSpaces(2) + ornamentGradientText(renderSlashFill(ornW)) + surfaceSpaces(2) + "\n\n")

	switch p.view {
	case panelViewList:
		m.renderPanelList(&sb, p, innerW)
		sb.WriteString("\n" + stylePickerDesc.Render("↑/↓ navigate · Enter detail · Esc close"))
	case panelViewDetail:
		m.renderPanelDetail(&sb, p, innerW)
		sb.WriteString("\n" + stylePickerDesc.Render("↑/↓ navigate · Enter select · Esc back"))
	case panelViewTools:
		m.renderPanelTools(&sb, p, innerW)
	case panelViewToolDetail:
		m.renderPanelToolDetail(&sb, p, innerW)
		sb.WriteString("\n" + stylePickerDesc.Render("Esc back"))
	}

	return panelFrameStyle(w, panelH).Render(sb.String())
}

func (m Model) renderPanelList(sb *strings.Builder, p *panelState, innerW int) {
	switch p.tab {
	case panelTabMCP:
		if len(p.mcpItems) == 0 {
			sb.WriteString(stylePickerDesc.Render("No MCP servers configured.\nAdd servers to ~/.conduit/mcp.json under \"mcpServers\"."))
			return
		}
		sb.WriteString(styleStatusAccent.Render("Manage MCP servers") + "\n")
		fmt.Fprintf(sb, "%d server%s", len(p.mcpItems), pluralS(len(p.mcpItems)))

		// Items are pre-sorted by scope (User → Project → Built-in).
		// Insert a section header whenever scope changes.
		lastScope := ""
		for i, item := range p.mcpItems {
			if item.scope != lastScope {
				lastScope = item.scope
				src := truncatePlainToWidth(item.source, max(innerW-lipgloss.Width(item.scope)-14, 12))
				fmt.Fprintf(sb, "\n  %s (%s)\n",
					fgOnBg(colorFg).Bold(true).Render(item.scope+" MCPs"), src)
			}
			cursor := "  "
			nameStyle := stylePickerItem
			if i == p.selected {
				cursor = stylePickerItemSelected.Render("❯") + " "
				nameStyle = stylePickerItemSelected
			}
			status := renderMCPStatus(item.status)
			nameW := innerW - lipgloss.Width(cursor) - lipgloss.Width(" · ") - lipgloss.Width(status)
			name := truncatePlainToWidth(item.name, max(nameW, 8))
			fmt.Fprintf(sb, "%s%s · %s\n", cursor, nameStyle.Render(name), status)
		}
		sb.WriteString("\n" + stylePickerDesc.Render("https://code.claude.com/docs/en/mcp for help"))

	}
}

func (m Model) renderPanelDetail(sb *strings.Builder, p *panelState, innerW int) {
	item := p.selectedMCPItem()
	if item == nil {
		return
	}
	// Title
	title := truncatePlainToWidth(item.name, max(innerW-lipgloss.Width(" MCP Server"), 10))
	sb.WriteString(styleStatusAccent.Render(title) + " MCP Server\n\n")
	// Info grid
	writeField := func(label, value string) {
		labelText := label + ":"
		valueW := max(innerW-20, 12)
		wrapped := wordWrap(value, valueW)
		lines := strings.Split(wrapped, "\n")
		for i, line := range lines {
			if i == 0 {
				fmt.Fprintf(sb, "%-18s%s\n", labelText, line)
			} else {
				fmt.Fprintf(sb, "%-18s%s\n", "", line)
			}
		}
	}
	writeField("Status", renderMCPStatus(item.status))
	if item.command != "" {
		writeField("Command", item.command)
	}
	if item.args != "" {
		writeField("Args", item.args)
	}
	if item.source != "" {
		writeField("Config location", item.source)
	}
	writeField("Tools", fmt.Sprintf("%d tool%s", item.toolCount, pluralS(item.toolCount)))
	if item.err != "" {
		// Wrap long error messages to the inner panel width — without
		// this, OAuth errors (which can be hundreds of chars long with
		// a URL chain) get clipped at the right edge.
		wrapW := innerW - 2
		wrapW = max(wrapW, 20)
		errStyle := fgOnBg(colorError)
		sb.WriteString("\n" + errStyle.Render(wordWrap("Error: "+item.err, wrapW)) + "\n")
	}
	sb.WriteByte('\n')
	// Context-sensitive actions matching Claude Code's MCPStdioServerMenu:
	//   1. View tools   — only if connected and has tools
	//   2. Reconnect    — only if not disabled
	//   3. Disable/Enable — always shown, label toggles
	actions := mcpActions(item)
	for i, action := range actions {
		cursor := "  "
		style := stylePickerItem
		if i == p.selected {
			cursor = stylePickerItemSelected.Render("❯") + " "
			style = stylePickerItemSelected
		}
		fmt.Fprintf(sb, "%s%d. %s\n", cursor, i+1, style.Render(action))
	}
}

// mcpActions returns the context-sensitive action list for a server detail view.
// Matches MCPStdioServerMenu.tsx in the real Claude Code.
func mcpActions(item *panelMCPItem) []string {
	var actions []string
	if !item.disabled && item.status == "connected" && item.toolCount > 0 {
		actions = append(actions, "View tools")
	}
	if item.status == "needs-auth" {
		actions = append(actions, "Authenticate")
	}
	if !item.disabled {
		actions = append(actions, "Reconnect")
	}
	if item.disabled {
		actions = append(actions, "Enable")
	} else {
		actions = append(actions, "Disable")
	}
	return actions
}

func (m Model) renderPanelTools(sb *strings.Builder, p *panelState, innerW int) {
	item := p.selectedMCPItem()
	if item == nil {
		return
	}
	fmt.Fprintf(sb, "Tools for %s\n", styleStatusAccent.Render(truncatePlainToWidth(item.name, max(innerW-lipgloss.Width("Tools for "), 10))))
	fmt.Fprintf(sb, "%d tool%s\n\n", len(item.tools), pluralS(len(item.tools)))

	if len(item.tools) == 0 {
		sb.WriteString(stylePickerDesc.Render("No tools loaded (server may not be connected)."))
		return
	}

	// Header is 4 rows, footer is 2 rows, and panelFrameStyle contributes
	// top/bottom border plus top/bottom padding. Keep the list inside the
	// visible panel rect instead of letting long tool lists paint under the
	// input/footer chrome.
	visible := m.panelHeight() - 11
	visible = max(visible, 3)
	total := len(item.tools)
	start := p.selected - visible/2
	start = max(start, 0)
	end := start + visible
	if end > total {
		end = total
		start = end - visible
		start = max(start, 0)
	}

	for i := start; i < end; i++ {
		t := item.tools[i]

		cursor := "  "
		nameStyle := stylePickerItem
		if i == p.selected {
			cursor = stylePickerItemSelected.Render("❯") + " "
			nameStyle = stylePickerItemSelected
		}

		nameWidth := min(28, max(innerW-22, 10))

		name := truncatePlainToWidth(t.name, nameWidth)

		paddedName := lipgloss.PlaceHorizontal(
			nameWidth,
			lipgloss.Left,
			name,
		)

		attrs := stylePickerDesc.Render("read-only, open-world")

		fmt.Fprintf(
			sb,
			"%s%2d. %s %s\n",
			cursor,
			i+1,
			nameStyle.Render(paddedName),
			attrs,
		)
	}

	footer := "↑/↓ navigate · Enter view · Esc back"
	if total > visible {
		footer = fmt.Sprintf("↑/↓ scroll (%d–%d/%d) · Enter view · Esc back", start+1, end, total)
	}
	sb.WriteString("\n" + stylePickerDesc.Render(footer))
}

func (m Model) renderPanelToolDetail(sb *strings.Builder, p *panelState, innerW int) {
	item := p.selectedMCPItem()
	if item == nil {
		return
	}
	if p.selected >= len(item.tools) {
		return
	}
	tool := item.tools[p.selected]

	// Title bar
	title := truncatePlainToWidth(tool.name, max(innerW-25, 10))
	sb.WriteString(styleStatusAccent.Render(title) + " [read-only] [open-world]\n")
	sb.WriteString(stylePickerDesc.Render(truncatePlainToWidth(item.name, max(innerW, 10))) + "\n\n")
	fmt.Fprintf(sb, "Tool name: %s\n", wordWrap(tool.name, max(innerW-11, 10)))
	if tool.fullName != "" {
		fmt.Fprintf(sb, "Full name: %s\n", wordWrap(tool.fullName, max(innerW-11, 10)))
	}
	if tool.description != "" {
		sb.WriteString("\nDescription:\n")
		sb.WriteString(indentLines(wordWrap(tool.description, max(innerW-2, 10)), "  ") + "\n")
	}
}

func renderMCPStatus(status string) string {
	switch status {
	case "connected":
		return fgOnBg(lipgloss.Color("2")).Render("✔ connected")
	case "failed":
		return fgOnBg(lipgloss.Color("1")).Render("✗ failed")
	default:
		return stylePickerDesc.Render("… " + status)
	}
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
