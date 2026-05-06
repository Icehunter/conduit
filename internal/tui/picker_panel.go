package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// pickerItem is one row in a small selection picker.
// JSON tags let commands construct payloads with json.Marshal directly.
type pickerItem struct {
	Value string `json:"value"` // dispatched as `/<kind> <value>` on Enter
	Label string `json:"label"` // human-readable display
}

// pickerState drives the small select-one overlay used by /theme, /model,
// and /output-style. The picker has no awareness of what each kind does:
// on Enter it dispatches `/<kind> <value>` back through the command
// registry, so the underlying command does the actual work.
type pickerState struct {
	kind     string       // "theme" | "model" | "output-style"
	title    string       // header line
	items    []pickerItem // options (caller-ordered)
	selected int          // current cursor row
	current  string       // value to highlight as "active"
}

func (m Model) handlePickerKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.picker
	switch msg.String() {
	case "up":
		if p.selected > 0 {
			p.selected--
		}
	case "down":
		if p.selected < len(p.items)-1 {
			p.selected++
		}
	case "home", "g":
		p.selected = 0
	case "end", "G":
		p.selected = len(p.items) - 1
	case "enter", "space":
		if p.selected < 0 || p.selected >= len(p.items) {
			return m, nil
		}
		picked := p.items[p.selected].Value
		kind := p.kind
		m.picker = nil
		if m.cfg.Commands == nil {
			return m, nil
		}
		if res, ok := m.cfg.Commands.Dispatch("/" + kind + " " + picked); ok {
			return m.applyCommandResult(res)
		}
		return m, nil
	case "esc", "ctrl+c", "q":
		m.picker = nil
		m.refreshViewport()
		return m, nil
	}
	m.picker = p
	return m, nil
}

func (m Model) renderPicker() string {
	p := m.picker
	if p == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render(p.title) + "\n\n")

	for i, it := range p.items {
		marker := "  "
		if it.Value == p.current {
			marker = "● "
		}
		label := marker + it.Label
		if i == p.selected {
			sb.WriteString(stylePickerItemSelected.Render("❯ "+label) + "\n")
		} else {
			sb.WriteString(stylePickerItem.Render("  "+label) + "\n")
		}
	}
	sb.WriteString("\n" + stylePickerDesc.Render("↑/↓ navigate · Enter select · Escape cancel"))

	return sb.String()
}
