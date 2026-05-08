package tui

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/session"
)

// resumeSession is one entry in the /resume picker.
type resumeSession struct {
	filePath string
	age      string
	preview  string // session title / first user message
	msgCount int    // approximate JSONL record count
	size     string // transcript plus sidecar directory footprint
}

// resumePromptState holds the /resume session picker state.
type resumePromptState struct {
	sessions []resumeSession
	filtered []int // indices into sessions matching filter
	filter   string
	selected int
}

// resumeFilter applies the current filter string and resets selection.
func (p *resumePromptState) applyFilter() {
	q := strings.ToLower(p.filter)
	p.filtered = p.filtered[:0]
	for i, s := range p.sessions {
		if q == "" || strings.Contains(strings.ToLower(s.preview), q) ||
			strings.Contains(strings.ToLower(s.age), q) {
			p.filtered = append(p.filtered, i)
		}
	}
	if p.selected >= len(p.filtered) {
		p.selected = 0
	}
}

func (m Model) handleResumeKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.resumePrompt
	switch msg.String() {
	case "up":
		if p.selected > 0 {
			p.selected--
		}
	case "down":
		if p.selected < len(p.filtered)-1 {
			p.selected++
		}
	case "enter":
		if len(p.filtered) == 0 {
			break
		}
		picked := p.sessions[p.filtered[p.selected]]
		m.resumePrompt = nil
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Loading session…"})
		m.refreshViewport()
		filePath := picked.filePath
		return m, func() tea.Msg {
			loadPath := filePath
			if cwd, err := os.Getwd(); err == nil {
				if writeSession, err := session.ImportForWrite(cwd, filePath); err == nil {
					loadPath = writeSession.FilePath
				}
			}
			msgs, err := session.LoadMessages(loadPath)
			return resumeLoadMsg{msgs: msgs, filePath: loadPath, err: err}
		}
	case "space":
		if p.filter != "" {
			p.filter += " "
			p.applyFilter()
		}
	case "esc", "ctrl+c":
		if p.filter != "" {
			// Esc clears filter first, then second Esc cancels.
			p.filter = ""
			p.applyFilter()
			m.resumePrompt = p
			return m, nil
		}
		m.resumePrompt = nil
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Resume cancelled."})
		m.refreshViewport()
		return m, nil
	case "backspace":
		if len(p.filter) > 0 {
			p.filter = p.filter[:len([]rune(p.filter))-1]
			p.applyFilter()
		}
	default:
		// Any printable character updates the search filter.
		if r := []rune(msg.String()); len(r) == 1 && r[0] >= 0x20 {
			p.filter += string(r)
			p.applyFilter()
		}
	}
	m.resumePrompt = p
	return m, nil
}

func (m Model) renderResumePicker() string {
	p := m.resumePrompt
	if p == nil {
		return ""
	}
	contentW := floatingInnerWidth(m.width, floatingPickerSpec)
	var sb strings.Builder
	// Header with slash fill.
	if p.filter != "" {
		countHint := stylePickerDesc.Render(fmt.Sprintf("  (%d/%d)", len(p.filtered), len(p.sessions)))
		titleW := contentW - lipgloss.Width(countHint) - 4
		sb.WriteString(panelHeader(fmt.Sprintf("Resume: %q", p.filter), titleW) + countHint + "\n\n")
	} else {
		sb.WriteString(panelHeader("Resume a previous conversation", contentW) + "\n\n")
	}

	const maxVisible = 12
	start := p.selected - maxVisible/2
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > len(p.filtered) {
		end = len(p.filtered)
		start = end - maxVisible
		if start < 0 {
			start = 0
		}
	}

	if len(p.filtered) == 0 {
		sb.WriteString(stylePickerDesc.Render("  (no matches)") + "\n")
	}
	for vi := start; vi < end; vi++ {
		i := p.filtered[vi]
		s := p.sessions[i]
		label := truncateMiddle(s.age+"  "+s.preview, contentW)
		var line string
		if vi == p.selected {
			line = stylePickerItemSelected.Render("❯ " + label)
		} else {
			line = stylePickerItem.Render("  " + label)
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\n" + stylePickerDesc.Render("↑/↓ navigate · Enter load · Esc clear search · Ctrl+C cancel"))

	// Preview panel: show selected session detail below the list.
	if len(p.filtered) > 0 {
		sel := p.sessions[p.filtered[p.selected]]
		var detail strings.Builder
		detail.WriteString("\n")
		detail.WriteString(ornamentGradientText(strings.Repeat("─", contentW-4)))
		detail.WriteString("\n")
		meta := sel.age
		if sel.msgCount > 0 {
			meta += fmt.Sprintf("  ·  %d records", sel.msgCount)
		}
		if sel.size != "" {
			meta += "  ·  " + sel.size
		}
		detail.WriteString(stylePickerDesc.Render(meta) + "\n")
		// Word-wrap the title to available width.
		titleStyle := lipgloss.NewStyle().Foreground(colorFg)
		detail.WriteString(titleStyle.Render(wordWrap(sel.preview, contentW)))
		sb.WriteString(detail.String())
	}

	return sb.String()
}
