package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/settings"
)

// ---- Plugin panel tab / view types ----------------------------------------

type pluginPanelTab int

const (
	pluginTabDiscover     pluginPanelTab = 0
	pluginTabInstalled    pluginPanelTab = 1
	pluginTabMarketplaces pluginPanelTab = 2
	pluginTabErrors       pluginPanelTab = 3
)

var pluginTabNames = []string{"Discover", "Installed", "Marketplaces", "Errors"}

type pluginPanelView int

const (
	pluginViewList    pluginPanelView = 0
	pluginViewDetail  pluginPanelView = 1
	pluginViewMCPOpts pluginPanelView = 2
	pluginViewAddMkt  pluginPanelView = 3
)

// ---- Item types ------------------------------------------------------------

type discoverItem struct {
	pluginID    string
	name        string
	description string
	category    string
	installs    int
	installed   bool
	selected    bool // toggle for batch install
}

type installedItem struct {
	isMCPSub       bool
	pluginID       string // for plugin rows: "name@marketplace"
	name           string
	marketplace    string
	version        string
	scope          string
	enabled        bool
	hasMCP         bool
	mcpServerName  string // for MCP sub-rows: full server key e.g. "plugin:context7:context7"
	mcpStatus      string
	parentPluginID string // for MCP sub-rows: pluginID of parent plugin
}

type pluginMarketplaceItem struct {
	name        string
	source      string
	lastUpdated string
	pluginCount int
}

// ---- Panel state -----------------------------------------------------------

type pluginPanelState struct {
	tab  pluginPanelTab
	view pluginPanelView

	selected int // cursor in current list/actions
	itemIdx  int // preserved on drill-down (which item was selected)

	// Discover
	discoverItems    []discoverItem
	discoverSearch   string
	discoverFiltered []int // indices into discoverItems after filtering
	loadingCounts    bool

	// Installed
	installedItems  []installedItem
	mcpActionTarget string // server name for MCP opts view
	mcpActionIdx    int    // action cursor in MCP opts view

	// Marketplaces
	marketplaceItems []pluginMarketplaceItem
	addMktMode       bool   // true when "Add Marketplace" input is active
	addMktInput      string // text input value

	// Errors
	errors []string
}

// pluginCountsMsg carries async-loaded install counts back to the model.
type pluginCountsMsg struct {
	counts map[string]int
	err    error
}

// pluginInstallMsg carries the result of an async install back to the model.
type pluginInstallMsg struct {
	pluginID string
	err      error
}

// pluginPanelReloadMsg triggers a full reload of the plugin panel from disk.
// Sent after install/uninstall to get correct data (version, description, sort order).
type pluginPanelReloadMsg struct {
	mcpMgr *mcp.Manager
	// preserve state across the reload
	tab      pluginPanelTab
	errors   []string // errors to carry forward (install failures etc.)
}

// reloadPluginPanelCmd returns a tea.Cmd that reloads panel data from disk.
func reloadPluginPanelCmd(mgr *mcp.Manager, tab pluginPanelTab, existingErrors []string) tea.Cmd {
	return func() tea.Msg {
		return pluginPanelReloadMsg{mcpMgr: mgr, tab: tab, errors: existingErrors}
	}
}

// rebuildPluginPanel rebuilds the full panel state from disk, preserving the
// current tab and carrying forward any error messages. Used after install/uninstall.
func rebuildPluginPanel(msg pluginPanelReloadMsg) *pluginPanelState {
	installed, _ := plugins.LoadInstalledPlugins()

	p := &pluginPanelState{
		tab:           msg.tab,
		loadingCounts: true,
		errors:        msg.errors,
	}

	installedIDs := map[string]bool{}
	if installed != nil {
		for id, entries := range installed.Plugins {
			if len(entries) == 0 {
				continue
			}
			name, marketplace := splitPluginID(id)
			e := entries[0]
			installedIDs[id] = true
			p.installedItems = append(p.installedItems, installedItem{
				pluginID:    id,
				name:        name,
				marketplace: marketplace,
				version:     e.Version,
				scope:       e.Scope,
				enabled:     true, // default; override below
			})
		}
		// Sort installed by name.
		sort.Slice(p.installedItems, func(i, j int) bool {
			return p.installedItems[i].name < p.installedItems[j].name
		})
	}

	// Inject MCP sub-entries.
	p.injectMCPSubEntries(msg.mcpMgr)

	// Build marketplace items.
	if known, err := plugins.LoadKnownMarketplaces(); err == nil {
		names := make([]string, 0, len(known))
		for n := range known {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			e := known[n]
			src := e.Source.Repo
			if src == "" {
				src = e.Source.URL
			}
			if src == "" {
				src = e.Source.Path
			}
			cnt := 0
			if manifest, err := plugins.LoadMarketplaceManifest(n); err == nil {
				cnt = len(manifest.Plugins)
			}
			p.marketplaceItems = append(p.marketplaceItems, pluginMarketplaceItem{
				name:        n,
				source:      src,
				lastUpdated: e.LastUpdated,
				pluginCount: cnt,
			})
		}
	}

	// Build discover items (excludes installed).
	p.buildDiscoverItems(installedIDs)
	return p
}

// newPluginPanel creates a pluginPanelState from the JSON payload in a "plugin-panel" command result.
func newPluginPanel(jsonText string) (*pluginPanelState, error) {
	var data commands.PluginPanelData
	if err := json.Unmarshal([]byte(jsonText), &data); err != nil {
		return nil, fmt.Errorf("plugin panel: parse: %w", err)
	}

	p := &pluginPanelState{
		tab:           pluginTabDiscover,
		loadingCounts: true,
		errors:        data.Errors,
	}

	// Build installed items (will add MCP sub-entries later when MCPManager is available).
	for _, e := range data.Installed {
		p.installedItems = append(p.installedItems, installedItem{
			pluginID:    e.ID,
			name:        e.Name,
			marketplace: e.Marketplace,
			version:     e.Version,
			scope:       e.Scope,
			enabled:     e.Enabled,
		})
	}

	// Build marketplace items.
	for _, r := range data.Marketplaces {
		p.marketplaceItems = append(p.marketplaceItems, pluginMarketplaceItem{
			name:        r.Name,
			source:      r.Source,
			lastUpdated: r.LastUpdated,
			pluginCount: r.PluginCount,
		})
	}

	return p, nil
}

// buildDiscoverItems populates p.discoverItems from all configured marketplaces.
// Called synchronously (on panel open) to load marketplace.json files; install counts
// are loaded asynchronously.
func (p *pluginPanelState) buildDiscoverItems(installedIDs map[string]bool) {
	known, err := plugins.LoadKnownMarketplaces()
	if err != nil {
		p.errors = append(p.errors, fmt.Sprintf("load marketplaces: %v", err))
		return
	}
	names := make([]string, 0, len(known))
	for n := range known {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, mktName := range names {
		manifest, err := plugins.LoadMarketplaceManifest(mktName)
		if err != nil {
			p.errors = append(p.errors, fmt.Sprintf("%s: %v", mktName, err))
			continue
		}
		for _, plug := range manifest.Plugins {
			id := plug.Name + "@" + mktName
			// Discover tab shows only non-installed plugins (to find new ones).
			// Installed tab shows what's already installed.
			if installedIDs[id] {
				continue
			}
			p.discoverItems = append(p.discoverItems, discoverItem{
				pluginID:    id,
				name:        plug.Name,
				description: plug.Description,
				category:    plug.Category,
				installed:   false,
			})
		}
	}
	p.applyDiscoverFilter()
}

// injectMCPSubEntries inserts MCP sub-entries into installedItems using the live manager.
func (p *pluginPanelState) injectMCPSubEntries(mgr *mcp.Manager) {
	if mgr == nil {
		return
	}
	servers := mgr.Servers()

	// Build a map: pluginName → []server
	pluginServers := map[string][]*mcp.ConnectedServer{}
	for _, srv := range servers {
		if srv.Config.PluginName != "" {
			pluginServers[srv.Config.PluginName] = append(pluginServers[srv.Config.PluginName], srv)
		}
	}

	var result []installedItem
	for _, item := range p.installedItems {
		if item.isMCPSub {
			continue // skip old sub-entries
		}
		srvList := pluginServers[item.name]
		item.hasMCP = len(srvList) > 0
		result = append(result, item)
		for _, srv := range srvList {
			// srv.Name is "plugin:pluginName:serverName" — extract the display name.
			displayName := srv.Name
			if parts := strings.SplitN(srv.Name, ":", 3); len(parts) == 3 {
				displayName = parts[2]
			}
			result = append(result, installedItem{
				isMCPSub:       true,
				mcpServerName:  srv.Name, // full key for disable/reconnect ops
				name:           displayName,
				mcpStatus:      string(srv.Status),
				parentPluginID: item.pluginID, // stable ID, not index
			})
		}
	}
	p.installedItems = result
}

// applyDiscoverFilter recomputes discoverFiltered based on discoverSearch.
func (p *pluginPanelState) applyDiscoverFilter() {
	query := strings.ToLower(p.discoverSearch)
	p.discoverFiltered = nil
	for i, item := range p.discoverItems {
		if query == "" ||
			strings.Contains(strings.ToLower(item.name), query) ||
			strings.Contains(strings.ToLower(item.description), query) ||
			strings.Contains(strings.ToLower(item.category), query) {
			p.discoverFiltered = append(p.discoverFiltered, i)
		}
	}
}

// applyInstallCounts sets installs on discoverItems and re-sorts by popularity.
func (p *pluginPanelState) applyInstallCounts(counts map[string]int) {
	for i := range p.discoverItems {
		p.discoverItems[i].installs = counts[p.discoverItems[i].pluginID]
	}
	sort.SliceStable(p.discoverItems, func(i, j int) bool {
		return p.discoverItems[i].installs > p.discoverItems[j].installs
	})
	p.applyDiscoverFilter()
}

// currentListLen returns the number of items in the current tab's list view.
func (p *pluginPanelState) currentListLen() int {
	switch p.tab {
	case pluginTabDiscover:
		return len(p.discoverFiltered)
	case pluginTabInstalled:
		return len(p.installedItems)
	case pluginTabMarketplaces:
		return len(p.marketplaceItems) + 1 // +1 for "Add Marketplace"
	case pluginTabErrors:
		return len(p.errors)
	}
	return 0
}

// ---- Key handler -----------------------------------------------------------

func (m Model) handlePluginPanelKey(msg tea.KeyMsg) (Model, tea.Cmd) {
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
			key == "tab" || len(key) > 1 // multi-char = special key
		// "i" installs when search is empty; goes to search when search is non-empty.
		isInstall := key == "i" && p.discoverSearch == ""
		// h/j/k/l/q are nav only when search is empty.
		isNavLetter := p.discoverSearch == "" && (key == "h" || key == "j" || key == "k" || key == "l" || key == "q")

		if !isStructural && !isInstall && !isNavLetter && len(key) == 1 {
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
	case "left", "h":
		p.tab = pluginPanelTab((int(p.tab) + len(pluginTabNames) - 1) % len(pluginTabNames))
		p.selected = 0
		p.discoverSearch = ""
		p.applyDiscoverFilter()
	case "right", "l":
		p.tab = pluginPanelTab((int(p.tab) + 1) % len(pluginTabNames))
		p.selected = 0
		p.discoverSearch = ""
		p.applyDiscoverFilter()
	case "up", "k":
		if p.selected > 0 {
			p.selected--
		}
	case "down", "j":
		if p.selected < p.currentListLen()-1 {
			p.selected++
		}
	case " ":
		if p.tab == pluginTabDiscover && len(p.discoverFiltered) > 0 {
			idx := p.discoverFiltered[p.selected]
			p.discoverItems[idx].selected = !p.discoverItems[idx].selected
		}
	case "i":
		if p.tab == pluginTabDiscover && len(p.discoverFiltered) > 0 {
			// Collect toggled items. If none are toggled, do nothing.
			var toInstall []string
			for _, item := range p.discoverItems {
				if item.selected {
					toInstall = append(toInstall, item.pluginID)
				}
			}
			if len(toInstall) == 0 {
				break // nothing toggled — i does nothing without a selection
			}
			cwd, _ := os.Getwd()
			var cmds []tea.Cmd
			mgr := m.cfg.MCPManager
			for _, pluginID := range toInstall {
				pid := pluginID
				cmds = append(cmds, func() tea.Msg {
					_, err := plugins.Install(pid, "user", cwd)
					if err == nil && mgr != nil {
						mgr.SyncPluginServers(context.Background(), cwd)
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
	case "esc", "q", "ctrl+c":
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
	case "up", "k":
		if p.selected > 0 {
			p.selected--
		}
	case "down", "j":
		if p.selected < len(actions)-1 {
			p.selected++
		}
	case "enter":
		if p.selected < len(actions) {
			return m.execPluginDetailAction(p, actions[p.selected])
		}
	case "esc", "q":
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
	case "up", "k":
		if p.mcpActionIdx > 0 {
			p.mcpActionIdx--
		}
	case "down", "j":
		if p.mcpActionIdx < len(actions)-1 {
			p.mcpActionIdx++
		}
	case "enter":
		if p.mcpActionIdx < len(actions) {
			return m.execMCPOptAction(p, actions[p.mcpActionIdx])
		}
	case "esc", "q":
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
			go func() { _ = plugins.MarketplaceAdd(name, src, nil) }()
			p.addMktInput = ""
			p.addMktMode = false
			// Reload marketplaces.
			if known, err := plugins.LoadKnownMarketplaces(); err == nil {
				p.marketplaceItems = nil
				names := make([]string, 0, len(known))
				for n := range known {
					names = append(names, n)
				}
				sort.Strings(names)
				for _, n := range names {
					e := known[n]
					src2 := e.Source.Repo
					if src2 == "" {
						src2 = e.Source.URL
					}
					if src2 == "" {
						src2 = e.Source.Path
					}
					cnt := 0
					if manifest, err := plugins.LoadMarketplaceManifest(n); err == nil {
						cnt = len(manifest.Plugins)
					}
					p.marketplaceItems = append(p.marketplaceItems, pluginMarketplaceItem{
						name:        n,
						source:      src2,
						lastUpdated: e.LastUpdated,
						pluginCount: cnt,
					})
				}
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
	switch action {
	case "Back":
		p.view = pluginViewList
		p.selected = 0
	case "Install":
		if p.itemIdx < len(p.discoverItems) {
			pluginID := p.discoverItems[p.itemIdx].pluginID
			mgr := m.cfg.MCPManager
			return m, func() tea.Msg {
				_, err := plugins.Install(pluginID, "user", cwd)
				if err == nil && mgr != nil {
					mgr.SyncPluginServers(context.Background(), cwd)
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
					m.cfg.MCPManager.SyncPluginServers(context.Background(), cwd)
				}
				m.pluginPanel = p
				return m, reloadPluginPanelCmd(m.cfg.MCPManager, pluginTabDiscover, p.errors)
			}
		case pluginTabInstalled:
			if p.itemIdx < len(p.installedItems) {
				pluginID := p.installedItems[p.itemIdx].pluginID
				_ = plugins.Uninstall(pluginID, "user", cwd)
				if m.cfg.MCPManager != nil {
					m.cfg.MCPManager.SyncPluginServers(context.Background(), cwd)
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
				_ = plugins.MarketplaceUpdate(mktName)
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
	switch action {
	case "Back":
		p.view = pluginViewList
		p.selected = 0
	case "Reconnect":
		if m.cfg.MCPManager != nil {
			srvName := p.mcpActionTarget
			mgr := m.cfg.MCPManager
			go func() { _ = mgr.Reconnect(context.Background(), srvName, cwd) }()
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
			go func() { _ = mgr.Reconnect(context.Background(), srvName, cwd) }()
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
	panelH := m.height - 1
	if panelH < 4 {
		panelH = 4
	}
	innerW := w - 6

	var sb strings.Builder

	// Title bar with tabs.
	var tabParts []string
	for i, name := range pluginTabNames {
		if pluginPanelTab(i) == p.tab {
			tabParts = append(tabParts, styleStatusAccent.Render(name))
		} else {
			tabParts = append(tabParts, stylePickerDesc.Render(name))
		}
	}
	sb.WriteString(strings.Join(tabParts, stylePickerDesc.Render(" | ")))
	sb.WriteByte('\n')
	sb.WriteString(stylePickerDesc.Render(strings.Repeat("─", innerW)))
	sb.WriteString("\n\n")

	// Inner content height: panelH minus border(2) minus padding(2) minus title(1) minus separator(1) minus blank(1) = panelH - 7
	contentH := panelH - 7
	if contentH < 4 {
		contentH = 4
	}

	switch p.view {
	case pluginViewList:
		m.renderPluginList(&sb, p, innerW, contentH)
	case pluginViewDetail:
		m.renderPluginDetail(&sb, p, innerW)
	case pluginViewMCPOpts:
		m.renderPluginMCPOpts(&sb, p)
	case pluginViewAddMkt:
		m.renderPluginAddMkt(&sb, p, innerW)
	}

	style := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		BorderBackground(colorBg).
		Background(colorModalBg).
		PaddingLeft(2).PaddingRight(2).PaddingTop(1).PaddingBottom(1).
		Width(w - 2).
		Height(panelH - 2)
	return style.Render(sb.String())
}

func (m Model) renderPluginList(sb *strings.Builder, p *pluginPanelState, innerW, contentH int) {
	switch p.tab {
	case pluginTabDiscover:
		m.renderDiscoverTab(sb, p, innerW, contentH)
	case pluginTabInstalled:
		m.renderInstalledTab(sb, p)
	case pluginTabMarketplaces:
		m.renderMarketplacesTab(sb, p)
	case pluginTabErrors:
		m.renderErrorsTab(sb, p)
	}
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
			installed = " " + fgOnModal(lipgloss.Color("2")).Render("[installed]")
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
		sb.WriteString(fmt.Sprintf("%s%s %s%s%s\n    %s\n",
			cursor, toggle, nameStyle.Render(item.name), installed, installs,
			stylePickerDesc.Render(desc)))
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
		footer = fmt.Sprintf("↑↓ scroll (%d–%d/%d) · %s", start+1, end, total, baseFooter)
	}
	sb.WriteString("\n" + stylePickerDesc.Render(footer))
}

func (m Model) renderInstalledTab(sb *strings.Builder, p *pluginPanelState) {
	if len(p.installedItems) == 0 {
		sb.WriteString(stylePickerDesc.Render("No plugins installed.\nUse /plugin marketplace add <source> then /plugin install <name>."))
		return
	}
	for i, item := range p.installedItems {
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
				sb.WriteString(fmt.Sprintf("  %s └ %s · %s\n",
					stylePickerItemSelected.Render("❯"),
					nameStyle.Render(displayLabel), status))
			} else {
				sb.WriteString(fmt.Sprintf("    └ %s · %s\n",
					stylePickerItem.Render(displayLabel), status))
			}
		} else {
			enabled := ""
			if !item.enabled {
				enabled = " " + stylePickerDesc.Render("[disabled]")
			}
			sb.WriteString(fmt.Sprintf("%s%s v%s%s\n",
				cursor, nameStyle.Render(item.name), item.version, enabled))
		}
	}
	sb.WriteString("\n" + stylePickerDesc.Render("Enter detail/MCP opts · ←→ tabs · Esc close"))
}

func (m Model) renderMarketplacesTab(sb *strings.Builder, p *pluginPanelState) {
	// "+ Add Marketplace" always first.
	addCursor := "  "
	addStyle := stylePickerItem
	if p.selected == 0 {
		addCursor = stylePickerItemSelected.Render("❯") + " "
		addStyle = stylePickerItemSelected
	}
	sb.WriteString(fmt.Sprintf("%s%s\n\n", addCursor, addStyle.Render("+ Add Marketplace")))

	for i, item := range p.marketplaceItems {
		row := i + 1 // +1 for Add row
		cursor := "  "
		nameStyle := stylePickerItem
		if p.selected == row {
			cursor = stylePickerItemSelected.Render("❯") + " "
			nameStyle = stylePickerItemSelected
		}
		pluginStr := fmt.Sprintf("%d plugin%s", item.pluginCount, pluralS(item.pluginCount))
		sb.WriteString(fmt.Sprintf("%s%s · %s\n    %s · %s\n",
			cursor, nameStyle.Render(item.name), pluginStr,
			stylePickerDesc.Render(item.source),
			stylePickerDesc.Render("updated "+item.lastUpdated)))
	}

	if len(p.marketplaceItems) == 0 {
		sb.WriteString(stylePickerDesc.Render("No marketplaces configured."))
	}
	sb.WriteString("\n" + stylePickerDesc.Render("Enter add/manage · ←→ tabs · Esc close"))
}

func (m Model) renderErrorsTab(sb *strings.Builder, p *pluginPanelState) {
	if len(p.errors) == 0 {
		sb.WriteString(stylePickerDesc.Render("No errors."))
		return
	}
	for _, e := range p.errors {
		sb.WriteString(fgOnModal(lipgloss.Color("1")).Render("✗ "+e) + "\n")
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
				sb.WriteString(fmt.Sprintf("\nInstalls: %s\n", formatInstalls(item.installs)))
			}
			if item.category != "" {
				sb.WriteString(fmt.Sprintf("Category: %s\n", item.category))
			}
		}
	case pluginTabInstalled:
		if p.itemIdx < len(p.installedItems) {
			item := p.installedItems[p.itemIdx]
			sb.WriteString(styleStatusAccent.Render(item.pluginID) + "\n\n")
			sb.WriteString(fmt.Sprintf("Version:     %s\n", item.version))
			sb.WriteString(fmt.Sprintf("Scope:       %s\n", item.scope))
			enabledStr := "enabled"
			if !item.enabled {
				enabledStr = stylePickerDesc.Render("disabled")
			}
			sb.WriteString(fmt.Sprintf("Status:      %s\n", enabledStr))
		}
	case pluginTabMarketplaces:
		if p.itemIdx < len(p.marketplaceItems) {
			item := p.marketplaceItems[p.itemIdx]
			sb.WriteString(styleStatusAccent.Render(item.name) + "\n\n")
			sb.WriteString(fmt.Sprintf("Source:      %s\n", item.source))
			sb.WriteString(fmt.Sprintf("Plugins:     %d\n", item.pluginCount))
			sb.WriteString(fmt.Sprintf("Updated:     %s\n", item.lastUpdated))
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
		sb.WriteString(fmt.Sprintf("%s%d. %s\n", cursor, i+1, style.Render(action)))
	}
}

func (m Model) renderPluginMCPOpts(sb *strings.Builder, p *pluginPanelState) {
	sb.WriteString(styleStatusAccent.Render(p.mcpActionTarget) + " MCP Server\n\n")

	// Find the server status.
	if m.cfg.MCPManager != nil {
		for _, srv := range m.cfg.MCPManager.Servers() {
			if srv.Name == p.mcpActionTarget {
				sb.WriteString(fmt.Sprintf("Status:  %s\n\n", renderMCPStatus(string(srv.Status))))
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
		sb.WriteString(fmt.Sprintf("%s%d. %s\n", cursor, i+1, style.Render(action)))
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
