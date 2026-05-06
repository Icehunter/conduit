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
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/guptarohit/asciigraph"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/ratelimit"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/theme"
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
	settingsTabAccounts
)

var settingsTabNames = []string{"Status", "Config", "Stats", "Usage", "Accounts"}

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
		return settingsStatsMsg{stats: loadAllStats(0), days: 0}
	}
	return p, cmd
}

func loadStatsCmd(days int) tea.Cmd {
	return func() tea.Msg {
		return settingsStatsMsg{stats: loadAllStats(days), days: days}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Config item catalog
// ──────────────────────────────────────────────────────────────────────────────

func (p *settingsPanelState) rebuildConfigItems() {
	snap := statusSnapshot{}
	if p.getStatus != nil {
		snap = p.getStatus()
	}

	home, _ := os.UserHomeDir()
	data, _ := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))

	getBool := func(key string, def bool) bool {
		if strings.Contains(string(data), `"`+key+`":false`) ||
			strings.Contains(string(data), `"`+key+`": false`) {
			return false
		}
		if strings.Contains(string(data), `"`+key+`":true`) ||
			strings.Contains(string(data), `"`+key+`": true`) {
			return true
		}
		return def
	}
	getStr := func(key, def string) string {
		for _, sep := range []string{`":"`, `": "`} {
			prefix := `"` + key + sep
			if idx := strings.Index(string(data), prefix); idx >= 0 {
				rest := string(data)[idx+len(prefix):]
				if end := strings.IndexByte(rest, '"'); end >= 0 {
					return rest[:end]
				}
			}
		}
		return def
	}

	permMode := snap.permMode
	if permMode == "" {
		permMode = "default"
	}
	effort := snap.effort
	if effort == "" {
		effort = "normal"
	}
	model := snap.model
	if model == "" {
		model = "claude-sonnet-4-6"
	}

	// Resolve display name for permission mode.
	permDisplay := permModeDisplay(permMode)
	outputStyle := getStr("outputStyle", "default")
	outputDisplay := outputStyleDisplay(outputStyle)

	p.configItems = []settingItem{
		{id: "autoCompactEnabled", label: "Auto-compact", kind: "bool",
			on: getBool("autoCompactEnabled", true)},
		{id: "spinnerTipsEnabled", label: "Show tips", kind: "bool",
			on: getBool("spinnerTipsEnabled", true)},
		{id: "prefersReducedMotion", label: "Reduce motion", kind: "bool",
			on: getBool("prefersReducedMotion", false)},
		{id: "thinkingEnabled", label: "Thinking mode", kind: "bool",
			on: getBool("alwaysThinkingEnabled", true)},
		{id: "fileCheckpointingEnabled", label: "Rewind code (checkpoints)", kind: "bool",
			on: getBool("fileCheckpointingEnabled", true)},
		{id: "verbose", label: "Verbose output", kind: "bool",
			on: getBool("verbose", false)},
		{id: "terminalProgressBarEnabled", label: "Terminal progress bar", kind: "bool",
			on: getBool("terminalProgressBarEnabled", true)},
		{id: "showTurnDuration", label: "Show turn duration", kind: "bool",
			on: getBool("showTurnDuration", true)},
		{
			id:         "defaultPermissionMode",
			label:      "Default permission mode",
			kind:       "enum",
			value:      permDisplay,
			options:    []string{"Default", "Plan Mode", "Accept Edits", "Auto Mode", "Don't Ask"},
			optionVals: []string{"default", "plan", "acceptEdits", "auto", "bypassPermissions"},
		},
		{id: "respectGitignore", label: "Respect .gitignore in file picker", kind: "bool",
			on: getBool("respectGitignore", true)},
		{id: "copyFullResponse", label: "Skip the /copy picker", kind: "bool",
			on: getBool("copyFullResponse", false)},
		{
			id:      "autoUpdatesChannel",
			label:   "Auto-update channel",
			kind:    "enum",
			value:   getStr("autoUpdatesChannel", "latest"),
			options: []string{"latest", "beta", "disabled"},
		},
		func() settingItem {
			// theme.AvailableThemes() includes any user-defined themes loaded
			// from settings.json's "themes" map (registered at startup via
			// theme.SetUserThemes), followed by all six built-in CC palettes.
			// We list everything so users who share settings.json with Claude
			// Code don't have conduit silently rewrite their CC theme pref.
			names := theme.AvailableThemes()
			return settingItem{
				id:    "theme",
				label: "Theme",
				kind:  "enum",
				value: func() string {
					v := getStr("theme", "dark")
					if v == "" {
						return "dark"
					}
					return v
				}(),
				options:    names,
				optionVals: names,
			}
		}(),
		{
			id:         "notifChannel",
			label:      "Local notifications",
			kind:       "enum",
			value:      getStr("preferredNotifChannel", "auto"),
			options:    []string{"auto", "iterm2", "terminal_bell", "disabled"},
			optionVals: []string{"auto", "iterm2", "terminal_bell", "notifications_disabled"},
		},
		{id: "agentPushNotifEnabled", label: "Push when Claude decides", kind: "bool",
			on: getBool("agentPushNotifEnabled", false)},
		{
			id:    "outputStyle",
			label: "Output style",
			kind:  "enum",
			value: outputDisplay,
			// Stored values match CC's built-in names exactly: "default"
			// is lowercase, "Explanatory" and "Learning" are capitalized.
			// Mismatched casing here would make the panel display "Default"
			// no matter what was actually selected.
			options:    []string{"Default", "Explanatory", "Learning"},
			optionVals: []string{"default", "Explanatory", "Learning"},
		},
		{
			id:         "model",
			label:      "Model",
			kind:       "enum",
			value:      modelDisplayName(model),
			options:    []string{"Default (recommended)", "Opus", "Haiku"},
			optionVals: []string{"claude-sonnet-4-6", "claude-opus-4-7", "claude-haiku-4-5-20251001"},
		},
		{
			id:      "effort",
			label:   "Thinking effort",
			kind:    "enum",
			value:   effort,
			options: []string{"low", "normal", "high", "max"},
		},
	}
}

func permModeDisplay(val string) string {
	switch val {
	case "plan":
		return "Plan Mode"
	case "acceptEdits":
		return "Accept Edits"
	case "auto":
		return "Auto Mode"
	case "bypassPermissions":
		return "Don't Ask"
	default:
		return "Default"
	}
}

// permModeStoredVal converts a display name back to the stored value.
// Called from model.go's saveConfigFn.
func permModeStoredVal(display string) string {
	switch display {
	case "Plan Mode":
		return "plan"
	case "Accept Edits":
		return "acceptEdits"
	case "Auto Mode":
		return "auto"
	case "Don't Ask":
		return "bypassPermissions"
	default:
		return "default"
	}
}

// outputStyleStoredVal converts a display name back to the stored value.
// Built-in style names are case-sensitive — "Explanatory" and "Learning"
// stay capitalized; only "default" is lowercase.
func outputStyleStoredVal(display string) string {
	switch display {
	case "Explanatory":
		return "Explanatory"
	case "Learning":
		return "Learning"
	default:
		return "default"
	}
}

func outputStyleDisplay(val string) string {
	switch val {
	case "Explanatory", "explanatory":
		return "Explanatory"
	case "Learning", "learning":
		return "Learning"
	default:
		return "Default"
	}
}

func modelDisplayName(modelID string) string {
	switch {
	case strings.Contains(modelID, "opus"):
		return "Opus"
	case strings.Contains(modelID, "haiku"):
		return "Haiku"
	default:
		return "Default (recommended)"
	}
}

func (p *settingsPanelState) applyFilter() {
	q := strings.ToLower(p.search)
	p.filteredIdx = nil
	for i, item := range p.configItems {
		if q == "" || strings.Contains(strings.ToLower(item.label), q) {
			p.filteredIdx = append(p.filteredIdx, i)
		}
	}
	if p.selected >= len(p.filteredIdx) {
		p.selected = max(0, len(p.filteredIdx)-1)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ──────────────────────────────────────────────────────────────────────────────
// Keyboard handler
// ──────────────────────────────────────────────────────────────────────────────

func (m Model) handleSettingsPanelKey(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	p := m.settingsPanel
	if p == nil {
		return m, nil, false
	}

	key := msg.String()

	done := func() (Model, tea.Cmd, bool) {
		m.settingsPanel = p
		m.refreshViewport()
		return m, nil, true
	}
	doneCmd := func(cmd tea.Cmd) (Model, tea.Cmd, bool) {
		m.settingsPanel = p
		m.refreshViewport()
		return m, cmd, true
	}
	closePanel := func() (Model, tea.Cmd, bool) {
		m.settingsPanel = nil
		m.refreshViewport()
		return m, nil, true
	}

	switchMainTab := func(delta int) {
		p.tab = settingsPanelTab((int(p.tab) + len(settingsTabNames) + delta) % len(settingsTabNames))
		p.cfgFocus = configFocusHeader
		p.search = ""
		p.selected = 0
		p.applyFilter()
	}

	// Accounts tab: delegate to embedded account panel key handler.
	// Esc inside detail view goes back to list; esc at list level closes panel.
	if p.tab == settingsTabAccounts {
		if p.accts == nil {
			p.accts = newAccountPanel()
		}
		switch key {
		case "ctrl+c":
			return closePanel()
		case "esc":
			if p.accts.view == accountViewDetail {
				p.accts.view = accountViewList
				return done()
			}
			return closePanel()
		case "left":
			switchMainTab(-1)
			return done()
		case "right":
			switchMainTab(1)
			return done()
		default:
			m.settingsPanel = p
			m2, cmd := m.handleAccountsTabKey(key)
			if m2.settingsPanel != nil {
				m2.settingsPanel = p // p was mutated in place; keep it
			}
			m2.refreshViewport()
			return m2, cmd, true
		}
	}

	// Global close.
	switch key {
	case "ctrl+c":
		return closePanel()
	case "esc":
		if p.tab == settingsTabConfig && p.search != "" {
			p.search = ""
			p.applyFilter()
			return done()
		}
		if p.tab == settingsTabConfig && p.cfgFocus == configFocusList {
			p.cfgFocus = configFocusHeader
			return done()
		}
		return closePanel()
	}

	// left/right: main tab switch from header; subtab/enum cycle from content.
	switch key {
	case "left":
		switch p.tab {
		case settingsTabStats:
			if p.cfgFocus == configFocusList {
				p.statsSubTab = statsSubTab((int(p.statsSubTab) + len(statsSubTabNames) - 1) % len(statsSubTabNames))
			} else {
				switchMainTab(-1)
			}
		case settingsTabConfig:
			if p.cfgFocus == configFocusList {
				p.cycleEnum(-1)
			} else {
				switchMainTab(-1)
			}
		default:
			switchMainTab(-1)
		}
		return done()

	case "right":
		switch p.tab {
		case settingsTabStats:
			if p.cfgFocus == configFocusList {
				p.statsSubTab = statsSubTab((int(p.statsSubTab) + 1) % len(statsSubTabNames))
			} else {
				switchMainTab(1)
			}
		case settingsTabConfig:
			if p.cfgFocus == configFocusList {
				p.cycleEnum(1)
			} else {
				switchMainTab(1)
			}
		default:
			switchMainTab(1)
		}
		return done()
	}

	// Per-tab handling.
	switch p.tab {
	case settingsTabStatus:
		// read-only

	case settingsTabConfig:
		switch p.cfgFocus {
		case configFocusHeader:
			switch key {
			case "down", "tab", "enter":
				p.cfgFocus = configFocusList
				p.selected = 0
			}
		case configFocusList:
			switch key {
			case "up":
				if p.selected > 0 {
					p.selected--
				} else {
					p.cfgFocus = configFocusHeader
				}
			case "down":
				if p.selected < len(p.filteredIdx)-1 {
					p.selected++
				}
			case "enter", "space":
				p.toggleSelected()
			case "backspace":
				if len(p.search) > 0 {
					p.search = p.search[:len(p.search)-1]
					p.applyFilter()
				}
			default:
				if len(key) == 1 && key >= " " {
					p.search += key
					p.applyFilter()
					p.selected = 0
				}
			}
		}

	case settingsTabStats:
		switch key {
		case "up":
			p.cfgFocus = configFocusHeader
		case "down":
			p.cfgFocus = configFocusList
		case "tab":
			p.statsSubTab = statsSubTab((int(p.statsSubTab) + 1) % len(statsSubTabNames))
		case "r":
			p.statsRange = statsDateRange((int(p.statsRange) + 1) % len(statsDateRangeLabels))
			var days int
			switch p.statsRange {
			case statsRangeLast7:
				days = 7
			case statsRangeLast30:
				days = 30
			}
			return doneCmd(loadStatsCmd(days))
		}

	case settingsTabUsage:
		// read-only
	}

	return done()
}

func (p *settingsPanelState) cycleEnum(dir int) {
	if p.selected >= len(p.filteredIdx) {
		return
	}
	idx := p.filteredIdx[p.selected]
	item := &p.configItems[idx]
	if item.kind != "enum" || len(item.options) == 0 {
		return
	}
	// Find current option index (by display value).
	cur := 0
	for i, o := range item.options {
		if o == item.value {
			cur = i
			break
		}
	}
	cur = (cur + len(item.options) + dir) % len(item.options)
	item.value = item.options[cur]
	p.dirtyIDs[item.id] = true
	// Persist using the stored value if available.
	stored := item.value
	if len(item.optionVals) > cur {
		stored = item.optionVals[cur]
	}
	if p.saveConfig != nil {
		p.saveConfig(item.id, stored)
	}
}

func (p *settingsPanelState) toggleSelected() {
	if p.selected >= len(p.filteredIdx) {
		return
	}
	idx := p.filteredIdx[p.selected]
	item := &p.configItems[idx]
	switch item.kind {
	case "bool":
		item.on = !item.on
		p.dirtyIDs[item.id] = true
		if p.saveConfig != nil {
			p.saveConfig(item.id, item.on)
		}
	case "enum":
		p.cycleEnum(1)
	}
}

func (p *settingsPanelState) rebuildConfigItemsFromSnap(snap statusSnapshot) {
	for i := range p.configItems {
		id := p.configItems[i].id
		if p.dirtyIDs[id] {
			continue // user changed this — don't clobber
		}
		switch id {
		case "model":
			if snap.model != "" {
				p.configItems[i].value = modelDisplayName(snap.model)
			}
		case "effort":
			if snap.effort != "" {
				p.configItems[i].value = snap.effort
			}
		case "defaultPermissionMode":
			if snap.permMode != "" {
				p.configItems[i].value = permModeDisplay(snap.permMode)
			}
		}
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Renderer — matches plugin panel style exactly
// ──────────────────────────────────────────────────────────────────────────────

func (m Model) renderSettingsPanel() string {
	p := m.settingsPanel
	if p == nil {
		return ""
	}

	if p.getStatus != nil {
		snap := p.getStatus()
		p.rebuildConfigItemsFromSnap(snap)
		p.applyFilter()
	}

	w := m.width
	if w < 10 {
		w = 10
	}
	panelH := m.panelHeight() - 1
	if panelH < 8 {
		panelH = m.panelHeight()
	}
	// lipgloss v2's Width() is total block width (including border + padding).
	// Outer style: Width(w-2), border 1 each side (2), padding 2 each side (4)
	// → content area = (w-2) - 2 - 4 = w - 8. v1 was w-6 because Width was
	// content-only there.
	innerW := w - 4

	var sb strings.Builder

	// ── Crush-style panel header + tab selector ────────────────────────────
	title := panelTitle("Settings")
	tabs := settingsColorTabs(settingsTabNames, int(p.tab))

	ornW := innerW - lipgloss.Width(title) - lipgloss.Width(tabs) - 4
	ornW = max(min(0, ornW), 12)

	sb.WriteString(title + surfaceSpaces(2) + ornamentGradientText(renderSlashFill(ornW)) + surfaceSpaces(2) + tabs)
	sb.WriteString("\n\n")

	contentH := panelH - 3
	if contentH < 4 {
		contentH = 4
	}

	// ── Tab body ───────────────────────────────────────────────────────────────
	switch p.tab {
	case settingsTabStatus:
		m.renderSettingsStatus(&sb, p, innerW, contentH)
	case settingsTabConfig:
		m.renderSettingsConfig(&sb, p, innerW, contentH)
	case settingsTabStats:
		m.renderSettingsStats(&sb, p, innerW, contentH)
	case settingsTabUsage:
		m.renderSettingsUsage(&sb, p, innerW, contentH)
	case settingsTabAccounts:
		m.renderSettingsAccounts(&sb, p, innerW, contentH)
	}

	return panelFrameStyle(w, panelH).Render(sb.String())
}

func settingsColorTabs(labels []string, active int) string {
	parts := make([]string, 0, len(labels))
	for i, label := range labels {
		if i == active {
			parts = append(parts, styleStatusAccent.Render(label))
		} else {
			parts = append(parts, stylePickerDesc.Render(label))
		}
	}
	return strings.Join(parts, surfaceSpaces(2))
}

// ── Status tab ────────────────────────────────────────────────────────────────

func (m Model) renderSettingsStatus(sb *strings.Builder, p *settingsPanelState, innerW, _ int) {
	snap := statusSnapshot{}
	if p.getStatus != nil {
		snap = p.getStatus()
	}

	bold := fgOnBg(colorFg).Bold(true)
	dim := stylePickerDesc

	row := func(label, value string) {
		sb.WriteString(bold.Render(label+":") + surfaceSpaces(1) + fgOnBg(colorFg).Render(value) + "\n")
	}

	authStatus := dim.Render("not found")
	if snap.authenticated {
		authStatus = "CLAUDE_CODE_OAUTH_TOKEN"
	}
	row("Auth token", authStatus)

	modelName := snap.model
	if modelName == "" {
		modelName = "claude-sonnet-4-6"
	}
	row("Model", modelName+" · "+dim.Render(modelDescription(modelName)))

	sb.WriteByte('\n')
	row("Version", m.cfg.Version)
	row("Session ID", truncateStr(snap.sessionID, 40))

	title := ""
	if p.sessPath != "" {
		title = session.ExtractTitle(p.sessPath)
	}
	if title == "" {
		title = dim.Render("/rename to add a name")
	}
	row("Session name", title)

	cwd, _ := os.Getwd()
	row("cwd", truncateStr(cwd, innerW-8))

	sb.WriteByte('\n')
	if p.getMCPInfo != nil {
		mcpRows := p.getMCPInfo()
		connected := 0
		for _, r := range mcpRows {
			if strings.Contains(r.status, "connected") {
				connected++
			}
		}
		row("MCP servers", fmt.Sprintf("%d connected · /mcp", connected))
	}

	sb.WriteByte('\n')
	var sources []string
	home2, _ := os.UserHomeDir()
	cwd2, _ := os.Getwd()
	for _, f := range []struct{ path, label string }{
		{filepath.Join(home2, ".claude", "settings.json"), "User settings"},
		{filepath.Join(cwd2, ".claude", "settings.json"), "Project settings"},
		{filepath.Join(cwd2, ".claude", "settings.local.json"), "Project local settings"},
	} {
		if _, err := os.Stat(f.path); err == nil {
			sources = append(sources, f.label)
		}
	}
	if len(sources) == 0 {
		sources = []string{"none"}
	}
	row("Settings", strings.Join(sources, ", "))

	sb.WriteByte('\n')
	sb.WriteString(dim.Render("Platform: "+runtime.GOOS+"/"+runtime.GOARCH+" · Go: "+runtime.Version()) + "\n")
}

func modelDescription(model string) string {
	switch {
	case strings.Contains(model, "opus"):
		return "Most capable model"
	case strings.Contains(model, "sonnet"):
		return "Best for everyday tasks"
	case strings.Contains(model, "haiku"):
		return "Fastest and most compact"
	default:
		return "Claude model"
	}
}

// ── Config tab ────────────────────────────────────────────────────────────────

func (m Model) renderSettingsConfig(sb *strings.Builder, p *settingsPanelState, _, contentH int) {
	// Show focus state in header hint.
	if p.cfgFocus == configFocusHeader {
		sb.WriteString(stylePickerDesc.Render("  ↓ to navigate settings") + "\n\n")
	} else if p.search != "" {
		sb.WriteString(styleStatusAccent.Render("  Filter: "+p.search) +
			stylePickerDesc.Render("  Backspace clear · ↑ tabs") + "\n\n")
	} else {
		sb.WriteString(stylePickerDesc.Render("  Type to filter · ↑ to tabs · ←/→ cycle enum · Enter toggle") + "\n\n")
	}

	visible := contentH - 2
	if visible < 3 {
		visible = 3
	}

	start := 0
	if p.cfgFocus == configFocusList && p.selected >= visible {
		start = p.selected - visible + 1
	}

	count := 0
	for i := start; i < len(p.filteredIdx) && count < visible; i++ {
		item := p.configItems[p.filteredIdx[i]]
		isSel := p.cfgFocus == configFocusList && i == p.selected

		cursor := "  "
		if isSel {
			cursor = styleStatusAccent.Render("❯ ")
		}

		var line string
		// Always wrap label/value in explicit fg styles so they render with
		// theme colors instead of inheriting the terminal's default fg
		// (which is light on dark terminals — invisible on light theme).
		labelStyle := fgOnBg(colorFg)
		valueStyle := fgOnBg(colorFg).Bold(true) // values stand out with bold
		switch item.kind {
		case "bool":
			dot := stylePickerDesc.Render("○")
			if item.on {
				dot = styleStatusAccent.Render("●")
			}
			label := labelStyle.Render(item.label)
			if isSel {
				label = styleStatusAccent.Render(item.label)
			}
			line = cursor + dot + surfaceSpaces(1) + label
		case "enum":
			label := labelStyle.Render(item.label)
			if isSel {
				label = styleStatusAccent.Render(item.label)
			}
			var val string
			if isSel {
				val = styleStatusAccent.Render("‹ " + item.value + " ›")
			} else {
				val = valueStyle.Render(item.value)
			}
			line = cursor + label + surfaceSpaces(2) + val
		case "info":
			label := labelStyle.Render(item.label)
			if isSel {
				label = styleStatusAccent.Render(item.label)
			}
			val := valueStyle.Render(item.value)
			line = cursor + label + surfaceSpaces(2) + val
		}
		sb.WriteString(line + "\n")
		count++
	}

	remaining := len(p.filteredIdx) - start - count
	if remaining > 0 {
		sb.WriteString(stylePickerDesc.Render(fmt.Sprintf("\n  ↓ %d more below", remaining)))
	}
}

// ── Stats tab ─────────────────────────────────────────────────────────────────

// dailyModelEntry mirrors DailyModelTokens from the cache — one entry per active day.
type dailyModelEntry struct {
	date          string
	tokensByModel map[string]int
}

type sessionStats struct {
	totalSessions    int
	totalMessages    int
	totalInputTok    int
	totalOutputTok   int
	totalCostUSD     float64
	modelUsage       map[string]modelUsageStats
	dailyCounts      map[string]int    // day → message count
	dailyTokens      map[string]int    // day → total tokens (all models)
	dailyModelTokens []dailyModelEntry // ordered by date — for per-model chart
	longestStreak    int
	currentStreak    int
	mostActiveDay    string
	longestSession   time.Duration
	rangeStart       time.Time // earliest date in the loaded range
	totalDaysRange   int       // calendar days from rangeStart to today
}

type modelUsageStats struct {
	inputTokens  int
	outputTokens int
	sessions     int
}

// modelRow is a model name + usage pair used for chart and breakdown rendering.
type modelRow struct {
	name string
	u    modelUsageStats
}

func (m Model) renderSettingsStats(sb *strings.Builder, p *settingsPanelState, innerW, _ int) {
	sb.WriteString(surfaceSpaces(2) + settingsColorTabs(statsSubTabNames, int(p.statsSubTab)) + "\n\n")

	if !p.statsLoaded {
		sb.WriteString(stylePickerDesc.Render("  Loading stats…"))
		return
	}

	stats := p.statsData

	// Date range selector.
	var rangeLabels []string
	for i, label := range statsDateRangeLabels {
		if statsDateRange(i) == p.statsRange {
			rangeLabels = append(rangeLabels, styleStatusAccent.Render(label))
		} else {
			rangeLabels = append(rangeLabels, stylePickerDesc.Render(label))
		}
	}
	sb.WriteString(surfaceSpaces(2) + strings.Join(rangeLabels, stylePickerDesc.Render(" · ")) +
		stylePickerDesc.Render("  (r to cycle)") + "\n\n")

	switch p.statsSubTab {
	case statsSubOverview:
		m.renderStatsOverview(sb, &stats, innerW)
	case statsSubModels:
		m.renderStatsModels(sb, &stats, innerW)
	}
}

func (m Model) renderStatsOverview(sb *strings.Builder, stats *sessionStats, innerW int) {
	if stats.totalSessions == 0 {
		sb.WriteString(stylePickerDesc.Render("  No sessions found."))
		return
	}

	// 7-row GitHub-style heatmap.
	buildHeatmap(sb, stats.dailyCounts, innerW)
	sb.WriteByte('\n')

	dim := stylePickerDesc
	acc := styleStatusAccent.Render

	// Favorite model by output tokens.
	favModel := ""
	favTok := 0
	for model, u := range stats.modelUsage {
		if u.outputTokens > favTok {
			favTok = u.outputTokens
			favModel = model
		}
	}
	totalTok := stats.totalInputTok + stats.totalOutputTok

	// Layout matches screenshot: label left-aligned, value accent, 2 columns per row.
	type col struct{ label, value string }
	rows := [][2]col{
		{{"Favorite model", shortModelName(favModel)}, {"Total tokens", formatNum(totalTok)}},
		{{"Sessions", fmt.Sprintf("%d", stats.totalSessions)}, {"Longest session", formatDur(stats.longestSession)}},
		{{"Active days", activeDaysLabel(stats)}, {"Longest streak", fmt.Sprintf("%d days", stats.longestStreak)}},
		{{"Most active day", stats.mostActiveDay}, {"Current streak", fmt.Sprintf("%d days", stats.currentStreak)}},
	}
	// Fixed column widths: left col = 38 visible chars, right col fills the rest.
	// Using a fixed left-column width ensures right column values align regardless
	// of value length. lipgloss.Width() gives the visible width past ANSI escapes.
	const leftColW = 38
	for _, row := range rows {
		l := row[0]
		r := row[1]
		lPart := dim.Render(fmt.Sprintf("%-16s", l.label+":")) + surfaceSpaces(1) + acc(l.value)
		rPart := dim.Render(fmt.Sprintf("%-18s", r.label+":")) + surfaceSpaces(1) + acc(r.value)
		lVis := lipgloss.Width(lPart)
		pad := leftColW - lVis
		if pad < 2 {
			pad = 2
		}
		sb.WriteString(surfaceSpaces(2) + lPart + surfaceSpaces(pad) + rPart + "\n")
	}

	sb.WriteByte('\n')
	if f := buildFactoid(stats); f != "" {
		sb.WriteString(fgOnBg(colorTool).Render("  "+f) + "\n")
	}
}

func (m Model) renderStatsModels(sb *strings.Builder, stats *sessionStats, innerW int) {
	if len(stats.modelUsage) == 0 {
		sb.WriteString(stylePickerDesc.Render("  No model usage data found."))
		return
	}

	var rows []modelRow
	total := 0
	for k, v := range stats.modelUsage {
		if k == "<synthetic>" {
			continue
		}
		rows = append(rows, modelRow{k, v})
		total += v.inputTokens + v.outputTokens
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].u.inputTokens+rows[i].u.outputTokens > rows[j].u.inputTokens+rows[j].u.outputTokens
	})

	// Model colors — shared by chart legend and 2-column breakdown below.
	modelColors := []color.Color{
		lipgloss.Color("#74C69D"), // green
		lipgloss.Color("#ADB5BD"), // gray
		lipgloss.Color("#FFD166"), // yellow
		lipgloss.Color("#EF476F"), // red
		lipgloss.Color("#118AB2"), // blue
		lipgloss.Color("#9B5DE5"), // purple
	}

	// Tokens per Day chart using asciigraph (top 3 models as separate colored series).
	sb.WriteString(fgOnBg(colorFg).Bold(true).Render("  Tokens per Day") + "\n")
	buildTokensLineChart(sb, stats.dailyModelTokens, rows, modelColors, innerW)
	sb.WriteByte('\n')

	colW := (innerW - 2) / 2
	renderModelEntry := func(idx int, r modelRow) (line1, line2 string) {
		tot := r.u.inputTokens + r.u.outputTokens
		pct := 0
		if total > 0 {
			pct = tot * 100 / total
		}
		color := modelColors[idx%len(modelColors)]
		dot := fgOnBg(color).Render("●")
		name := fgOnBg(colorFg).Bold(true).Render(shortModelName(r.name))
		line1 = dot + surfaceSpaces(1) + name + surfaceSpaces(1) + stylePickerDesc.Render(fmt.Sprintf("(%d%%)", pct))
		line2 = stylePickerDesc.Render(fmt.Sprintf("    In: %s · Out: %s",
			formatNum(r.u.inputTokens), formatNum(r.u.outputTokens)))
		return
	}

	for i := 0; i < len(rows); i += 2 {
		l1, l2 := renderModelEntry(i, rows[i])
		if i+1 < len(rows) {
			r1, r2 := renderModelEntry(i+1, rows[i+1])
			// Pad left column to colW visible chars using spaces.
			l1vis := lipgloss.Width(l1)
			l2vis := lipgloss.Width(l2)
			pad1 := colW - l1vis
			pad2 := colW - l2vis
			if pad1 < 1 {
				pad1 = 1
			}
			if pad2 < 1 {
				pad2 = 1
			}
			sb.WriteString(surfaceSpaces(2) + l1 + surfaceSpaces(pad1) + r1 + "\n")
			sb.WriteString(surfaceSpaces(2) + l2 + surfaceSpaces(pad2) + r2 + "\n")
		} else {
			sb.WriteString(surfaceSpaces(2) + l1 + "\n")
			sb.WriteString(surfaceSpaces(2) + l2 + "\n")
		}
		sb.WriteByte('\n')
	}
	if len(rows) > 4 {
		sb.WriteString(stylePickerDesc.Render(fmt.Sprintf("  ↓ 1–4 of %d models (↑/↓ to scroll)", len(rows))) + "\n")
	}
}

// ── Usage tab ─────────────────────────────────────────────────────────────────

func (m Model) renderSettingsUsage(sb *strings.Builder, p *settingsPanelState, innerW, _ int) {
	snap := statusSnapshot{}
	if p.getStatus != nil {
		snap = p.getStatus()
	}

	bold := fgOnBg(colorFg).Bold(true)
	dim := stylePickerDesc

	sb.WriteString(bold.Render("Session") + "\n\n")

	row := func(label, value string) {
		sb.WriteString(fgOnBg(colorFg).Render(fmt.Sprintf("  %-22s %s", label, value)) + "\n")
	}

	if snap.costUSD <= 0 && snap.inputTokens <= 0 {
		sb.WriteString(dim.Render("  No API calls made this session.") + "\n")
	} else {
		if snap.costUSD > 0 {
			row("Total cost:", fmt.Sprintf("$%.4f", snap.costUSD))
		}
		if snap.apiDurSec > 0 {
			row("API duration:", formatDurSec(snap.apiDurSec))
		}
		if snap.wallDurSec > 0 {
			row("Wall duration:", formatDurSec(snap.wallDurSec))
		}
		if snap.linesAdded > 0 || snap.linesRemoved > 0 {
			row("Code changes:", fmt.Sprintf("+%d / -%d lines", snap.linesAdded, snap.linesRemoved))
		}
		sb.WriteByte('\n')
		if snap.inputTokens > 0 {
			row("Tokens in:", formatNum(snap.inputTokens))
		}
		if snap.outputTokens > 0 {
			row("Tokens out:", formatNum(snap.outputTokens))
		}
		if snap.cacheReadTok > 0 {
			row("Cache read:", formatNum(snap.cacheReadTok))
		}
		if snap.cacheWriteTok > 0 {
			row("Cache write:", formatNum(snap.cacheWriteTok))
		}
		if snap.inputTokens > 0 {
			pct := snap.inputTokens * 100 / 200000
			if pct > 100 {
				pct = 100
			}
			barW := innerW - 28
			if barW < 8 {
				barW = 8
			}
			filled := barW * pct / 100
			bar := styleStatusAccent.Render(strings.Repeat("█", filled)) +
				dim.Render(strings.Repeat("░", barW-filled))
			fmt.Fprintf(sb, "\n  %-22s %s %d%%\n", "Context:", bar, pct)
		}
	}

	rl := p.rateLimitInfo
	if rl.HasData() {
		sb.WriteString("\n" + bold.Render("Rate Limits") + "\n\n")
		if rl.RequestsLimit > 0 {
			pct := 100 - (rl.RequestsRemaining * 100 / rl.RequestsLimit)
			sb.WriteString(renderLimitBar("Requests", pct, rl.RequestsRemaining, rl.RequestsLimit, innerW))
		}
		if rl.TokensLimit > 0 {
			pct := 100 - (rl.TokensRemaining * 100 / rl.TokensLimit)
			sb.WriteString(renderLimitBar("Tokens", pct, rl.TokensRemaining, rl.TokensLimit, innerW))
		}
		if snap.rateLimitWarn != "" {
			sb.WriteString("\n  " + styleModeYellow.Render("⚠ "+snap.rateLimitWarn) + "\n")
		}
	}
}

func renderLimitBar(label string, pctUsed, remaining, limit, innerW int) string {
	barW := innerW - 24
	if barW < 8 {
		barW = 8
	}
	if pctUsed > 100 {
		pctUsed = 100
	}
	filled := barW * pctUsed / 100
	style := styleStatusAccent
	if pctUsed >= 80 {
		style = styleModeYellow
	}
	bar := style.Render(strings.Repeat("█", filled)) +
		stylePickerDesc.Render(strings.Repeat("░", barW-filled))
	return fmt.Sprintf("  %-14s %s %d%%  (%d / %d)\n", label+":", bar, 100-pctUsed, remaining, limit)
}

// ──────────────────────────────────────────────────────────────────────────────
// Stats loading — reads ~/.claude/stats-cache.json (maintained by Claude Code),
// falls back to JSONL scanning only when the cache is absent.
// ──────────────────────────────────────────────────────────────────────────────

// statsCacheFile is the shape of ~/.claude/stats-cache.json written by Claude Code.
type statsCacheFile struct {
	Version          int    `json:"version"`
	LastComputedDate string `json:"lastComputedDate"`
	TotalSessions    int    `json:"totalSessions"`
	TotalMessages    int    `json:"totalMessages"`
	FirstSessionDate string `json:"firstSessionDate"`
	LongestSession   struct {
		Duration int64 `json:"duration"` // milliseconds
	} `json:"longestSession"`
	DailyActivity []struct {
		Date         string `json:"date"`
		MessageCount int    `json:"messageCount"`
		SessionCount int    `json:"sessionCount"`
	} `json:"dailyActivity"`
	DailyModelTokens []struct {
		Date          string         `json:"date"`
		TokensByModel map[string]int `json:"tokensByModel"`
	} `json:"dailyModelTokens"`
	ModelUsage map[string]struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"modelUsage"`
	HourCounts map[string]int `json:"hourCounts"`
}

func loadAllStats(days int) sessionStats {
	home, err := os.UserHomeDir()
	if err != nil {
		return sessionStats{}
	}

	// Try the stats cache first.
	cachePath := filepath.Join(home, ".claude", "stats-cache.json")
	if data, err := os.ReadFile(cachePath); err == nil {
		var cache statsCacheFile
		if json.Unmarshal(data, &cache) == nil && cache.TotalSessions > 0 {
			return statsFromCache(&cache, days)
		}
	}

	// Fallback: scan JSONL files.
	return scanAllJSONL(home, days)
}

// statsFromCache converts a statsCacheFile into sessionStats, optionally filtering
// to the most recent `days` days (0 = all time).
func statsFromCache(cache *statsCacheFile, days int) sessionStats {
	cutoff := time.Time{}
	if days > 0 {
		cutoff = time.Now().AddDate(0, 0, -days)
	}

	out := sessionStats{
		modelUsage:  map[string]modelUsageStats{},
		dailyCounts: map[string]int{},
		dailyTokens: map[string]int{},
	}

	// Pre-sort DailyModelTokens by date for ordered chart series.
	sortedDMT := make([]struct {
		Date          string
		TokensByModel map[string]int
	}, len(cache.DailyModelTokens))
	for i, e := range cache.DailyModelTokens {
		sortedDMT[i].Date = e.Date
		sortedDMT[i].TokensByModel = e.TokensByModel
	}
	sort.Slice(sortedDMT, func(i, j int) bool {
		return sortedDMT[i].Date < sortedDMT[j].Date
	})

	if cache.LongestSession.Duration > 0 {
		out.longestSession = time.Duration(cache.LongestSession.Duration) * time.Millisecond
	}

	if cutoff.IsZero() {
		out.totalSessions = cache.TotalSessions
		out.totalMessages = cache.TotalMessages
	}

	// DailyActivity → dailyCounts + filtered totals.
	for _, da := range cache.DailyActivity {
		t, err := time.Parse("2006-01-02", da.Date)
		if err != nil {
			continue
		}
		if !cutoff.IsZero() && t.Before(cutoff) {
			continue
		}
		out.dailyCounts[da.Date] = da.MessageCount
		if !cutoff.IsZero() {
			out.totalSessions += da.SessionCount
			out.totalMessages += da.MessageCount
		}
	}

	// DailyModelTokens → dailyTokens + per-model filtered combined totals + chart series.
	filteredModelCombined := map[string]int{} // model → combined tok in filtered range
	for _, dmt := range sortedDMT {
		t, err := time.Parse("2006-01-02", dmt.Date)
		if err != nil {
			continue
		}
		if !cutoff.IsZero() && t.Before(cutoff) {
			continue
		}
		dayTotal := 0
		for model, tok := range dmt.TokensByModel {
			dayTotal += tok
			if !cutoff.IsZero() {
				filteredModelCombined[model] += tok
			}
		}
		out.dailyTokens[dmt.Date] = dayTotal
		out.dailyModelTokens = append(out.dailyModelTokens, dailyModelEntry{
			date:          dmt.Date,
			tokensByModel: dmt.TokensByModel,
		})
	}

	// All-time: use modelUsage directly (has input+output split).
	if cutoff.IsZero() {
		for model, u := range cache.ModelUsage {
			out.modelUsage[model] = modelUsageStats{
				inputTokens:  u.InputTokens,
				outputTokens: u.OutputTokens,
			}
			out.totalInputTok += u.InputTokens
			out.totalOutputTok += u.OutputTokens
		}
	} else {
		// Filtered range: dailyModelTokens has combined totals only.
		// Derive input/output split using the all-time ratio from modelUsage.
		for model, combined := range filteredModelCombined {
			var inTok, outTok int
			if u, ok := cache.ModelUsage[model]; ok {
				allTotal := u.InputTokens + u.OutputTokens
				if allTotal > 0 {
					// Apply same in/out ratio as all-time.
					inTok = combined * u.InputTokens / allTotal
					outTok = combined - inTok
				} else {
					outTok = combined
				}
			} else {
				outTok = combined
			}
			out.modelUsage[model] = modelUsageStats{
				inputTokens:  inTok,
				outputTokens: outTok,
			}
			out.totalInputTok += inTok
			out.totalOutputTok += outTok
		}
	}

	// Set rangeStart: earliest date in scope.
	if cutoff.IsZero() {
		// All time: use firstSessionDate from cache.
		if cache.FirstSessionDate != "" {
			if t, err := time.Parse(time.RFC3339Nano, cache.FirstSessionDate); err == nil {
				out.rangeStart = t.UTC().Truncate(24 * time.Hour)
			}
		}
	} else {
		out.rangeStart = cutoff.UTC().Truncate(24 * time.Hour)
	}

	out.longestStreak, out.currentStreak = computeStreaks(out.dailyCounts)
	out.mostActiveDay = mostActiveDay(out.dailyCounts)
	if !out.rangeStart.IsZero() {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		out.totalDaysRange = int(today.Sub(out.rangeStart).Hours()/24) + 1
	}
	return out
}

// scanAllJSONL is the fallback when no stats cache exists.
func scanAllJSONL(home string, days int) sessionStats {
	projectsBase := filepath.Join(home, ".claude", "projects")
	projectDirs, err := os.ReadDir(projectsBase)
	if err != nil {
		return sessionStats{}
	}

	cutoff := time.Time{}
	if days > 0 {
		cutoff = time.Now().AddDate(0, 0, -days)
	}

	out := sessionStats{
		modelUsage:  map[string]modelUsageStats{},
		dailyCounts: map[string]int{},
		dailyTokens: map[string]int{},
	}

	for _, pd := range projectDirs {
		if !pd.IsDir() {
			continue
		}
		dirPath := filepath.Join(projectsBase, pd.Name())
		files, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, e := range files {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			if days > 0 {
				info, err2 := e.Info()
				if err2 != nil || info.ModTime().Before(cutoff) {
					continue
				}
			}
			scanJSONL(filepath.Join(dirPath, e.Name()), &out, cutoff)
		}
	}

	// Set rangeStart for the JSONL fallback.
	if !cutoff.IsZero() {
		out.rangeStart = cutoff.UTC().Truncate(24 * time.Hour)
	} else if len(out.dailyCounts) > 0 {
		var earliest string
		for d := range out.dailyCounts {
			if earliest == "" || d < earliest {
				earliest = d
			}
		}
		if t, err := time.Parse("2006-01-02", earliest); err == nil {
			out.rangeStart = t.UTC()
		}
	}

	out.longestStreak, out.currentStreak = computeStreaks(out.dailyCounts)
	out.mostActiveDay = mostActiveDay(out.dailyCounts)
	if !out.rangeStart.IsZero() {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		out.totalDaysRange = int(today.Sub(out.rangeStart).Hours()/24) + 1
	}
	return out
}

func scanJSONL(path string, out *sessionStats, cutoff time.Time) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	out.totalSessions++
	sessionStart := time.Time{}
	sessionEnd := time.Time{}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var entry struct {
			Type      string          `json:"type"`
			Timestamp string          `json:"timestamp"`
			Ts        int64           `json:"ts"`
			Message   json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		ts := time.Time{}
		if entry.Ts > 0 {
			ts = time.UnixMilli(entry.Ts)
		} else if entry.Timestamp != "" {
			ts, _ = time.Parse(time.RFC3339Nano, entry.Timestamp)
		}

		if !cutoff.IsZero() && !ts.IsZero() && ts.Before(cutoff) {
			continue
		}

		if !ts.IsZero() {
			if sessionStart.IsZero() {
				sessionStart = ts
			}
			sessionEnd = ts
		}

		if entry.Type == "cost" && len(entry.Message) > 0 {
			var cost struct {
				InputTokens  int     `json:"inputTokens"`
				OutputTokens int     `json:"outputTokens"`
				CostUSD      float64 `json:"costUSD"`
			}
			if json.Unmarshal(entry.Message, &cost) == nil {
				out.totalInputTok += cost.InputTokens
				out.totalOutputTok += cost.OutputTokens
				out.totalCostUSD += cost.CostUSD
			}
			continue
		}

		var (
			role   string
			model  string
			inTok  int
			outTok int
		)

		parseMsg := func(raw json.RawMessage) {
			var msg struct {
				Role  string `json:"role"`
				Model string `json:"model"`
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(raw, &msg) == nil {
				if msg.Role != "" {
					role = msg.Role
				}
				if msg.Model != "" {
					model = msg.Model
				}
				inTok = msg.Usage.InputTokens
				outTok = msg.Usage.OutputTokens
			}
		}

		switch entry.Type {
		case "user":
			role = "user"
		case "assistant":
			role = "assistant"
			if len(entry.Message) > 0 {
				parseMsg(entry.Message)
				role = "assistant"
			}
		case "message":
			if len(entry.Message) > 0 {
				parseMsg(entry.Message)
			}
		default:
			continue
		}

		if role != "user" && role != "assistant" {
			continue
		}

		out.totalMessages++
		if !ts.IsZero() {
			out.dailyCounts[ts.Format("2006-01-02")]++
		}

		if role == "assistant" && model != "" && model != "<synthetic>" && (inTok > 0 || outTok > 0) {
			mu := out.modelUsage[model]
			mu.inputTokens += inTok
			mu.outputTokens += outTok
			mu.sessions++
			out.modelUsage[model] = mu
			out.totalInputTok += inTok
			out.totalOutputTok += outTok
			if !ts.IsZero() {
				out.dailyTokens[ts.Format("2006-01-02")] += inTok + outTok
			}
		}
	}

	if !sessionStart.IsZero() && !sessionEnd.IsZero() {
		dur := sessionEnd.Sub(sessionStart)
		if dur > out.longestSession {
			out.longestSession = dur
		}
	}
}

func computeStreaks(dailyCounts map[string]int) (longest, current int) {
	if len(dailyCounts) == 0 {
		return
	}
	var days []string
	for d := range dailyCounts {
		days = append(days, d)
	}
	sort.Strings(days)

	streak := 1
	for i := 1; i < len(days); i++ {
		prev, _ := time.Parse("2006-01-02", days[i-1])
		curr, _ := time.Parse("2006-01-02", days[i])
		if curr.Sub(prev) == 24*time.Hour {
			streak++
		} else {
			if streak > longest {
				longest = streak
			}
			streak = 1
		}
	}
	if streak > longest {
		longest = streak
	}

	// Current streak counting back from today or yesterday.
	for _, startOffset := range []int{0, 1} {
		start := time.Now().AddDate(0, 0, -startOffset).Format("2006-01-02")
		if _, ok := dailyCounts[start]; !ok {
			continue
		}
		cur := 1
		for i := startOffset + 1; ; i++ {
			day := time.Now().AddDate(0, 0, -i).Format("2006-01-02")
			if _, ok := dailyCounts[day]; ok {
				cur++
			} else {
				break
			}
		}
		current = cur
		break
	}
	return longest, current
}

func mostActiveDay(dailyCounts map[string]int) string {
	best, bestCount := "", 0
	for d, c := range dailyCounts {
		if c > bestCount {
			bestCount = c
			best = d
		}
	}
	if best == "" {
		return "—"
	}
	t, err := time.Parse("2006-01-02", best)
	if err != nil {
		return best
	}
	return t.Format("Jan 2")
}

// buildHeatmap writes a 7-row × N-week GitHub-style activity heatmap.
func buildHeatmap(sb *strings.Builder, dailyCounts map[string]int, innerW int) {
	const leftPad = 5 // "Mon  " = 5

	weeks := (innerW - leftPad) / 2
	if weeks < 8 {
		weeks = 8
	}
	if weeks > 26 {
		weeks = 26
	}

	now := time.Now()

	// Start on Sunday so columns are weeks and rows are weekdays.
	todaySunday := int(now.Weekday()) // Sun=0, Mon=1..Sat=6
	startDay := now.AddDate(0, 0, -(weeks*7 - 1 + todaySunday))

	maxCount := 0
	for _, c := range dailyCounts {
		if c > maxCount {
			maxCount = c
		}
	}

	heatColors := []color.Color{
		lipgloss.Color("#123524"),
		lipgloss.Color("#1f6f43"),
		lipgloss.Color("#2ea043"),
		lipgloss.Color("#56d364"),
	}

	emptyChar := "·"
	heatChars := []string{"∘", "●", "◉", "⬤"}

	emptyStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#2a2f36")).
		Background(colorWindowBg)

	heatStyles := make([]lipgloss.Style, len(heatColors))
	for i, c := range heatColors {
		heatStyles[i] = fgOnBg(c)
	}

	levelFor := func(count int) int {
		if count <= 0 || maxCount <= 0 {
			return -1
		}

		// Map 1..maxCount onto 0..len(heatChars)-1.
		level := (count - 1) * len(heatChars) / maxCount
		if level < 0 {
			level = 0
		}
		if level >= len(heatChars) {
			level = len(heatChars) - 1
		}
		return level
	}

	cell := func(count int) string {
		level := levelFor(count)
		if level < 0 {
			return emptyStyle.Render(emptyChar)
		}
		return heatStyles[level].Render(heatChars[level])
	}

	grid := make([][]int, 7)
	for i := range grid {
		grid[i] = make([]int, weeks)
	}

	weekStarts := make([]time.Time, weeks)
	for w := 0; w < weeks; w++ {
		ws := startDay.AddDate(0, 0, w*7)
		weekStarts[w] = ws

		for d := 0; d < 7; d++ {
			day := ws.AddDate(0, 0, d).Format("2006-01-02")
			grid[d][w] = dailyCounts[day]
		}
	}

	renderHeatmapMonths(sb, weekStarts, leftPad)
	renderHeatmapRows(sb, grid, weeks, cell)
	renderHeatmapLegend(sb, leftPad, emptyStyle, emptyChar, heatStyles, heatChars)
}

func renderHeatmapMonths(sb *strings.Builder, weekStarts []time.Time, leftPad int) {
	weeks := len(weekStarts)

	monthRow := make([]byte, weeks*2+4)
	for i := range monthRow {
		monthRow[i] = ' '
	}

	prevMonth := -1
	lastLabelEnd := -1

	for w, ws := range weekStarts {
		m := int(ws.Month())
		pos := w * 2

		if m == prevMonth {
			continue
		}

		// Need enough space to draw "Jan".
		if pos < lastLabelEnd+1 {
			prevMonth = m
			continue
		}

		label := ws.Format("Jan")
		for i, c := range []byte(label) {
			if pos+i < len(monthRow) {
				monthRow[pos+i] = c
			}
		}

		prevMonth = m
		lastLabelEnd = pos + len(label)
	}

	monthStr := strings.TrimRight(string(monthRow), " ")

	sb.WriteString(surfaceSpaces(leftPad))
	sb.WriteString(stylePickerDesc.Render(monthStr))
	sb.WriteByte('\n')
}

func renderHeatmapRows(
	sb *strings.Builder,
	grid [][]int,
	weeks int,
	cell func(count int) string,
) {
	rowLabels := [7]string{"   ", "Mon", "   ", "Wed", "   ", "Fri", "   "}

	for d := 0; d < 7; d++ {
		sb.WriteString(stylePickerDesc.Render(rowLabels[d]))
		sb.WriteString(surfaceSpaces(2))

		for w := 0; w < weeks; w++ {
			sb.WriteString(cell(grid[d][w]))

			if w < weeks-1 {
				sb.WriteString(surfaceSpaces(1))
			}
		}

		sb.WriteByte('\n')
	}
}

func renderHeatmapLegend(
	sb *strings.Builder,
	leftPad int,
	emptyStyle lipgloss.Style,
	emptyChar string,
	heatStyles []lipgloss.Style,
	heatChars []string,
) {
	sb.WriteString(surfaceSpaces(leftPad))
	sb.WriteString(stylePickerDesc.Render("Less  "))
	sb.WriteString(emptyStyle.Render(emptyChar))

	for i := range heatChars {
		sb.WriteString(surfaceSpaces(1))
		sb.WriteString(heatStyles[i].Render(heatChars[i]))
	}

	sb.WriteString(stylePickerDesc.Render("  More"))
	sb.WriteByte('\n')
}

// buildTokensLineChart renders a per-model step-line chart using asciigraph,
// matching Claude Code's "Tokens per Day" chart exactly.
// Top 3 models are drawn as separate colored lines; x-axis labels show dates.
func buildTokensLineChart(sb *strings.Builder, dailyModelTokens []dailyModelEntry, rows []modelRow, modelColors []color.Color, innerW int) {
	if len(dailyModelTokens) < 2 {
		sb.WriteString(stylePickerDesc.Render("  Not enough data for chart.\n"))
		return
	}

	// CC caps chart width at 52, aligned with heatmap. Y-axis label width is 7.
	const yAxisWidth = 7
	availW := innerW - yAxisWidth - 2 // -2 for indent
	chartW := availW
	if chartW > 52 {
		chartW = 52
	}
	if chartW < 10 {
		chartW = 10
	}

	// Distribute data across chartW: if fewer points than width, repeat each;
	// if more, take the most recent chartW entries. Mirrors CC's generateTokenChart.
	var recentData []dailyModelEntry
	if len(dailyModelTokens) >= chartW {
		recentData = dailyModelTokens[len(dailyModelTokens)-chartW:]
	} else {
		repeatCount := chartW / len(dailyModelTokens)
		for _, day := range dailyModelTokens {
			for i := 0; i < repeatCount; i++ {
				recentData = append(recentData, day)
			}
		}
	}

	// Top 3 models only (already sorted by total tokens descending).
	topModels := rows
	if len(topModels) > 3 {
		topModels = topModels[:3]
	}

	// asciigraph color constants matching CC theme (suggestion=green, success=yellow, warning=red).
	agColors := []asciigraph.AnsiColor{asciigraph.Green, asciigraph.Yellow, asciigraph.Red}
	// Lipgloss colors for legend bullets (match the asciigraph ANSI colors visually).
	legendColors := []color.Color{lipgloss.Color("#22C55E"), lipgloss.Color("#EAB308"), lipgloss.Color("#EF4444")}

	var series [][]float64
	var legendParts []string
	for i, r := range topModels {
		data := make([]float64, len(recentData))
		hasData := false
		for j, day := range recentData {
			v := day.tokensByModel[r.name]
			data[j] = float64(v)
			if v > 0 {
				hasData = true
			}
		}
		if !hasData {
			continue
		}
		series = append(series, data)
		color := legendColors[i%len(legendColors)]
		_ = modelColors // modelColors used in the breakdown below
		dot := fgOnBg(color).Render("●")
		legendParts = append(legendParts, dot+" "+stylePickerDesc.Render(shortModelName(r.name)))
	}

	if len(series) == 0 {
		sb.WriteString(stylePickerDesc.Render("  No token data.\n"))
		return
	}

	// Render chart with asciigraph.
	nSeries := len(series)
	if nSeries > len(agColors) {
		nSeries = len(agColors)
		series = series[:nSeries]
	}
	opts := []asciigraph.Option{
		asciigraph.Height(8),
		asciigraph.SeriesColors(agColors[:nSeries]...),
	}
	var chart string
	if len(series) == 1 {
		chart = asciigraph.Plot(series[0], opts...)
	} else {
		chart = asciigraph.PlotMany(series, opts...)
	}

	// Indent each chart line by 2 spaces.
	indent := "  "
	for _, line := range strings.Split(chart, "\n") {
		sb.WriteString(indent + line + "\n")
	}

	// X-axis date labels.
	xLabels := generateChartXLabels(recentData, yAxisWidth)
	sb.WriteString(indent + xLabels + "\n")

	// Legend: "● Sonnet 4.6 · ● Opus 4.7"
	if len(legendParts) > 0 {
		sb.WriteString(indent + strings.Join(legendParts, stylePickerDesc.Render(" · ")) + "\n")
	}
}

// generateChartXLabels produces evenly-spaced date labels for the chart x-axis.
func generateChartXLabels(data []dailyModelEntry, yAxisOffset int) string {
	if len(data) == 0 {
		return ""
	}
	numLabels := 4
	if len(data) < 16 {
		numLabels = 2
	}
	usableLength := len(data) - 6 // reserve space for last label
	if usableLength < 1 {
		usableLength = 1
	}
	step := usableLength / (numLabels - 1)
	if step < 1 {
		step = 1
	}

	result := strings.Repeat(" ", yAxisOffset)
	currentPos := 0
	for i := 0; i < numLabels; i++ {
		idx := i * step
		if idx >= len(data) {
			idx = len(data) - 1
		}
		t, err := time.Parse("2006-01-02", data[idx].date)
		if err != nil {
			continue
		}
		label := t.Format("Jan 2")
		spaces := idx - currentPos
		if spaces < 1 {
			spaces = 1
		}
		result += strings.Repeat(" ", spaces) + label
		currentPos = idx + len(label)
	}
	return result
}

// literaryTokenCounts maps famous books to approximate word/token counts.
// Claude Code uses these for the "~Nx more tokens than <book>" factoid.
var literaryTokenCounts = []struct {
	title  string
	tokens int
}{
	{"War and Peace", 580_000},
	{"Les Misérables", 530_000},
	{"Don Quixote", 430_000},
	{"Ulysses", 265_000},
	{"Moby Dick", 210_000},
	{"Anna Karenina", 350_000},
	{"The Brothers Karamazov", 360_000},
	{"Crime and Punishment", 211_000},
	{"Great Expectations", 185_000},
	{"Jane Eyre", 183_000},
	{"Hamlet", 30_000},
	{"Slaughterhouse-Five", 49_000},
	{"The Great Gatsby", 47_000},
	{"Of Mice and Men", 30_000},
	{"The Catcher in the Rye", 73_000},
	{"To Kill a Mockingbird", 100_000},
	{"1984", 88_000},
	{"Brave New World", 64_000},
	{"Fahrenheit 451", 46_000},
	{"Lord of the Flies", 59_000},
}

func buildFactoid(stats *sessionStats) string {
	totalTok := stats.totalInputTok + stats.totalOutputTok

	// Literary comparison: find the best-fit book.
	if totalTok > 5_000 {
		bestTitle := ""
		bestMult := 0
		for _, b := range literaryTokenCounts {
			if b.tokens <= 0 {
				continue
			}
			mult := totalTok / b.tokens
			if mult >= 1 && mult > bestMult {
				bestMult = mult
				bestTitle = b.title
			}
		}
		if bestTitle != "" && bestMult >= 2 {
			return fmt.Sprintf("You've used ~%dx more tokens than %s", bestMult, bestTitle)
		}
	}

	switch {
	case stats.currentStreak >= 7:
		return fmt.Sprintf("You're on a %d-day streak! Keep it up!", stats.currentStreak)
	case stats.totalSessions >= 100:
		return fmt.Sprintf("Over %d sessions — you're a power user!", stats.totalSessions)
	case stats.longestStreak >= 5:
		return fmt.Sprintf("Your longest streak was %d days.", stats.longestStreak)
	default:
		return ""
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func activeDaysLabel(stats *sessionStats) string {
	active := len(stats.dailyCounts)
	if stats.totalDaysRange > 0 {
		return fmt.Sprintf("%d/%d", active, stats.totalDaysRange)
	}
	return fmt.Sprintf("%d", active)
}

func truncateStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

func formatNum(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func formatDur(d time.Duration) string {
	if d < time.Minute {
		return "< 1m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

func formatDurSec(sec float64) string {
	return formatDur(time.Duration(sec * float64(time.Second)))
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

var _ = formatDuration
