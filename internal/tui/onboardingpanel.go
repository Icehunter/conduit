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
		_ = settings.SaveConduitOnboardingComplete(true)
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
	panelW := m.width - 8
	if panelW > 92 {
		panelW = 92
	}
	if panelW < 48 {
		panelW = max(24, m.width)
	}
	innerW := panelW - 4
	wrapW := innerW
	if wrapW > 76 {
		wrapW = 76
	}

	var sb strings.Builder
	title := panelTitle("Welcome to conduit")
	ornW := innerW - lipgloss.Width(title) - 4
	if ornW < 8 {
		ornW = 8
	}
	sb.WriteString(title + surfaceSpaces(2) + ornamentGradientText(renderSlashFill(ornW)) + surfaceSpaces(2) + "\n\n")

	intro := "Conduit is a local-first coding agent for the terminal: Claude-compatible at the core, provider-aware where it matters, and built as a single Go binary. Use Claude subscriptions or API keys, OpenAI-compatible providers like Gemini, local MCP-backed models, plugins, and MCP servers from one TUI."
	sb.WriteString(stylePickerDesc.Width(wrapW).Render(intro) + "\n\n")

	if o.authenticated {
		who := o.userName
		if who == "" {
			who = "your account"
		}
		sb.WriteString(stylePickerItem.Render("Signed in as ") + styleStatusAccent.Render(who) + "\n\n")
	} else {
		sb.WriteString(stylePickerItem.Render("Not signed in") + " - run " + styleStatusAccent.Render("/login") + " when ready.\n\n")
	}

	sb.WriteString(stylePickerItem.Render("Start here") + "\n")
	writeOnboardingCommand(&sb, "/models", "assign providers and models by role")
	writeOnboardingCommand(&sb, "/config", "manage accounts, providers, usage, and settings")
	writeOnboardingCommand(&sb, "/plugin", "install skills, commands, and MCP-backed plugins")
	writeOnboardingCommand(&sb, "/mcp", "inspect local and remote MCP servers")
	writeOnboardingCommand(&sb, "/resume", "continue previous Conduit or imported Claude sessions")
	writeOnboardingCommand(&sb, "/doctor", "diagnose auth, MCP, plugins, and config")
	sb.WriteByte('\n')

	sb.WriteString(stylePickerDesc.Render("Press Enter to continue · Ctrl+C to exit"))

	return panelFrameStyle(panelW, renderedLineCount(sb.String())+4).Render(sb.String())
}

func writeOnboardingCommand(sb *strings.Builder, command, desc string) {
	sb.WriteString("  " + styleStatusAccent.Render(command))
	gap := 11 - lipgloss.Width(command)
	if gap < 2 {
		gap = 2
	}
	sb.WriteString(surfaceSpaces(gap) + stylePickerDesc.Render(desc) + "\n")
}
