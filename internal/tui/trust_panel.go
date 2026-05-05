package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// trustDialogState drives the workspace trust overlay shown the first time
// conduit opens a directory that hasn't been accepted yet.
// Mirrors decoded/5053.js (the oh3 React component in Claude Code).
//
// Blocks all agent interaction until the user accepts ("Yes, I trust this
// folder") or rejects (tea.Quit — exit code 1 like CC).
type trustDialogState struct {
	selected   int          // 0 = Yes, 1 = No
	setTrusted func() error // persists acceptance to ~/.claude.json
}

// trustAcceptedMsg is returned by the tea.Cmd that persists trust.
type trustAcceptedMsg struct{}

// handleTrustKey routes keyboard input while the trust dialog is active.
func (m Model) handleTrustKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	t := m.trustDialog
	switch msg.String() {
	case "up", "left", "shift+tab":
		if t.selected > 0 {
			t.selected--
		}
	case "down", "right", "tab":
		if t.selected < 1 {
			t.selected++
		}
	case "1", "y":
		return m.acceptTrust(t)
	case "2", "n":
		return m, tea.Quit
	case "enter", "space":
		if t.selected == 0 {
			return m.acceptTrust(t)
		}
		return m, tea.Quit
	case "ctrl+c", "esc", "q":
		return m, tea.Quit
	}
	m.trustDialog = t
	return m, nil
}

func (m Model) acceptTrust(t *trustDialogState) (Model, tea.Cmd) {
	fn := t.setTrusted
	m.trustDialog = nil
	m.refreshViewport()
	return m, func() tea.Msg {
		_ = fn() // best-effort; startup isn't blocked by a write failure
		return trustAcceptedMsg{}
	}
}

// renderTrustDialog renders the trust overlay.
// Mirrors decoded/5053.js lines 120–157.
func (m Model) renderTrustDialog() string {
	t := m.trustDialog
	if t == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render("⚠  Workspace trust required") + "\n\n")

	prompt := wordWrap(
		"Quick safety check: Is this a project you created or one you trust? "+
			"(Like your own code, a well-known open source project, or work from your team.) "+
			"If not, take a moment to review what's in this folder first. "+
			"conduit will be able to read, edit, and execute files here.",
		m.width-14,
	)
	sb.WriteString(stylePickerItem.Render(prompt) + "\n\n")

	options := []string{"Yes, I trust this folder", "No, exit"}
	for i, opt := range options {
		if i == t.selected {
			sb.WriteString(stylePickerItemSelected.Render("  ❯ "+opt) + "\n")
		} else {
			sb.WriteString(stylePickerItem.Render("    "+opt) + "\n")
		}
	}
	sb.WriteString("\n" + stylePickerDesc.Render("↑↓ navigate · Enter select · 1 trust · 2 exit"))

	style := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		PaddingLeft(2).PaddingRight(2).PaddingTop(1).PaddingBottom(1)
	return style.Width(m.width).Render(sb.String())
}
