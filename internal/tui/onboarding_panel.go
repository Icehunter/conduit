package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/settings"
)

// onboardingState is the first-run welcome shown until the user dismisses
// it with Enter. Mirrors the gating from src/components/Onboarding.tsx but
// trimmed to a single screen — conduit doesn't need CC's preflight,
// API-key, or terminal-setup steps inside the wizard (those are handled
// elsewhere or descoped).
type onboardingState struct {
	authenticated bool
	userName      string
}

func (m Model) handleOnboardingKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "esc":
		m.onboarding = nil
		// Persist so the welcome doesn't show on next launch. Best-effort —
		// a failure here just means the user sees the screen again, no data
		// loss, so silent.
		_ = settings.SaveRawKey("onboardingComplete", true)
		m.refreshViewport()
		return m, nil
	case "ctrl+c", "q":
		// Treat as exit so users can bail without persisting.
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) renderOnboarding() string {
	o := m.onboarding
	if o == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render("Welcome to conduit") + "\n\n")
	sb.WriteString("Conduit is a Go-native CLI for the Claude API — a port of the\n")
	sb.WriteString("official Claude Code with the same wire protocol, tool set, and\n")
	sb.WriteString("plugin/MCP system.\n\n")

	if o.authenticated {
		who := o.userName
		if who == "" {
			who = "your account"
		}
		sb.WriteString(stylePickerItem.Render("✓ Signed in as ") + styleStatusAccent.Render(who) + "\n\n")
	} else {
		sb.WriteString(stylePickerItem.Render("✗ Not signed in") + " — run " + styleStatusAccent.Render("/login") + " when ready.\n\n")
	}

	sb.WriteString("Useful commands:\n")
	sb.WriteString("  " + styleStatusAccent.Render("/help") + "    list all slash commands\n")
	sb.WriteString("  " + styleStatusAccent.Render("/login") + "   authenticate with your Anthropic account\n")
	sb.WriteString("  " + styleStatusAccent.Render("/theme") + "   pick a color palette\n")
	sb.WriteString("  " + styleStatusAccent.Render("/doctor") + "  diagnose auth / MCP / settings\n")
	sb.WriteString("  " + styleStatusAccent.Render("/quit") + "    exit\n\n")

	sb.WriteString(stylePickerDesc.Render("Press Enter to continue · Ctrl+C to exit"))

	style := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		PaddingLeft(2).PaddingRight(2).PaddingTop(1).PaddingBottom(1)
	return style.Width(m.width - 4).Render(sb.String())
}
