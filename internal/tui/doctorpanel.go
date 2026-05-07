package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type doctorPanelState struct {
	checks   []string // pre-rendered check lines ("✅ Auth", "❌ ripgrep  (hint)")
	platform string   // binary path + OS/arch
}

func (m Model) handleDoctorPanelKey(msg tea.KeyPressMsg) (Model, tea.Cmd) { //nolint:unparam
	switch msg.String() {
	case "esc", "ctrl+c":
		m.doctorPanel = nil
		m.refreshViewport()
	}
	return m, nil
}

func (m Model) renderDoctorPanel() string {
	if m.doctorPanel == nil {
		return ""
	}
	dp := m.doctorPanel
	innerW := m.width - 8
	var sb strings.Builder
	sb.WriteString(panelHeader("Conduit Diagnostics", innerW) + "\n\n")
	if dp.platform != "" {
		sb.WriteString(stylePickerDesc.Render(dp.platform) + "\n\n")
	}
	for _, line := range dp.checks {
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\n" + stylePickerDesc.Render("q / Esc  close"))
	return panelFrameStyle(m.width, lipgloss.Height(sb.String())+4).Render(sb.String())
}
