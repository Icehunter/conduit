package tui

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/session"
)

type searchResult struct {
	filePath string
	title    string
	age      string
	role     string
	snippet  string
}

type searchPanelState struct {
	query    string
	results  []searchResult
	selected int
}

func (m Model) handleSearchPanelKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.searchPanel
	switch msg.String() {
	case "up":
		if p.selected > 0 {
			p.selected--
		}
	case "down":
		if p.selected < len(p.results)-1 {
			p.selected++
		}
	case "enter":
		if len(p.results) == 0 {
			break
		}
		picked := p.results[p.selected]
		m.searchPanel = nil
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
	case "esc", "ctrl+c":
		m.searchPanel = nil
		m.refreshViewport()
	}
	m.searchPanel = p
	return m, nil
}

func (m Model) renderSearchPanel() string {
	if m.searchPanel == nil {
		return ""
	}
	p := m.searchPanel
	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render(fmt.Sprintf("Search: %q", p.query)))
	sb.WriteString("  " + stylePickerDesc.Render(fmt.Sprintf("%d results", len(p.results))) + "\n\n")

	const maxVisible = 10
	start := p.selected - maxVisible/2
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > len(p.results) {
		end = len(p.results)
		start = end - maxVisible
		if start < 0 {
			start = 0
		}
	}

	if len(p.results) == 0 {
		sb.WriteString(stylePickerDesc.Render("  (no results)") + "\n")
	}
	lastTitle := ""
	for vi := start; vi < end; vi++ {
		r := p.results[vi]
		// Print session header when title changes.
		if r.title != lastTitle {
			header := fmt.Sprintf("  ─ %s  %s", r.title, r.age)
			sb.WriteString(stylePickerDesc.Render(header) + "\n")
			lastTitle = r.title
		}
		roleTag := "[" + r.role + "]"
		label := stylePickerDesc.Render(roleTag) + " " + r.snippet
		if vi == p.selected {
			sb.WriteString(stylePickerItemSelected.Render("❯ ") + label + "\n")
		} else {
			sb.WriteString("  " + label + "\n")
		}
	}
	sb.WriteString("\n" + stylePickerDesc.Render("↑/↓ navigate · Enter load session · q/Esc close"))
	return panelFrameStyle(m.width, renderedLineCount(sb.String())+4).Render(sb.String())
}
