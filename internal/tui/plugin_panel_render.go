package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// ---- Render ----------------------------------------------------------------

func (m Model) renderPluginPanel() string {
	p := m.pluginPanel
	if p == nil {
		return ""
	}

	w := m.width
	if w < 10 {
		w = 10
	}
	panelH := pluginPanelHeight(m.panelHeight())
	// lipgloss v2's Width() is total block width (including border + padding).
	// Outer style: Width(w), border 1 each side (2), padding 2 each side (4)
	// → content area = w - 6.
	innerW := w - 4

	var sb strings.Builder

	title := panelTitle("Plugins")
	tabs := settingsColorTabs(pluginTabNames, int(p.tab))
	ornW := innerW - lipgloss.Width(title) - lipgloss.Width(tabs) - 6
	if ornW < 6 {
		ornW = 6
	}
	sb.WriteString(title + surfaceSpaces(2) + ornamentGradientText(renderSlashFill(ornW)) + surfaceSpaces(2) + tabs)
	sb.WriteString("\n\n")

	contentH := panelH - 3
	if contentH < 4 {
		contentH = 4
	}

	var body strings.Builder
	switch p.view {
	case pluginViewList:
		m.renderPluginList(&body, p, innerW, contentH)
	case pluginViewDetail:
		m.renderPluginDetail(&body, p, innerW)
	case pluginViewMCPOpts:
		m.renderPluginMCPOpts(&body, p)
	case pluginViewAddMkt:
		m.renderPluginAddMkt(&body, p, innerW)
	}
	bodyText := padPluginPanelBody(body.String(), contentH)
	sb.WriteString(bodyText)

	return panelFrameStyle(w, panelH).Render(sb.String())
}

func pluginPanelHeight(available int) int {
	if available < 1 {
		return 1
	}
	const preferred = 24
	if available < preferred {
		return available
	}
	return preferred
}

func (m Model) renderPluginList(sb *strings.Builder, p *pluginPanelState, innerW, contentH int) {
	switch p.tab {
	case pluginTabDiscover:
		m.renderDiscoverTab(sb, p, innerW, contentH)
	case pluginTabInstalled:
		m.renderInstalledTab(sb, p, contentH)
	case pluginTabMarketplaces:
		m.renderMarketplacesTab(sb, p, contentH)
	case pluginTabErrors:
		m.renderErrorsTab(sb, p, contentH)
	}
}

func padPluginPanelBody(body string, targetHeight int) string {
	for lipgloss.Height(body) < targetHeight {
		body += "\n"
	}
	return body
}

func (m Model) renderDiscoverTab(sb *strings.Builder, p *pluginPanelState, innerW, contentH int) {
	// Lines consumed before and after the item list:
	//   search prompt: 1 row
	//   blank after search (\n\n ends search row + adds blank): 1 row
	//   blank before footer (\n prefix on footer): 1 row
	//   footer text: 1 row
	//   total: 4 rows (+ 1 if loading notice shown)
	overhead := 4
	if p.loadingCounts {
		overhead++
		sb.WriteString(stylePickerDesc.Render("Loading install counts…") + "\n")
	}

	var searchPrompt string
	if p.discoverSearch == "" {
		searchPrompt = stylePickerDesc.Render("Search: (type to filter)")
	} else {
		searchPrompt = "Search: " + styleStatusAccent.Render(p.discoverSearch)
	}
	sb.WriteString(searchPrompt + "\n\n")

	if len(p.discoverFiltered) == 0 {
		if p.discoverSearch != "" {
			sb.WriteString(stylePickerDesc.Render("No plugins match \"" + p.discoverSearch + "\"."))
		} else {
			sb.WriteString(stylePickerDesc.Render("No plugins found."))
		}
		sb.WriteString("\n\n" + stylePickerDesc.Render("Space toggle · i install · Enter detail · ←→ tabs · Esc close"))
		return
	}

	// Each item is 2 lines. Compute how many fit.
	availableLines := contentH - overhead
	if availableLines < 2 {
		availableLines = 2
	}
	maxItems := availableLines / 2

	// Scroll window: keep selected visible.
	total := len(p.discoverFiltered)
	start := p.selected - maxItems/2
	if start < 0 {
		start = 0
	}
	end := start + maxItems
	if end > total {
		end = total
		start = end - maxItems
		if start < 0 {
			start = 0
		}
	}

	// Scroll indicator.
	scrollInfo := ""
	if total > maxItems {
		scrollInfo = fmt.Sprintf(" (%d–%d of %d)", start+1, end, total)
		scrollInfo = stylePickerDesc.Render(scrollInfo)
	}
	_ = scrollInfo // used in footer below

	for i := start; i < end; i++ {
		idx := p.discoverFiltered[i]
		item := p.discoverItems[idx]
		cursor := "  "
		nameStyle := stylePickerItem
		if i == p.selected {
			cursor = stylePickerItemSelected.Render("❯") + " "
			nameStyle = stylePickerItemSelected
		}
		toggle := "○"
		if item.selected {
			toggle = styleStatusAccent.Render("●")
		}
		installed := ""
		if item.installed {
			installed = " " + fgOnBg(lipgloss.Color("2")).Render("[installed]")
		}
		installs := ""
		if item.installs > 0 {
			installs = fmt.Sprintf(" (%s)", formatInstalls(item.installs))
		}
		// Truncate description to fit.
		desc := item.description
		maxDesc := innerW - 10
		if maxDesc > 0 && len([]rune(desc)) > maxDesc {
			desc = string([]rune(desc)[:maxDesc-1]) + "…"
		}
		fmt.Fprintf(sb, "%s%s %s%s%s\n    %s\n",
			cursor, toggle, nameStyle.Render(item.name), installed, installs,
			stylePickerDesc.Render(desc))
	}

	// Count toggled items.
	toggledCount := 0
	for _, item := range p.discoverItems {
		if item.selected {
			toggledCount++
		}
	}
	baseFooter := "Space toggle · Enter detail · ←→ tabs · Esc close"
	if toggledCount > 0 {
		baseFooter = fmt.Sprintf("Space toggle · i install (%d selected) · Enter detail · ←→ tabs · Esc close", toggledCount)
	}
	footer := baseFooter
	if total > maxItems {
		footer = fmt.Sprintf("↑/↓ scroll (%d–%d/%d) · %s", start+1, end, total, baseFooter)
	}
	sb.WriteString("\n" + stylePickerDesc.Render(footer))
}

func (m Model) renderInstalledTab(sb *strings.Builder, p *pluginPanelState, contentH int) {
	if len(p.installedItems) == 0 {
		sb.WriteString(stylePickerDesc.Render("No plugins installed.\nUse /plugin marketplace add <source> then /plugin install <name>."))
		return
	}
	available := contentH - 2
	if available < 1 {
		available = 1
	}
	total := len(p.installedItems)
	start := p.selected - available/2
	if start < 0 {
		start = 0
	}
	end := start + available
	if end > total {
		end = total
		start = end - available
		if start < 0 {
			start = 0
		}
	}
	for i := start; i < end; i++ {
		item := p.installedItems[i]
		cursor := "  "
		nameStyle := stylePickerItem
		if i == p.selected {
			cursor = stylePickerItemSelected.Render("❯") + " "
			nameStyle = stylePickerItemSelected
		}
		if item.isMCPSub {
			// Indented sub-entry — cursor replaces the leading spaces.
			status := renderMCPStatus(item.mcpStatus)
			// item.name is the display name (e.g. "context7"), mcpServerName is the full key.
			displayLabel := item.name + " MCP"
			if i == p.selected {
				fmt.Fprintf(sb, "  %s └ %s · %s\n",
					stylePickerItemSelected.Render("❯"),
					nameStyle.Render(displayLabel), status)
			} else {
				fmt.Fprintf(sb, "    └ %s · %s\n",
					stylePickerItem.Render(displayLabel), status)
			}
		} else {
			enabled := ""
			if !item.enabled {
				enabled = " " + stylePickerDesc.Render("[disabled]")
			}
			fmt.Fprintf(sb, "%s%s v%s%s\n",
				cursor, nameStyle.Render(item.name), item.version, enabled)
		}
	}
	footer := "Enter detail/MCP opts · ←→ tabs · Esc close"
	if total > available {
		footer = fmt.Sprintf("↑/↓ scroll (%d–%d/%d) · %s", start+1, end, total, footer)
	}
	sb.WriteString("\n" + stylePickerDesc.Render(footer))
}

func (m Model) renderMarketplacesTab(sb *strings.Builder, p *pluginPanelState, contentH int) {
	// "+ Add Marketplace" always first.
	addCursor := "  "
	addStyle := stylePickerItem
	if p.selected == 0 {
		addCursor = stylePickerItemSelected.Render("❯") + " "
		addStyle = stylePickerItemSelected
	}
	fmt.Fprintf(sb, "%s%s\n\n", addCursor, addStyle.Render("+ Add Marketplace"))

	maxItems := (contentH - 4) / 2
	if maxItems < 1 {
		maxItems = 1
	}
	total := len(p.marketplaceItems)
	start := p.selected - 1 - maxItems/2
	if start < 0 {
		start = 0
	}
	end := start + maxItems
	if end > total {
		end = total
		start = end - maxItems
		if start < 0 {
			start = 0
		}
	}

	for i := start; i < end; i++ {
		item := p.marketplaceItems[i]
		row := i + 1 // +1 for Add row
		cursor := "  "
		nameStyle := stylePickerItem
		if p.selected == row {
			cursor = stylePickerItemSelected.Render("❯") + " "
			nameStyle = stylePickerItemSelected
		}
		pluginStr := fmt.Sprintf("%d plugin%s", item.pluginCount, pluralS(item.pluginCount))
		fmt.Fprintf(sb, "%s%s · %s\n    %s · %s\n",
			cursor, nameStyle.Render(item.name), pluginStr,
			stylePickerDesc.Render(item.source),
			stylePickerDesc.Render("updated "+item.lastUpdated))
	}

	if len(p.marketplaceItems) == 0 {
		sb.WriteString(stylePickerDesc.Render("No marketplaces configured."))
	}
	footer := "Enter add/manage · ←→ tabs · Esc close"
	if total > maxItems {
		footer = fmt.Sprintf("↑/↓ scroll (%d–%d/%d) · %s", start+1, end, total, footer)
	}
	sb.WriteString("\n" + stylePickerDesc.Render(footer))
}

func (m Model) renderErrorsTab(sb *strings.Builder, p *pluginPanelState, contentH int) {
	if len(p.errors) == 0 {
		sb.WriteString(stylePickerDesc.Render("No errors."))
		return
	}
	maxItems := contentH - 1
	if maxItems < 1 {
		maxItems = 1
	}
	total := len(p.errors)
	end := total
	if end > maxItems {
		end = maxItems
	}
	for _, e := range p.errors[:end] {
		sb.WriteString(fgOnBg(lipgloss.Color("1")).Render("✗ "+e) + "\n")
	}
	if total > end {
		fmt.Fprintf(sb, "\n%s", stylePickerDesc.Render(fmt.Sprintf("%d more errors", total-end)))
	}
}

func (m Model) renderPluginDetail(sb *strings.Builder, p *pluginPanelState, _ int) {
	switch p.tab {
	case pluginTabDiscover:
		if p.itemIdx < len(p.discoverItems) {
			item := p.discoverItems[p.itemIdx]
			sb.WriteString(styleStatusAccent.Render(item.pluginID) + "\n\n")
			sb.WriteString(item.description + "\n")
			if item.installs > 0 {
				fmt.Fprintf(sb, "\nInstalls: %s\n", formatInstalls(item.installs))
			}
			if item.category != "" {
				fmt.Fprintf(sb, "Category: %s\n", item.category)
			}
		}
	case pluginTabInstalled:
		if p.itemIdx < len(p.installedItems) {
			item := p.installedItems[p.itemIdx]
			sb.WriteString(styleStatusAccent.Render(item.pluginID) + "\n\n")
			fmt.Fprintf(sb, "Version:     %s\n", item.version)
			fmt.Fprintf(sb, "Scope:       %s\n", item.scope)
			enabledStr := "enabled"
			if !item.enabled {
				enabledStr = stylePickerDesc.Render("disabled")
			}
			fmt.Fprintf(sb, "Status:      %s\n", enabledStr)
		}
	case pluginTabMarketplaces:
		if p.itemIdx < len(p.marketplaceItems) {
			item := p.marketplaceItems[p.itemIdx]
			sb.WriteString(styleStatusAccent.Render(item.name) + "\n\n")
			fmt.Fprintf(sb, "Source:      %s\n", item.source)
			fmt.Fprintf(sb, "Plugins:     %d\n", item.pluginCount)
			fmt.Fprintf(sb, "Updated:     %s\n", item.lastUpdated)
		}
	}

	sb.WriteByte('\n')
	actions := pluginDetailActions(p)
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

func (m Model) renderPluginMCPOpts(sb *strings.Builder, p *pluginPanelState) {
	sb.WriteString(styleStatusAccent.Render(p.mcpActionTarget) + " MCP Server\n\n")

	// Find the server status.
	if m.cfg.MCPManager != nil {
		for _, srv := range m.cfg.MCPManager.Servers() {
			if srv.Name == p.mcpActionTarget {
				fmt.Fprintf(sb, "Status:  %s\n\n", renderMCPStatus(string(srv.Status)))
				break
			}
		}
	}

	actions := mcpOptsActions(p, m.cfg.MCPManager)
	for i, action := range actions {
		cursor := "  "
		style := stylePickerItem
		if i == p.mcpActionIdx {
			cursor = stylePickerItemSelected.Render("❯") + " "
			style = stylePickerItemSelected
		}
		fmt.Fprintf(sb, "%s%d. %s\n", cursor, i+1, style.Render(action))
	}
}

func (m Model) renderPluginAddMkt(sb *strings.Builder, p *pluginPanelState, _ int) {
	sb.WriteString(styleStatusAccent.Render("Add Marketplace") + "\n\n")
	sb.WriteString("Enter source (owner/repo, https://... or local path):\n\n")
	sb.WriteString("> " + p.addMktInput + styleStatusAccent.Render("▌") + "\n\n")
	sb.WriteString(stylePickerDesc.Render("Enter confirm · Escape cancel"))
}

func formatInstalls(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
