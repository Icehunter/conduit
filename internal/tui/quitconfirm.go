package tui

import (
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// quitConfirmState drives the "Are you sure you want to quit?" overlay shown
// when the user attempts to quit conduit from an idle state. The default
// selection is "Nope" so an accidental Enter does not exit the session.
type quitConfirmState struct {
	selected int // 0 = Yep!, 1 = Nope (default)
}

// handleQuitConfirmKey routes keyboard input while the quit-confirm dialog
// is active.
func (m Model) handleQuitConfirmKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	q := m.quitConfirm
	switch msg.String() {
	case "left", "h", "shift+tab":
		q.selected = 0
	case "right", "l", "tab":
		q.selected = 1
	case "y", "Y":
		m.quitConfirm = nil
		return m, tea.Quit
	case "n", "N", "esc", "q", "ctrl+c":
		m.quitConfirm = nil
		m.refreshViewport()
		return m, nil
	case "enter", "space":
		if q.selected == 0 {
			m.quitConfirm = nil
			return m, tea.Quit
		}
		m.quitConfirm = nil
		m.refreshViewport()
		return m, nil
	}
	m.quitConfirm = q
	return m, nil
}

// renderQuitConfirm renders the quit confirmation overlay.
func (m Model) renderQuitConfirm() string {
	q := m.quitConfirm
	if q == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render("◆ Quit conduit?") + "\n\n")

	bodyW := floatingInnerWidth(m.width, floatingModalSpec) - floatingBodyPadX*6
	bodyW = max(bodyW, 20)
	prompt := wordWrap("Are you sure you want to quit?", bodyW)
	sb.WriteString(stylePickerItem.Render(prompt) + "\n\n")

	sb.WriteString(renderQuitConfirmButtons(q.selected) + "\n\n")
	sb.WriteString(stylePickerDesc.Render("←/→ navigate · Enter select · Y quit · N/Esc cancel"))

	return sb.String()
}

// renderQuitConfirmButtons lays out the Yep!/Nope buttons side-by-side. The
// selected button uses the accent (window-title) background; the inactive
// button uses a dim chrome background. First letters are underlined to
// surface the keyboard accelerators.
func renderQuitConfirmButtons(selected int) string {
	yep := buildQuitButton("Y", "ep!", selected == 0)
	nope := buildQuitButton("N", "ope", selected == 1)
	row := lipgloss.JoinHorizontal(lipgloss.Top, yep, lipgloss.NewStyle().Background(colorWindowBg).Render("  "), nope)
	return lipgloss.NewStyle().Background(colorWindowBg).Render(row)
}

// quitButtonInactiveBg is a slightly-elevated chrome that reads as a button
// against the modal body without competing with the active button.
var quitButtonInactiveBg = lipgloss.Color("#3a3a4a")

func buildQuitButton(accel, rest string, active bool) string {
	var (
		fg, bg color.Color
		bold   bool
	)
	if active {
		fg = colorSelectionFg
		bg = colorWindowTitle
		bold = true
	} else {
		fg = colorFg
		bg = quitButtonInactiveBg
	}
	under := lipgloss.NewStyle().
		Foreground(fg).
		Background(bg).
		Bold(bold).
		Underline(true).
		Render(accel)
	tail := lipgloss.NewStyle().
		Foreground(fg).
		Background(bg).
		Bold(bold).
		Render(rest)
	return lipgloss.NewStyle().Background(bg).Padding(0, 1).Render(under + tail)
}
