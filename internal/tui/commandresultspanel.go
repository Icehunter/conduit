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
	// Width(w-2) - 2 border - 4 padding = w - 8 content area.
	innerW := w - 2

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
		sb.WriteString("\n" + stylePickerDesc.Render("↑/↓ navigate · Enter view · Esc back"))
	case panelViewToolDetail:
		m.renderPanelToolDetail(&sb, p, innerW)
		sb.WriteString("\n" + stylePickerDesc.Render("Esc back"))
	}

	return panelFrameStyle(w, panelH).Render(sb.String())
}

func (m Model) renderPanelList(sb *strings.Builder, p *panelState, _ int) {
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
				src := item.source
				fmt.Fprintf(sb, "\n  %s (%s)\n",
					fgOnBg(colorFg).Bold(true).Render(item.scope+" MCPs"), src)
			}
			cursor := "  "
			nameStyle := stylePickerItem
			if i == p.selected {
				cursor = stylePickerItemSelected.Render("❯") + " "
				nameStyle = stylePickerItemSelected
			}
			fmt.Fprintf(sb, "%s%s · %s\n", cursor, nameStyle.Render(item.name), renderMCPStatus(item.status))
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
	sb.WriteString(styleStatusAccent.Render(item.name) + " MCP Server\n\n")
	// Info grid
	writeField := func(label, value string) {
		fmt.Fprintf(sb, "%-18s%s\n", label+":", value)
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
		errStyle := fgOnBg(colorError).Width(wrapW)
		sb.WriteString("\n" + errStyle.Render("Error: "+item.err) + "\n")
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

func (m Model) renderPanelTools(sb *strings.Builder, p *panelState, _ int) {
	item := p.selectedMCPItem()
	if item == nil {
		return
	}
	fmt.Fprintf(sb, "Tools for %s\n", styleStatusAccent.Render(item.name))
	fmt.Fprintf(sb, "%d tool%s\n\n", len(item.tools), pluralS(len(item.tools)))

	if len(item.tools) == 0 {
		sb.WriteString(stylePickerDesc.Render("No tools loaded (server may not be connected)."))
		return
	}
	for i, t := range item.tools {
		cursor := "  "
		nameStyle := stylePickerItem
		if i == p.selected {
			cursor = stylePickerItemSelected.Render("❯") + " "
			nameStyle = stylePickerItemSelected
		}
		// Pad the raw name first — %-30s on a styled string counts ANSI
		// escape bytes toward the width, so the visible padding becomes 0
		// and the description glues onto the tool name.
		const nameWidth = 30
		paddedName := t.name
		if pad := nameWidth - len([]rune(t.name)); pad > 0 {
			paddedName += strings.Repeat(" ", pad)
		}
		attrs := stylePickerDesc.Render("read-only, open-world")
		fmt.Fprintf(sb, "%s%d. %s%s\n", cursor, i+1, nameStyle.Render(paddedName), attrs)
	}
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
	sb.WriteString(styleStatusAccent.Render(tool.name) + " [read-only] [open-world]\n")
	sb.WriteString(stylePickerDesc.Render(item.name) + "\n\n")
	fmt.Fprintf(sb, "Tool name: %s\n", tool.name)
	if tool.fullName != "" {
		fmt.Fprintf(sb, "Full name: %s\n", tool.fullName)
	}
	if tool.description != "" {
		sb.WriteString("\nDescription:\n")
		// Word-wrap description to innerW.
		words := strings.Fields(tool.description)
		line := ""
		for _, w := range words {
			if len(line)+len(w)+1 > innerW-2 {
				sb.WriteString("  " + line + "\n")
				line = w
			} else {
				if line != "" {
					line += " "
				}
				line += w
			}
		}
		if line != "" {
			sb.WriteString("  " + line + "\n")
		}
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
