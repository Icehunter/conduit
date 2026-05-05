package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/mcp"
)

type panelTab int

const (
	panelTabMCP panelTab = 0
)

var panelTabNames = []string{"MCP"}

// panelMCPItem is one MCP server row — carries all info for detail view.
type panelMCPItem struct {
	name      string
	scope     string // "User" | "Project" | "Built-in"
	source    string // config file or "plugin:name"
	status    string // "connected" | "failed" | "pending" | "disabled"
	command   string // stdio command or URL
	args      string // space-separated args
	toolCount int
	err       string
	disabled  bool
	// tools populated on-demand when detail is opened
	tools []panelToolItem
}

// panelToolItem is one tool inside a server detail.
type panelToolItem struct {
	name        string
	fullName    string // e.g. mcp__plugin_context7_context7__resolve-library-id
	description string
}

// panelView is the navigation depth.
type panelView int

const (
	panelViewList       panelView = 0 // tab root list
	panelViewDetail     panelView = 1 // item detail (server/plugin/marketplace)
	panelViewTools      panelView = 2 // tool list inside a server
	panelViewToolDetail panelView = 3 // single tool detail
)

// panelState is the unified browser overlay.
type panelState struct {
	tab      panelTab
	view     panelView
	selected int // cursor within the current view (list row, action row, tool row)
	// serverIdx is preserved when drilling into detail/tools/tool-detail so
	// the render functions always know which server is being inspected.
	serverIdx int

	// raw encoded data
	mcpRaw string

	// parsed lists
	mcpItems []panelMCPItem
}

func newPanel(tab panelTab) *panelState {
	return &panelState{tab: tab}
}

func (p *panelState) parseMCPItems() {
	p.mcpItems = nil
	for _, line := range strings.Split(p.mcpRaw, "\n") {
		if line == "" {
			continue
		}
		// name\tscope\tsource\tstatus\tcommand\targs\ttoolCount\terr\tdisabled
		parts := strings.SplitN(line, "\t", 9)
		item := panelMCPItem{}
		get := func(i int) string {
			if i < len(parts) {
				return parts[i]
			}
			return ""
		}
		item.name = get(0)
		item.scope = get(1)
		item.source = get(2)
		item.status = get(3)
		item.command = get(4)
		item.args = get(5)
		_, _ = fmt.Sscanf(get(6), "%d", &item.toolCount)
		item.err = get(7)
		item.disabled = get(8) == "1"
		p.mcpItems = append(p.mcpItems, item)
	}
	// Sort so User then Project then Built-in — the visual order matches the
	// flat index used by p.selected.
	scopeRank := func(s string) int {
		switch s {
		case "User":
			return 0
		case "Project":
			return 1
		default:
			return 2
		}
	}
	for i := 1; i < len(p.mcpItems); i++ {
		for j := i; j > 0 && scopeRank(p.mcpItems[j].scope) < scopeRank(p.mcpItems[j-1].scope); j-- {
			p.mcpItems[j], p.mcpItems[j-1] = p.mcpItems[j-1], p.mcpItems[j]
		}
	}
}

// loadTabData is a no-op for the MCP-only panel (reserved for future tabs).
func (p *panelState) loadTabData() {}

func (p *panelState) currentLen() int {
	return len(p.mcpItems)
}

func (p *panelState) selectedMCPItem() *panelMCPItem {
	if p.serverIdx >= 0 && p.serverIdx < len(p.mcpItems) {
		return &p.mcpItems[p.serverIdx]
	}
	return nil
}

// handlePanelKey routes keyboard input through the panel navigation stack.
func (m Model) handlePanelKey(msg tea.KeyPressMsg) (Model, tea.Cmd) { //nolint:unparam
	p := m.panel
	key := msg.String()

	switch p.view {
	case panelViewList:
		switch key {
		case "left":
			p.tab = panelTab((int(p.tab) + len(panelTabNames) - 1) % len(panelTabNames))
			p.selected = 0
			p.loadTabData()
		case "right":
			p.tab = panelTab((int(p.tab) + 1) % len(panelTabNames))
			p.selected = 0
			p.loadTabData()
		case "up":
			if p.selected > 0 {
				p.selected--
			}
		case "down":
			if p.selected < p.currentLen()-1 {
				p.selected++
			}
		case "enter":
			if p.currentLen() > 0 {
				p.serverIdx = p.selected // remember which server/plugin was selected
				p.view = panelViewDetail
				p.selected = 0 // reset to first action in detail
			}
		case "esc", "ctrl+c":
			m.panel = nil
			m.refreshViewport()
			return m, nil
		}

	case panelViewDetail:
		switch key {
		case "up":
			if p.selected > 0 {
				p.selected--
			}
		case "down":
			item2 := p.selectedMCPItem()
			if item2 != nil && p.selected < len(mcpActions(item2))-1 {
				p.selected++
			}
		case "enter":
			if p.tab == panelTabMCP {
				item := p.selectedMCPItem()
				if item == nil {
					break
				}
				actions := mcpActions(item)
				if p.selected >= len(actions) {
					break
				}
				action := actions[p.selected]
				switch action {
				case "View tools":
					// Populate tools from the live manager if not already cached.
					if m.cfg.MCPManager != nil && len(item.tools) == 0 {
						for _, srv := range m.cfg.MCPManager.Servers() {
							if srv.Name == item.name {
								prefix := mcp.ToolNamePrefix(srv.Name)
								for _, t := range srv.Tools {
									item.tools = append(item.tools, panelToolItem{
										name:        t.Name,
										fullName:    prefix + t.Name,
										description: t.Description,
									})
								}
								p.mcpItems[p.serverIdx] = *item
								break
							}
						}
					}
					p.view = panelViewTools
					p.selected = 0
				case "Reconnect":
					if m.cfg.MCPManager != nil {
						cwd, _ := os.Getwd()
						srvName := item.name
						mgr := m.cfg.MCPManager
						go func() { _ = mgr.Reconnect(context.Background(), srvName, cwd) }()
						p.mcpItems[p.serverIdx].status = "pending"
						p.mcpItems[p.serverIdx].err = ""
					}
					p.view = panelViewList
					p.selected = 0
				case "Disable":
					cwd, _ := os.Getwd()
					_ = mcp.SetDisabled(item.name, cwd, true)
					p.mcpItems[p.serverIdx].disabled = true
					p.mcpItems[p.serverIdx].status = "disabled"
					// Close the live connection.
					if m.cfg.MCPManager != nil {
						go func() { m.cfg.MCPManager.DisconnectServer(item.name) }()
					}
					p.view = panelViewList
					p.selected = 0
				case "Enable":
					cwd, _ := os.Getwd()
					_ = mcp.SetDisabled(item.name, cwd, false)
					p.mcpItems[p.serverIdx].disabled = false
					p.mcpItems[p.serverIdx].status = "pending"
					// Reconnect.
					if m.cfg.MCPManager != nil {
						srvName := item.name
						mgr := m.cfg.MCPManager
						go func() { _ = mgr.Reconnect(context.Background(), srvName, cwd) }()
					}
					p.view = panelViewList
					p.selected = 0
				}
			}
		case "esc":
			p.view = panelViewList
			p.selected = 0 // cursor resets to top of list
		case "ctrl+c":
			m.panel = nil
			m.refreshViewport()
			return m, nil
		}

	case panelViewTools:
		switch key {
		case "up":
			if p.selected > 0 {
				p.selected--
			}
		case "down":
			item := p.selectedMCPItem() // uses serverIdx
			if item != nil && p.selected < len(item.tools)-1 {
				p.selected++
			}
		case "enter":
			p.view = panelViewToolDetail
		case "esc", "q":
			p.view = panelViewDetail
			p.selected = 0
		case "ctrl+c":
			m.panel = nil
			m.refreshViewport()
			return m, nil
		}

	case panelViewToolDetail:
		switch key {
		case "esc", "enter":
			p.view = panelViewTools
		case "ctrl+c":
			m.panel = nil
			m.refreshViewport()
			return m, nil
		}
	}

	m.panel = p
	return m, nil
}

// renderPanel renders the unified panel as a full-screen takeover.
// Height = terminal height minus 1 (status bar). Width = full terminal width.
func (m Model) renderPanel() string {
	p := m.panel
	if p == nil {
		return ""
	}

	w := m.width
	if w < 10 {
		w = 10
	}
	// Available height for the panel content = terminal height - 1 (status bar).
	// Border (top+bottom=2) + padding (top+bottom=2) = 4 rows consumed by chrome.
	panelH := m.height - 1
	if panelH < 4 {
		panelH = 4
	}
	// lipgloss v2's Width() is total block width (including border + padding).
	// Width(w-2) - 2 border - 4 padding = w - 8 content area.
	innerW := w - 2

	var sb strings.Builder

	// Panel title — always shown.
	sb.WriteString(styleStatusAccent.Render("MCP") + "\n")
	sb.WriteString(stylePickerDesc.Render(strings.Repeat("─", innerW-2)) + "\n\n")

	switch p.view {
	case panelViewList:
		m.renderPanelList(&sb, p, innerW)
		sb.WriteString("\n" + stylePickerDesc.Render("↑↓ navigate · Enter detail · Esc close"))
	case panelViewDetail:
		m.renderPanelDetail(&sb, p, innerW)
		sb.WriteString("\n" + stylePickerDesc.Render("↑↓ navigate · Enter select · Esc back"))
	case panelViewTools:
		m.renderPanelTools(&sb, p, innerW)
		sb.WriteString("\n" + stylePickerDesc.Render("↑↓ navigate · Enter view · Esc back"))
	case panelViewToolDetail:
		m.renderPanelToolDetail(&sb, p, innerW)
		sb.WriteString("\n" + stylePickerDesc.Render("Esc back"))
	}

	style := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Width(w).
		Height(panelH).
		PaddingLeft(2).PaddingRight(2).PaddingTop(1).PaddingBottom(1)
	return style.Width(m.width).Render(sb.String())
}

func (m Model) renderPanelList(sb *strings.Builder, p *panelState, _ int) {
	switch p.tab {
	case panelTabMCP:
		if len(p.mcpItems) == 0 {
			sb.WriteString(stylePickerDesc.Render("No MCP servers configured.\nAdd servers to ~/.claude.json under \"mcpServers\"."))
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
		if wrapW < 20 {
			wrapW = 20
		}
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
