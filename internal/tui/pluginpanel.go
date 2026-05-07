package tui

import (
	"github.com/icehunter/conduit/internal/mcp"
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

type pluginMarketplaceAddMsg struct {
	name string
	err  error
}

// pluginPanelReloadMsg triggers a full reload of the plugin panel from disk.
// Sent after install/uninstall to get correct data (version, description, sort order).
type pluginPanelReloadMsg struct {
	mcpMgr *mcp.Manager
	// preserve state across the reload
	tab    pluginPanelTab
	errors []string // errors to carry forward (install failures etc.)
}
