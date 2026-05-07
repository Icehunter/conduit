package tui

import (
	tea "charm.land/bubbletea/v2"
)

// handleKey processes a key event. The bool return indicates whether the key
// was fully consumed (true = skip textarea/viewport propagation).
// handleKey is the top-level key dispatcher. It runs overlay intercepts,
// then the keybinding resolver, then falls through to handleKeyBuiltins.
func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	// Trust dialog is the highest-priority modal — captures all keys.
	if m.trustDialog != nil {
		m2, cmd := m.handleTrustKey(msg)
		return m2, cmd, true
	}
	if m.loginPrompt != nil {
		m2, cmd := m.handleLoginKey(msg)
		return m2, cmd, true
	}
	// Resume picker intercepts all keys when active.
	if m.resumePrompt != nil {
		m2, cmd := m.handleResumeKey(msg)
		return m2, cmd, true
	}
	// Help overlay intercepts all keys when active.
	if m.helpOverlay != nil {
		m2, cmd := m.handleHelpOverlayKey(msg)
		return m2, cmd, true
	}
	// Doctor panel intercepts all keys when active.
	if m.doctorPanel != nil {
		m2, cmd := m.handleDoctorPanelKey(msg)
		return m2, cmd, true
	}
	// Search results panel intercepts all keys when active.
	if m.searchPanel != nil {
		m2, cmd := m.handleSearchPanelKey(msg)
		return m2, cmd, true
	}
	// Plan-approval picker intercepts all keys when active.
	if m.planApproval != nil {
		m2, cmd := m.handlePlanApprovalKey(msg)
		return m2, cmd, true
	}
	// Generic picker (/theme /model /output-style) intercepts keys.
	if m.picker != nil {
		m2, cmd := m.handlePickerKey(msg)
		return m2, cmd, true
	}
	// First-run onboarding intercepts keys until dismissed.
	if m.onboarding != nil {
		m2, cmd := m.handleOnboardingKey(msg)
		return m2, cmd, true
	}
	// Unified panel intercepts all keys when active.
	if m.panel != nil {
		m2, cmd := m.handlePanelKey(msg)
		return m2, cmd, true
	}
	// Plugin panel intercepts all keys when active.
	if m.pluginPanel != nil {
		m2, cmd := m.handlePluginPanelKey(msg)
		return m2, cmd, true
	}
	// Settings panel intercepts all keys when active.
	if m.settingsPanel != nil {
		m2, cmd, consumed := m.handleSettingsPanelKey(msg)
		return m2, cmd, consumed
	}
	// Sub-agent drill-in panel intercepts all keys when active.
	if m.subagentPanel != nil {
		m2, cmd := m.handleSubagentPanelKey(msg)
		return m2, cmd, true
	}
	// Permission prompt intercepts all keys when active.
	if m.permPrompt != nil {
		m2, cmd := m.handlePermissionKey(msg)
		return m2, cmd, true
	}

	// User-customizable keybindings. Checked after overlay handlers so
	// modal overlays always own their own key space. "command:*" actions
	// execute the named slash command directly; other action IDs not yet
	// handled here fall through to the built-in switch below.
	if m.kb != nil {
		contexts := m.activeContexts()
		if res := m.kb.Resolve(msg, contexts...); res.Matched {
			if res.Unbound {
				// Explicit null — swallow key, skip built-ins.
				return m, nil, true
			}
			if m2, cmd, ok := m.dispatchKeybindingAction(res.Action, msg); ok {
				return m2, cmd, true
			}
		}
	}

	return m.handleKeyBuiltins(msg)
}
