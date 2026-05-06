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
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/ratelimit"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/sessionstats"
	"github.com/icehunter/conduit/internal/settings"
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
		return settingsStatsMsg{stats: sessionstats.LoadAll(0), days: 0}
	}
	return p, cmd
}

func loadStatsCmd(days int) tea.Cmd {
	return func() tea.Msg {
		return settingsStatsMsg{stats: sessionstats.LoadAll(days), days: days}
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

	data, _ := os.ReadFile(settings.ConduitSettingsPath())

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
