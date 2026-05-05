package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// permissionPromptState holds the active permission prompt data.
type permissionPromptState struct {
	toolName  string
	toolInput string
	reply     chan<- permissionReply
	selected  int // 0=Allow once, 1=Always allow, 2=Deny
}

var permissionOptions = []string{"Allow once", "Always allow", "Deny"}

// handlePermissionKey routes keys to the permission modal.
func (m Model) handlePermissionKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.permPrompt
	switch msg.String() {
	case "up", "left", "shift+tab":
		if p.selected > 0 {
			p.selected--
		}
	case "down", "right", "tab":
		if p.selected < len(permissionOptions)-1 {
			p.selected++
		}
	case "enter", "space":
		reply := permissionReply{
			allow:       p.selected != 2,
			alwaysAllow: p.selected == 1,
		}
		m.permPrompt = nil
		m.refreshViewport()
		prog := *m.cfg.Program
		return m, func() tea.Msg {
			p.reply <- reply
			_ = prog
			return nil
		}
	case "ctrl+c", "esc":
		// Treat escape as Deny.
		reply := permissionReply{allow: false}
		m.permPrompt = nil
		m.refreshViewport()
		return m, func() tea.Msg {
			p.reply <- reply
			return nil
		}
	case "1":
		p.selected = 0
		reply := permissionReply{allow: true, alwaysAllow: false}
		m.permPrompt = nil
		m.refreshViewport()
		return m, func() tea.Msg { p.reply <- reply; return nil }
	case "2":
		p.selected = 1
		reply := permissionReply{allow: true, alwaysAllow: true}
		m.permPrompt = nil
		m.refreshViewport()
		return m, func() tea.Msg { p.reply <- reply; return nil }
	case "3":
		reply := permissionReply{allow: false}
		m.permPrompt = nil
		m.refreshViewport()
		return m, func() tea.Msg { p.reply <- reply; return nil }
	}
	m.permPrompt = p
	return m, nil
}

// renderPermissionPrompt renders the permission modal overlay.
func (m Model) renderPermissionPrompt() string {
	p := m.permPrompt
	if p == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render("Permission required") + "\n\n")

	// Tool + input.
	label := p.toolName
	if p.toolInput != "" {
		short := p.toolInput
		maxLen := m.width - 20
		if maxLen > 10 && len(short) > maxLen {
			short = short[:maxLen] + "…"
		}
		label += "(" + short + ")"
	}
	sb.WriteString(stylePickerItem.Render(label) + "\n\n")

	for i, opt := range permissionOptions {
		prefix := "  "
		var rendered string
		if i == p.selected {
			rendered = stylePickerItemSelected.Render("❯ " + opt)
		} else {
			rendered = stylePickerItem.Render("  " + opt)
		}
		sb.WriteString(prefix + rendered + "\n")
	}
	sb.WriteString("\n" + stylePickerDesc.Render("↑↓ navigate · Enter select · 1/2/3 quick pick"))

	style := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		PaddingLeft(2).PaddingRight(2).PaddingTop(1).PaddingBottom(1)
	return style.Width(m.width).Render(sb.String())
}
