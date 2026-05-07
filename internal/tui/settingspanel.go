package tui

// Settings panel — full-screen takeover mirroring the TS Settings component.
// Matches the floating panel visual language: rounded border, gradient slash
// ornament header, color-only tabs, ❯ cursor for list items, no underlines.
//
// Tabs: Status · Config · Stats · Usage
// Navigation:
//   ←/→    switch main tabs (from header); switch subtabs (from Stats content)
//   ↓/Enter drop into list from header
//   ↑       return to header from top of list
//   ↑/↓      navigate list
//   ←/→     cycle enum value when on an enum item in Config list
//   r        cycle date range in Stats
//   Esc/q    close (with search-clear / header-return first if applicable)

import (
	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/ratelimit"
	"github.com/icehunter/conduit/internal/sessionstats"
)

// ──────────────────────────────────────────────────────────────────────────────
// Types
// ──────────────────────────────────────────────────────────────────────────────

type settingsPanelTab int

const (
	settingsTabStatus settingsPanelTab = iota
	settingsTabConfig
	settingsTabStats
	settingsTabUsage
	settingsTabProviders
	settingsTabAccounts
)

var settingsTabNames = []string{"Status", "Config", "Stats", "Usage", "Providers", "Accounts"}

type statsDateRange int

const (
	statsRangeAllTime statsDateRange = iota
	statsRangeLast7
	statsRangeLast30
)

var statsDateRangeLabels = []string{"All time", "Last 7 days", "Last 30 days"}

type statsSubTab int

const (
	statsSubOverview statsSubTab = iota
	statsSubModels
)

var statsSubTabNames = []string{"Overview", "Models"}

// configFocus: header row vs list content.
type configFocus int

const (
	configFocusHeader configFocus = iota
	configFocusList
)

type settingItem struct {
	id         string
	label      string
	kind       string // "bool" | "enum" | "info"
	value      string
	options    []string // display names, parallel to optionVals
	optionVals []string // stored values (if different from display)
	on         bool
}

// settingsStatsMsg carries async-loaded stats back to Bubble Tea.
type settingsStatsMsg struct {
	stats sessionStats
	days  int
}

type settingsPanelState struct {
	tab      settingsPanelTab
	selected int

	search      string
	cfgFocus    configFocus
	configItems []settingItem
	filteredIdx []int

	statsSubTab statsSubTab
	statsRange  statsDateRange
	statsData   sessionStats
	statsLoaded bool

	// dirtyIDs tracks config items the user changed this session.
	// rebuildConfigItemsFromSnap does not overwrite these.
	dirtyIDs map[string]bool

	rateLimitInfo ratelimit.Info

	getStatus  func() statusSnapshot
	getMCPInfo func() []mcpInfoRow
	saveConfig func(id string, value interface{})
	gate       *permissions.Gate
	mcpManager *mcp.Manager
	sessPath   string
	cwd        string

	// accounts tab state (embedded from former standalone account panel)
	accts *accountPanelState

	providerSel       int
	providerDetailKey string
	providerAction    int
	providerForm      *providerFormState
}

type statusSnapshot struct {
	version       string
	sessionID     string
	model         string
	fastMode      bool
	effort        string
	permMode      string
	inputTokens   int
	outputTokens  int
	cacheReadTok  int
	cacheWriteTok int
	costUSD       float64
	apiDurSec     float64
	wallDurSec    float64
	linesAdded    int
	linesRemoved  int
	rateLimitWarn string
	authenticated bool // true when OAuth token is loaded
}

type mcpInfoRow struct {
	name   string
	status string
	tools  int
}

// ──────────────────────────────────────────────────────────────────────────────
// Constructor
// ──────────────────────────────────────────────────────────────────────────────

func newSettingsPanel(
	defaultTab settingsPanelTab,
	getStatus func() statusSnapshot,
	getMCPInfo func() []mcpInfoRow,
	saveConfig func(id string, value interface{}),
	gate *permissions.Gate,
	mcpManager *mcp.Manager,
	sessPath string,
	rlInfo ratelimit.Info,
	cwd string,
) (*settingsPanelState, tea.Cmd) {
	p := &settingsPanelState{
		tab:           defaultTab,
		cfgFocus:      configFocusHeader,
		getStatus:     getStatus,
		getMCPInfo:    getMCPInfo,
		saveConfig:    saveConfig,
		gate:          gate,
		mcpManager:    mcpManager,
		sessPath:      sessPath,
		rateLimitInfo: rlInfo,
		cwd:           cwd,
		dirtyIDs:      map[string]bool{},
	}
	p.rebuildConfigItems()
	p.applyFilter()
	cmd := func() tea.Msg {
		return settingsStatsMsg{stats: sessionstats.LoadAll(0), days: 0}
	}
	return p, cmd
}

func loadStatsCmd(days int) tea.Cmd {
	return func() tea.Msg {
		return settingsStatsMsg{stats: sessionstats.LoadAll(days), days: days}
	}
}
