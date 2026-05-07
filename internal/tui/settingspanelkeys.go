package tui

// Key dispatch and mutation for the settings panel.

import (
	tea "charm.land/bubbletea/v2"
)

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

	if p.tab == settingsTabProviders {
		switch key {
		case "ctrl+c":
			return closePanel()
		case "esc":
			if p.providerForm != nil {
				p.providerForm = nil
				return done()
			}
			return closePanel()
		case "left":
			if p.providerForm == nil {
				switchMainTab(-1)
			}
			return done()
		case "right":
			if p.providerForm == nil {
				switchMainTab(1)
			}
			return done()
		default:
			return m.handleProvidersTabKey(key)
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
