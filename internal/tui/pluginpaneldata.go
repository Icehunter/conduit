package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/settings"
)

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
				enabled:     settings.PluginEnabled(id),
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

// hasSelectedItems reports whether any discover item has been space-toggled.
func (p *pluginPanelState) hasSelectedItems() bool {
	for _, item := range p.discoverItems {
		if item.selected {
			return true
		}
	}
	return false
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
