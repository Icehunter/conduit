package tui

import (
	"context"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/settings"
)

// ---- Key handler -----------------------------------------------------------

func (m Model) handlePluginPanelKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.pluginPanel
	key := msg.String()

	switch p.view {
	case pluginViewList:
		return m.handlePluginListKey(p, key)
	case pluginViewDetail:
		return m.handlePluginDetailKey(p, key)
	case pluginViewMCPOpts:
		return m.handlePluginMCPOptsKey(p, key)
	case pluginViewAddMkt:
		return m.handlePluginAddMktKey(p, key)
	}
	m.pluginPanel = p
	return m, nil
}

func (m Model) handlePluginListKey(p *pluginPanelState, key string) (Model, tea.Cmd) {
	// Route keypresses to search on the Discover tab.
	// Structural/action keys are always handled directly; everything else
	// (including letters used for nav when search is empty) goes to the search box.
	if p.tab == pluginTabDiscover {
		isStructural := key == "esc" || key == "ctrl+c" || key == "enter" ||
			key == "up" || key == "down" || key == "left" || key == "right" ||
			key == " " || key == "backspace" || key == "ctrl+h" ||
			key == "tab" || key == "i" || len(key) > 1 // multi-char = special key

		if !isStructural && len(key) == 1 {
			p.discoverSearch += key
			p.applyDiscoverFilter()
			if p.selected >= len(p.discoverFiltered) {
				p.selected = 0
			}
			m.pluginPanel = p
			return m, nil
		}
	}

	switch key {
	case "backspace", "ctrl+h":
		if p.tab == pluginTabDiscover && len(p.discoverSearch) > 0 {
			runes := []rune(p.discoverSearch)
			p.discoverSearch = string(runes[:len(runes)-1])
			p.applyDiscoverFilter()
			if p.selected >= len(p.discoverFiltered) {
				p.selected = 0
			}
		}
	case "left":
		p.tab = pluginPanelTab((int(p.tab) + len(pluginTabNames) - 1) % len(pluginTabNames))
		p.selected = 0
		p.discoverSearch = ""
		p.applyDiscoverFilter()
	case "right":
		p.tab = pluginPanelTab((int(p.tab) + 1) % len(pluginTabNames))
		p.selected = 0
		p.discoverSearch = ""
		p.applyDiscoverFilter()
	case "up":
		if p.selected > 0 {
			p.selected--
		}
	case "down":
		if p.selected < p.currentListLen()-1 {
			p.selected++
		}
	case "space":
		if p.tab == pluginTabDiscover && len(p.discoverFiltered) > 0 {
			idx := p.discoverFiltered[p.selected]
			p.discoverItems[idx].selected = !p.discoverItems[idx].selected
		}
	case "i":
		if p.tab == pluginTabDiscover && len(p.discoverFiltered) > 0 {
			// Collect toggled items. If none are toggled, install the current row.
			var toInstall []string
			for _, item := range p.discoverItems {
				if item.selected {
					toInstall = append(toInstall, item.pluginID)
				}
			}
			if len(toInstall) == 0 {
				idx := p.discoverFiltered[p.selected]
				toInstall = append(toInstall, p.discoverItems[idx].pluginID)
			}
			cwd, _ := os.Getwd()
			trusted := !m.cfg.NeedsTrust
			var cmds []tea.Cmd
			mgr := m.cfg.MCPManager
			for _, pluginID := range toInstall {
				pid := pluginID
				cmds = append(cmds, func() tea.Msg {
					_, err := plugins.Install(context.Background(), pid, "user", cwd)
					if err == nil && mgr != nil {
						mgr.SyncPluginServers(context.Background(), cwd, trusted)
					}
					return pluginInstallMsg{pluginID: pid, err: err}
				})
			}
			m.pluginPanel = p
			return m, tea.Batch(cmds...)
		}
	case "enter":
		n := p.currentListLen()
		if n == 0 {
			break
		}
		switch p.tab {
		case pluginTabDiscover:
			if len(p.discoverFiltered) > 0 {
				p.itemIdx = p.discoverFiltered[p.selected]
				p.view = pluginViewDetail
				p.selected = 0
			}
		case pluginTabInstalled:
			item := p.installedItems[p.selected]
			if item.isMCPSub {
				p.mcpActionTarget = item.mcpServerName
				p.mcpActionIdx = 0
				p.view = pluginViewMCPOpts
			} else {
				p.itemIdx = p.selected
				p.view = pluginViewDetail
				p.selected = 0
			}
		case pluginTabMarketplaces:
			if p.selected == 0 {
				// "Add Marketplace"
				p.addMktInput = ""
				p.addMktMode = true
				p.view = pluginViewAddMkt
			} else {
				p.itemIdx = p.selected - 1 // -1 for the "Add" row
				p.view = pluginViewDetail
				p.selected = 0
			}
		}
	case "esc", "ctrl+c":
		m.pluginPanel = nil
		m.refreshViewport()
		return m, nil
	}

	m.pluginPanel = p
	return m, nil
}

func (m Model) handlePluginDetailKey(p *pluginPanelState, key string) (Model, tea.Cmd) {
	actions := pluginDetailActions(p)
	switch key {
	case "up":
		if p.selected > 0 {
			p.selected--
		}
	case "down":
		if p.selected < len(actions)-1 {
			p.selected++
		}
	case "enter":
		if p.selected < len(actions) {
			return m.execPluginDetailAction(p, actions[p.selected])
		}
	case "esc":
		p.view = pluginViewList
		p.selected = 0
	case "ctrl+c":
		m.pluginPanel = nil
		m.refreshViewport()
		return m, nil
	}
	m.pluginPanel = p
	return m, nil
}

func (m Model) handlePluginMCPOptsKey(p *pluginPanelState, key string) (Model, tea.Cmd) {
	actions := mcpOptsActions(p, m.cfg.MCPManager)
	switch key {
	case "up":
		if p.mcpActionIdx > 0 {
			p.mcpActionIdx--
		}
	case "down":
		if p.mcpActionIdx < len(actions)-1 {
			p.mcpActionIdx++
		}
	case "enter":
		if p.mcpActionIdx < len(actions) {
			return m.execMCPOptAction(p, actions[p.mcpActionIdx])
		}
	case "esc":
		p.view = pluginViewList
		p.selected = 0
	case "ctrl+c":
		m.pluginPanel = nil
		m.refreshViewport()
		return m, nil
	}
	m.pluginPanel = p
	return m, nil
}

func (m Model) handlePluginAddMktKey(p *pluginPanelState, key string) (Model, tea.Cmd) {
	switch key {
	case "enter":
		if p.addMktInput != "" {
			src := p.addMktInput
			name := deriveMarketplaceNameFromSource(src)
			p.addMktInput = ""
			p.addMktMode = false
			p.view = pluginViewList
			p.selected = 0
			m.pluginPanel = p
			return m, func() tea.Msg {
				err := plugins.MarketplaceAdd(context.Background(), name, src, nil)
				return pluginMarketplaceAddMsg{name: name, err: err}
			}
		}
		p.view = pluginViewList
		p.selected = 0
	case "esc":
		p.addMktInput = ""
		p.addMktMode = false
		p.view = pluginViewList
		p.selected = 0
	case "backspace", "ctrl+h":
		runes := []rune(p.addMktInput)
		if len(runes) > 0 {
			p.addMktInput = string(runes[:len(runes)-1])
		}
	case "ctrl+c":
		m.pluginPanel = nil
		m.refreshViewport()
		return m, nil
	default:
		if len(key) == 1 {
			p.addMktInput += key
		}
	}
	m.pluginPanel = p
	return m, nil
}

func pluginDetailActions(p *pluginPanelState) []string {
	var actions []string
	switch p.tab {
	case pluginTabDiscover:
		if p.itemIdx < len(p.discoverItems) {
			item := p.discoverItems[p.itemIdx]
			if item.installed {
				actions = append(actions, "Uninstall")
			} else {
				actions = append(actions, "Install")
			}
		}
	case pluginTabInstalled:
		if p.itemIdx < len(p.installedItems) {
			item := p.installedItems[p.itemIdx]
			if item.enabled {
				actions = append(actions, "Disable")
			} else {
				actions = append(actions, "Enable")
			}
			actions = append(actions, "Update now", "Uninstall")
		}
	case pluginTabMarketplaces:
		actions = append(actions, "Update", "Remove")
	}
	actions = append(actions, "Back")
	return actions
}

func mcpOptsActions(p *pluginPanelState, mgr *mcp.Manager) []string {
	if mgr == nil {
		return []string{"Back"}
	}
	for _, srv := range mgr.Servers() {
		if srv.Name == p.mcpActionTarget {
			var actions []string
			if !srv.Disabled {
				actions = append(actions, "Reconnect")
			}
			if srv.Disabled {
				actions = append(actions, "Enable")
			} else {
				actions = append(actions, "Disable")
			}
			actions = append(actions, "Back")
			return actions
		}
	}
	return []string{"Back"}
}

func (m Model) execPluginDetailAction(p *pluginPanelState, action string) (Model, tea.Cmd) {
	cwd, _ := os.Getwd()
	trusted := !m.cfg.NeedsTrust
	switch action {
	case "Back":
		p.view = pluginViewList
		p.selected = 0
	case "Install":
		if p.itemIdx < len(p.discoverItems) {
			pluginID := p.discoverItems[p.itemIdx].pluginID
			mgr := m.cfg.MCPManager
			return m, func() tea.Msg {
				_, err := plugins.Install(context.Background(), pluginID, "user", cwd)
				if err == nil && mgr != nil {
					mgr.SyncPluginServers(context.Background(), cwd, trusted)
				}
				return pluginInstallMsg{pluginID: pluginID, err: err}
			}
		}
	case "Uninstall":
		switch p.tab {
		case pluginTabDiscover:
			if p.itemIdx < len(p.discoverItems) {
				pluginID := p.discoverItems[p.itemIdx].pluginID
				_ = plugins.Uninstall(pluginID, "user", cwd)
				if m.cfg.MCPManager != nil {
					m.cfg.MCPManager.SyncPluginServers(context.Background(), cwd, trusted)
				}
				m.pluginPanel = p
				return m, reloadPluginPanelCmd(m.cfg.MCPManager, pluginTabDiscover, p.errors)
			}
		case pluginTabInstalled:
			if p.itemIdx < len(p.installedItems) {
				pluginID := p.installedItems[p.itemIdx].pluginID
				_ = plugins.Uninstall(pluginID, "user", cwd)
				if m.cfg.MCPManager != nil {
					m.cfg.MCPManager.SyncPluginServers(context.Background(), cwd, trusted)
				}
				// Reload panel from disk — gets correct descriptions, sort order, etc.
				m.pluginPanel = p
				return m, reloadPluginPanelCmd(m.cfg.MCPManager, pluginTabInstalled, p.errors)
			}
		}
		p.view = pluginViewList
		p.selected = 0
	case "Disable":
		if p.itemIdx < len(p.installedItems) {
			pluginID := p.installedItems[p.itemIdx].pluginID
			_ = settings.SetPluginEnabled(pluginID, false)
			p.installedItems[p.itemIdx].enabled = false
		}
		p.view = pluginViewList
		p.selected = 0
	case "Enable":
		if p.itemIdx < len(p.installedItems) {
			pluginID := p.installedItems[p.itemIdx].pluginID
			_ = settings.SetPluginEnabled(pluginID, true)
			p.installedItems[p.itemIdx].enabled = true
		}
		p.view = pluginViewList
		p.selected = 0
	case "Update now":
		p.view = pluginViewList
		p.selected = 0
	case "Update", "Remove":
		if p.tab == pluginTabMarketplaces && p.itemIdx < len(p.marketplaceItems) {
			mktName := p.marketplaceItems[p.itemIdx].name
			if action == "Remove" {
				_ = plugins.MarketplaceRemove(mktName)
				p.marketplaceItems = append(p.marketplaceItems[:p.itemIdx], p.marketplaceItems[p.itemIdx+1:]...)
			} else {
				_ = plugins.MarketplaceUpdate(context.Background(), mktName)
			}
		}
		p.view = pluginViewList
		p.selected = 0
	}
	m.pluginPanel = p
	return m, nil
}

func (m Model) execMCPOptAction(p *pluginPanelState, action string) (Model, tea.Cmd) {
	cwd, _ := os.Getwd()
	trusted := !m.cfg.NeedsTrust
	switch action {
	case "Back":
		p.view = pluginViewList
		p.selected = 0
	case "Reconnect":
		if m.cfg.MCPManager != nil {
			srvName := p.mcpActionTarget
			mgr := m.cfg.MCPManager
			go func() { _ = mgr.Reconnect(context.Background(), srvName, cwd, trusted) }()
		}
		p.view = pluginViewList
		p.selected = 0
	case "Disable":
		_ = mcp.SetDisabled(p.mcpActionTarget, cwd, true)
		if m.cfg.MCPManager != nil {
			srvName := p.mcpActionTarget
			go func() { m.cfg.MCPManager.DisconnectServer(srvName) }()
		}
		// Update status in installed items.
		for i, item := range p.installedItems {
			if item.isMCPSub && item.mcpServerName == p.mcpActionTarget {
				p.installedItems[i].mcpStatus = "disabled"
			}
		}
		p.view = pluginViewList
		p.selected = 0
	case "Enable":
		_ = mcp.SetDisabled(p.mcpActionTarget, cwd, false)
		if m.cfg.MCPManager != nil {
			srvName := p.mcpActionTarget
			mgr := m.cfg.MCPManager
			go func() { _ = mgr.Reconnect(context.Background(), srvName, cwd, trusted) }()
		}
		for i, item := range p.installedItems {
			if item.isMCPSub && item.mcpServerName == p.mcpActionTarget {
				p.installedItems[i].mcpStatus = "pending"
			}
		}
		p.view = pluginViewList
		p.selected = 0
	}
	m.pluginPanel = p
	return m, nil
}

// splitPluginID splits "name@marketplace" into components.
func splitPluginID(id string) (name, marketplace string) {
	at := strings.LastIndex(id, "@")
	if at < 0 {
		return id, "claude-plugins-official"
	}
	return id[:at], id[at+1:]
}

func deriveMarketplaceNameFromSource(source string) string {
	if idx := strings.LastIndex(source, "/"); idx >= 0 {
		name := source[idx+1:]
		name = strings.TrimSuffix(name, ".git")
		if name != "" {
			return name
		}
	}
	return source
}
