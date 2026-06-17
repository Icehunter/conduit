package tui

import (
	"fmt"
	"image"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/subagent"
	"github.com/icehunter/conduit/internal/team"
	"github.com/icehunter/conduit/internal/tools/tasktool"
)

const (
	teamPaneMinW       = 40
	teamPaneMinH       = 5
	teamTaskListMinH   = 2
	teamTaskListMaxH   = 6
	teamRosterInterval = 500 * time.Millisecond
)

// teamPane is one active pane in the agent-teams multi-pane layout.
// Index 0 in teamPaneRects is always the lead (using Model.vp); indices 1..N
// map to teamPanes[0..N-1].
type teamPane struct {
	name    string
	agentID string
	vp      viewport.Model
	status  string // "running" | "idle" | "done"
	content string // stable base content rebuilt by the roster-refresh tick
	stream  string // in-progress text from live EventText events
}

// teammateEventMsg carries one loop event from a named teammate.
// Sent by prog.Send (wired in Phase 9) so the TUI can update the pane live.
type teammateEventMsg struct {
	name  string
	event agent.LoopEvent
}

// teammateStatusMsg updates the status badge for a named teammate pane.
type teammateStatusMsg struct {
	name   string
	status string
}

// teammateRosterRefreshMsg fires every teamRosterInterval when teamActive so
// new teammates are added to the pane list and pane content is refreshed from
// the subagent event log.
type teammateRosterRefreshMsg struct{}

// teamRosterRefreshTick schedules the next roster refresh.
func teamRosterRefreshTick() tea.Cmd {
	return tea.Tick(teamRosterInterval, func(time.Time) tea.Msg {
		return teammateRosterRefreshMsg{}
	})
}

// teamTaskListRows returns the number of terminal rows the team task list strip
// occupies. Returns 0 when hidden, at least teamTaskListMinH when visible.
func (m Model) teamTaskListRows() int {
	if !m.teamTaskListVisible {
		return 0
	}
	rendered := tasktool.RenderTaskList(tasktool.GlobalStore())
	rows := renderedLineCount(rendered)
	rows = max(rows, teamTaskListMinH)
	rows = min(rows, teamTaskListMaxH)
	return rows
}

// computeTeamPaneGrid partitions area into nPanes rectangles.
//
// Strategy (in priority order):
//  1. Horizontal split — each pane width ≥ teamPaneMinW
//  2. Vertical split   — each pane height ≥ teamPaneMinH
//  3. Fallback         — focused pane gets the full area; others collapse to empty
func computeTeamPaneGrid(area image.Rectangle, nPanes int, focus int) []image.Rectangle {
	if nPanes <= 0 {
		return nil
	}
	if nPanes == 1 {
		return []image.Rectangle{area}
	}

	rects := make([]image.Rectangle, nPanes)

	// Horizontal split.
	paneW := area.Dx() / nPanes
	if paneW >= teamPaneMinW {
		x := area.Min.X
		for i := range nPanes {
			var endX int
			if i == nPanes-1 {
				endX = area.Max.X
			} else {
				endX = x + paneW
			}
			rects[i] = image.Rect(x, area.Min.Y, endX, area.Max.Y)
			x = endX
		}
		return rects
	}

	// Vertical split.
	paneH := area.Dy() / nPanes
	if paneH >= teamPaneMinH {
		y := area.Min.Y
		for i := range nPanes {
			var endY int
			if i == nPanes-1 {
				endY = area.Max.Y
			} else {
				endY = y + paneH
			}
			rects[i] = image.Rect(area.Min.X, y, area.Max.X, endY)
			y = endY
		}
		return rects
	}

	// Fallback: only the focused pane gets area; others are zero-sized.
	if focus < 0 || focus >= nPanes {
		focus = 0
	}
	rects[focus] = area
	return rects
}

// applyTeamPaneLayout resizes every pane viewport to match the given layout.
// Called from applyLayout when teamActive.
func (m Model) applyTeamPaneLayout(layout uiLayout) Model {
	rects := layout.teamPaneRects
	if len(rects) == 0 {
		return m
	}
	// Lead pane (rect[0]): resize Model.vp. Subtract 1 row for the title bar.
	if !rects[0].Empty() {
		h := max(rects[0].Dy()-1, 1)
		m.vp.SetWidth(rects[0].Dx())
		m.vp.SetHeight(h)
	}
	// Teammate panes (rect[1..N] → m.teamPanes[0..N-1]).
	for i := range m.teamPanes {
		ri := i + 1
		if ri >= len(rects) {
			break
		}
		r := rects[ri]
		if r.Empty() {
			continue
		}
		h := max(r.Dy()-1, 1)
		m.teamPanes[i].vp.SetWidth(r.Dx())
		m.teamPanes[i].vp.SetHeight(h)
	}
	return m
}

// drawTeamPanes renders the full team pane grid (pane titles + content +
// optional task list strip) onto scr.  Called from Draw when teamActive.
func (m Model) drawTeamPanes(scr uv.Screen, layout uiLayout) {
	// Task list strip at the bottom of the viewport area.
	if m.teamTaskListVisible && !layout.teamTaskList.Empty() {
		drawString(scr, layout.teamTaskList, m.renderTeamTaskListBar(layout.teamTaskList.Dx()))
	}

	nTotal := 1 + len(m.teamPanes)
	for i := range nTotal {
		if i >= len(layout.teamPaneRects) {
			break
		}
		rect := layout.teamPaneRects[i]
		if rect.Empty() {
			continue
		}

		// Title bar — always 1 row tall.
		titleRect := image.Rect(rect.Min.X, rect.Min.Y, rect.Max.X, rect.Min.Y+1)
		drawString(scr, titleRect, m.renderTeamPaneTitle(i, rect.Dx()))

		// Content area — everything below the title bar.
		contentRect := image.Rect(rect.Min.X, rect.Min.Y+1, rect.Max.X, rect.Max.Y)
		if !contentRect.Empty() {
			drawString(scr, contentRect, m.renderTeamPaneContent(i))
		}
	}
}

// renderTeamPaneTitle renders a 1-row title bar for the given pane index.
func (m Model) renderTeamPaneTitle(i int, width int) string {
	var name, status string
	if i == 0 {
		name = "lead"
		if m.running {
			status = "running"
		} else {
			status = "idle"
		}
	} else if i-1 < len(m.teamPanes) {
		p := m.teamPanes[i-1]
		name = p.name
		status = p.status
	}

	focused := i == m.teamFocus
	var prefix, label string
	if focused {
		prefix = styleModeYellow.Render("❯ ")
		label = styleModeYellow.Render(name)
	} else {
		prefix = "  "
		label = stylePickerDesc.Render(name)
	}

	badge := teamStatusBadge(status)
	content := prefix + label + "  " + badge
	contentW := lipgloss.Width(content)
	if contentW < width {
		content += strings.Repeat(" ", width-contentW)
	}
	return content
}

// renderTeamPaneContent returns the viewport view for pane i.
// Pane 0 = lead (uses Model.vp); pane i+1 = m.teamPanes[i].
func (m Model) renderTeamPaneContent(i int) string {
	if i == 0 {
		return m.vp.View()
	}
	if i-1 < len(m.teamPanes) {
		return m.teamPanes[i-1].vp.View()
	}
	return ""
}

// renderTeamTaskListBar renders the team task list strip.
func (m Model) renderTeamTaskListBar(width int) string {
	rendered := tasktool.RenderTaskList(tasktool.GlobalStore())
	header := stylePickerDesc.Width(width).Render("Tasks")
	if rendered == "" || rendered == "No tasks." {
		return header + "\n" + stylePickerDesc.Render("  No tasks")
	}
	return header + "\n" + rendered
}

// teamStatusBadge returns a short styled string for the pane status.
func teamStatusBadge(status string) string {
	switch status {
	case "running":
		return styleModeYellow.Render("[running]")
	case "idle":
		return stylePickerDesc.Render("[idle]")
	case "done":
		return styleModeGreen.Render("[done]")
	default:
		return stylePickerDesc.Render("[" + status + "]")
	}
}

// handleTeammateRosterRefresh syncs m.teamPanes with team.Default and refreshes
// pane content from the subagent event log.
func (m Model) handleTeammateRosterRefresh() (Model, tea.Cmd) {
	if !m.teamActive {
		return m, nil
	}

	// Collect currently registered teammate names (skip "lead").
	names := team.Default.Names()
	existing := make(map[string]int, len(m.teamPanes)) // name → slice index
	for i, p := range m.teamPanes {
		existing[p.name] = i
	}
	for _, n := range names {
		if n == team.ReservedLeadName {
			continue
		}
		if _, ok := existing[n]; !ok {
			m.teamPanes = append(m.teamPanes, teamPane{
				name:   n,
				status: "running",
				vp:     viewport.New(viewport.WithWidth(40), viewport.WithHeight(10)),
			})
			existing[n] = len(m.teamPanes) - 1
		}
	}

	// Map teammate name → agentID via TeammateFor field on subagent entries.
	all := subagent.Default.SnapshotAll()
	agentByTeammate := make(map[string]string, len(all))
	for _, e := range all {
		if e.TeammateFor != "" {
			agentByTeammate[e.TeammateFor] = e.ID
		}
	}

	// Update each pane: resolve agentID, refresh status and viewport content.
	for i := range m.teamPanes {
		p := &m.teamPanes[i]
		if p.agentID == "" {
			if id, ok := agentByTeammate[p.name]; ok {
				p.agentID = id
			}
		}
		if p.agentID == "" {
			continue
		}

		// Update status from subagent entry.
		for _, e := range all {
			if e.ID == p.agentID {
				if e.IsRunning() {
					p.status = "running"
				} else {
					p.status = "done"
				}
				break
			}
		}

		// Rebuild pane viewport from the tool-event log. Clears in-progress
		// stream so the stable summary takes precedence after each 500ms tick.
		evs := subagent.Default.GetEvents(p.agentID)
		if len(evs) > 0 {
			var sb strings.Builder
			for _, ev := range evs {
				fmt.Fprintf(&sb, "%s\n", renderSubagentToolEvent(ev, p.vp.Width()))
			}
			p.content = sb.String()
			p.stream = ""
			p.vp.SetContent(p.content)
			p.vp.GotoBottom()
		}
	}

	return m, teamRosterRefreshTick()
}
