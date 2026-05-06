package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

type helpShortcut struct {
	key  string
	desc string
}

type helpSection struct {
	title     string
	shortcuts []helpShortcut
}

var helpSections = []helpSection{
	{
		title: "Chat",
		shortcuts: []helpShortcut{
			{"Enter", "Send message"},
			{"Shift+Tab", "Cycle permission mode"},
			{"↑/↓", "Input history prev/next"},
			{"Shift+↑/↓", "Scroll chat up/down"},
			{"PgUp / PgDn", "Scroll chat page up/down"},
			{"Tab", "Autocomplete slash command"},
			{"Esc", "Clear attachments / cancel"},
			{"/? (command)", "Show keyboard shortcuts"},
			{"ctrl+o", "Toggle verbose tool output"},
			{"ctrl+v", "Paste image or PDF"},
			{"ctrl+y", "Copy last code block"},
		},
	},
	{
		title: "Global",
		shortcuts: []helpShortcut{
			{"ctrl+c", "Cancel turn / quit when idle"},
			{"ctrl+d", "Quit"},
			{"ctrl+l", "Redraw screen"},
		},
	},
	{
		title: "Select / Picker",
		shortcuts: []helpShortcut{
			{"↑ / k", "Previous item"},
			{"↓ / j", "Next item"},
			{"Enter", "Accept"},
			{"Esc", "Cancel"},
		},
	},
	{
		title: "Confirm",
		shortcuts: []helpShortcut{
			{"y / Enter", "Yes"},
			{"n / Esc", "No"},
		},
	},
}

type helpOverlayState struct {
	vp    viewport.Model
	query string
}

// openHelpOverlay creates the overlay. panelH is m.panelHeight() — the rows
// available above the footer (status bar). The viewport is sized to fill that
// space minus the title row, blank line, scroll hint, and border/padding (7 rows).
func openHelpOverlay(width, panelH int, query string) *helpOverlayState {
	inner := width - 6 // rounded border (1 each side) + padding (2 each side)
	vpH := panelH - 7
	if vpH < 3 {
		vpH = 3
	}
	vp := viewport.New(viewport.WithWidth(inner), viewport.WithHeight(vpH))
	vp.SetContent(buildHelpContent(inner, query))
	return &helpOverlayState{vp: vp, query: strings.TrimSpace(query)}
}

func buildHelpContent(width int, query string) string {
	query = strings.ToLower(strings.TrimSpace(query))
	var sb strings.Builder
	matches := 0
	for _, section := range helpSections {
		keyW := 14
		if width > 40 {
			keyW = 18
		}
		var rows []string
		for _, s := range section.shortcuts {
			haystack := strings.ToLower(section.title + " " + s.key + " " + s.desc)
			if query != "" && !strings.Contains(haystack, query) {
				continue
			}
			rows = append(rows, fmt.Sprintf("  %-*s %s", keyW, s.key, s.desc))
		}
		if len(rows) == 0 {
			continue
		}
		if matches > 0 {
			sb.WriteString("\n")
		}
		fmt.Fprintf(&sb, "%s\n", stylePickerDesc.Render(section.title))
		for _, row := range rows {
			sb.WriteString(row + "\n")
			matches++
		}
	}
	if matches == 0 {
		if query == "" {
			return stylePickerDesc.Render("No shortcuts configured.")
		}
		return stylePickerDesc.Render(fmt.Sprintf("No shortcuts found for %q.", query))
	}
	return sb.String()
}

func (m Model) handleHelpOverlayKey(msg tea.KeyPressMsg) (Model, tea.Cmd) { //nolint:unparam
	if msg.Code == tea.KeyEsc {
		m.helpOverlay = nil
		m.refreshViewport()
		return m, nil
	}
	switch msg.String() {
	case "esc", "escape", "q", "ctrl+c":
		m.helpOverlay = nil
		m.refreshViewport()
		return m, nil
	case "up", "k":
		m.helpOverlay.vp.ScrollUp(1)
	case "down", "j":
		m.helpOverlay.vp.ScrollDown(1)
	case "pgup":
		m.helpOverlay.vp.PageUp()
	case "pgdown":
		m.helpOverlay.vp.PageDown()
	}
	return m, nil
}

func (m Model) renderHelpOverlay() string {
	if m.helpOverlay == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render("Keyboard Shortcuts") + "\n\n")
	if m.helpOverlay.query != "" {
		sb.WriteString(stylePickerDesc.Render("Filter: "+m.helpOverlay.query) + "\n\n")
	}
	sb.WriteString(m.helpOverlay.vp.View())
	sb.WriteString("\n\n" + stylePickerDesc.Render("↑/↓ scroll · q/Esc close"))
	return panelFrameStyle(m.width, renderedLineCount(sb.String())+4).Render(sb.String())
}
