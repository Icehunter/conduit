package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/subagent"
)

// subagentPanelState holds the state for the two-level sub-agent drill-in panel.
type subagentPanelState struct {
	entryIDs []string
	selected int
	scroll   int
	view     string // "list" | "detail"
}

const subagentPanelMaxVisible = 10

// openSubagentPanel opens the list view of all sub-agents (running + recent).
func (m Model) openSubagentPanel() Model {
	ids := sortedSubagentIDs()
	if len(ids) == 0 {
		return m
	}
	m.subagentPanel = &subagentPanelState{
		entryIDs: ids,
		selected: 0,
		view:     "list",
	}
	return m
}

// sortedSubagentIDs returns sub-agent IDs sorted: active (newest first), then
// completed (most recently done first).
func sortedSubagentIDs() []string {
	all := subagent.Default.SnapshotAll()
	if len(all) == 0 {
		return nil
	}
	var active, done []subagent.Entry
	for _, e := range all {
		if e.IsRunning() {
			active = append(active, e)
		} else {
			done = append(done, e)
		}
	}
	sort.Slice(active, func(i, j int) bool {
		return active[i].StartedAt.After(active[j].StartedAt)
	})
	sort.Slice(done, func(i, j int) bool {
		return done[i].DoneAt.After(done[j].DoneAt)
	})
	sorted := append(active, done...)
	ids := make([]string, len(sorted))
	for i, e := range sorted {
		ids[i] = e.ID
	}
	return ids
}

// handleSubagentPanelKey dispatches to the list or detail key handler.
func (m Model) handleSubagentPanelKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.subagentPanel.view == "detail" {
		return m.handleSubagentDetailKey(msg)
	}
	return m.handleSubagentListKey(msg)
}

// handleSubagentListKey handles keys for the list view.
func (m Model) handleSubagentListKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.subagentPanel
	// Refresh sorted list.
	p.entryIDs = sortedSubagentIDs()
	if len(p.entryIDs) == 0 {
		m.subagentPanel = nil
		return m, nil
	}
	if p.selected >= len(p.entryIDs) {
		p.selected = len(p.entryIDs) - 1
	}

	switch msg.String() {
	case "esc", "ctrl+c", "q":
		m.subagentPanel = nil
		return m, nil
	case "up", "k":
		if p.selected > 0 {
			p.selected--
		}
	case "down", "j":
		if p.selected < len(p.entryIDs)-1 {
			p.selected++
		}
	case "enter":
		p.scroll = 0
		p.view = "detail"
	}
	m.subagentPanel = p
	return m, tickSubagentPanel()
}

// handleSubagentDetailKey handles keys for the detail view.
func (m Model) handleSubagentDetailKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.subagentPanel
	var currentID string
	if p.selected < len(p.entryIDs) {
		currentID = p.entryIDs[p.selected]
	}
	evs := subagent.Default.GetEvents(currentID)

	switch msg.String() {
	case "esc", "ctrl+c", "backspace", "b":
		if msg.String() == "esc" || msg.String() == "ctrl+c" {
			m.subagentPanel = nil
			return m, nil
		}
		// B / Backspace: go back to list.
		p.scroll = 0
		p.view = "list"
	case "up", "k":
		if p.scroll > 0 {
			p.scroll--
		}
	case "down", "j":
		maxScroll := len(evs) - subagentPanelMaxVisible
		if maxScroll < 0 {
			maxScroll = 0
		}
		if p.scroll < maxScroll {
			p.scroll++
		}
	case "g":
		p.scroll = 0
	case "G":
		maxScroll := len(evs) - subagentPanelMaxVisible
		if maxScroll < 0 {
			maxScroll = 0
		}
		p.scroll = maxScroll
	}
	m.subagentPanel = p
	return m, tickSubagentPanel()
}

// renderSubagentList renders the compact list picker (floated above input).
func (m Model) renderSubagentList() string {
	p := m.subagentPanel
	if p == nil || p.view != "list" {
		return ""
	}

	all := subagent.Default.SnapshotAll()
	byID := make(map[string]subagent.Entry, len(all))
	for _, e := range all {
		byID[e.ID] = e
	}

	contentW := floatingInnerWidth(m.width, floatingPickerSpec)
	var sb strings.Builder

	// Header.
	sb.WriteString(panelHeader("Agent Logs", contentW) + "\n\n")

	// List rows — 2 lines each: label row + activity row.
	for i, id := range p.entryIDs {
		e, ok := byID[id]
		if !ok {
			continue
		}
		label := e.Label
		runes := []rune(label)
		maxLabel := contentW - 12 // leave room for badge + cursor
		if len(runes) > maxLabel && maxLabel > 8 {
			label = string(runes[:maxLabel]) + "…"
		}

		// Status icon + label + mode badge.
		var icon, labelStyle string
		if e.IsRunning() {
			icon = styleModeYellow.Render("●")
			labelStyle = styleStatusAccent.Render(label)
		} else {
			icon = stylePickerDesc.Render("○")
			labelStyle = stylePickerItem.Render(label)
		}
		cursor := "  "
		if i == p.selected {
			cursor = stylePickerItemSelected.Render("❯") + " "
			if !e.IsRunning() {
				labelStyle = stylePickerItemSelected.Render(label)
			}
		}
		fmt.Fprintf(&sb, "%s%s %s  %s\n", cursor, icon, labelStyle, modeBadge(e.Mode))

		// Activity line: last tool event summary.
		evs := subagent.Default.GetEvents(id)
		activity := subagentActivitySummary(e, evs)
		fmt.Fprintf(&sb, "    %s\n", stylePickerDesc.Render(activity))
	}

	// Footer hint.
	sb.WriteString("\n" + stylePickerDesc.Render("↑/↓ navigate · Enter open log · Esc close"))
	return sb.String()
}

// renderSubagentDetail renders the full-size detail panel for the selected agent.
func (m Model) renderSubagentDetail() string {
	p := m.subagentPanel
	if p == nil || p.view != "detail" {
		return ""
	}

	all := subagent.Default.SnapshotAll()
	byID := make(map[string]subagent.Entry, len(all))
	for _, e := range all {
		byID[e.ID] = e
	}

	var currentID string
	if p.selected < len(p.entryIDs) {
		currentID = p.entryIDs[p.selected]
	}

	e, isKnown := byID[currentID]
	w := m.width
	if w < 20 {
		w = 20
	}
	innerW := w - 8 // panel padding: border(2) + padding(4) each side = 8 total

	var sb strings.Builder

	// Header: title = agent label, slash fill.
	title := currentID
	if isKnown {
		title = e.Label
	}
	runes := []rune(title)
	if len(runes) > 40 {
		title = string(runes[:40]) + "…"
	}
	statusTag := stylePickerDesc.Render("done")
	if isKnown && e.IsRunning() {
		statusTag = styleModeYellow.Render("● running")
	}
	titleW := innerW - lipgloss.Width(statusTag) - lipgloss.Width(modeBadge(e.Mode)) - 4
	if titleW < 8 {
		titleW = 8
	}
	sb.WriteString(panelHeader("↳ "+title, titleW))
	fmt.Fprintf(&sb, "  %s  %s\n\n", modeBadge(e.Mode), statusTag)

	// Available event rows: panel height - chrome rows.
	chromeRows := 5 // header + blank + footer-blank + footer + bottom padding
	visibleEvents := m.panelH - chromeRows
	if visibleEvents < 3 {
		visibleEvents = 3
	}

	// Event list.
	evs := subagent.Default.GetEvents(currentID)
	if len(evs) == 0 {
		sb.WriteString(stylePickerDesc.Render("  (no tool calls recorded)") + "\n")
	} else {
		start := p.scroll
		end := start + visibleEvents
		if end > len(evs) {
			end = len(evs)
		}
		for _, ev := range evs[start:end] {
			sb.WriteString(renderSubagentToolEvent(ev, innerW))
			sb.WriteByte('\n')
		}
		if len(evs) > visibleEvents {
			shown := end - start
			fmt.Fprintf(&sb, "  %s\n", stylePickerDesc.Render(
				fmt.Sprintf("%d/%d  ↑↓ scroll", shown, len(evs)),
			))
		}
	}

	// Footer hint (1 blank line above per design).
	hint := "B back · ↑↓ scroll · Esc close"
	if isKnown && e.IsRunning() {
		hint = "live · " + hint
	}
	fmt.Fprintf(&sb, "\n%s", stylePickerDesc.Render(hint))

	return panelFrameStyle(w, m.panelH).Render(sb.String())
}

// subagentActivitySummary returns a one-line summary of the agent's last activity.
func subagentActivitySummary(e subagent.Entry, evs []subagent.ToolEvent) string {
	if !e.IsRunning() {
		dur := e.DoneAt.Sub(e.StartedAt).Round(time.Second)
		if len(evs) == 0 {
			return fmt.Sprintf("(no tool calls) · %s", formatMessageDuration(dur))
		}
		last := evs[len(evs)-1]
		return fmt.Sprintf("↳ %s · %d calls · %s", toolDisplayName(last.ToolName), len(evs), formatMessageDuration(dur))
	}
	// Running.
	elapsed := time.Since(e.StartedAt).Round(time.Second)
	if len(evs) == 0 {
		return fmt.Sprintf("waiting… %s", formatMessageDuration(elapsed))
	}
	last := evs[len(evs)-1]
	status := last.Status
	if status == "running" {
		return fmt.Sprintf("↳ %s running… %s", toolDisplayName(last.ToolName), formatMessageDuration(elapsed))
	}
	return fmt.Sprintf("↳ %s · %d calls · %s", toolDisplayName(last.ToolName), len(evs), formatMessageDuration(elapsed))
}

// renderSubagentToolEvent renders one tool event row for the detail panel.
func renderSubagentToolEvent(ev subagent.ToolEvent, width int) string {
	var icon string
	switch ev.Status {
	case "done":
		icon = styleStatusAccent.Render("✓")
	case "failed":
		icon = styleErrorText.Render("✗")
	default:
		icon = styleModeYellow.Render("●")
	}

	name := styleToolBadge.Render(toolDisplayName(ev.ToolName))
	statusText := styleStatus.Render(ev.Status)
	header := icon + " " + name + " " + statusText
	if ev.Duration > 0 {
		header += " " + styleStatus.Render(formatMessageDuration(ev.Duration))
	}
	summary := toolInputSummary(ev.ToolName, ev.ToolInput)
	if summary != "" {
		available := width - lipgloss.Width("  "+header) - lipgloss.Width(" · ")
		if available >= 8 {
			header += styleStatus.Render(" · " + truncate(summary, available))
		}
	}
	if ev.Status == "running" && !ev.StartedAt.IsZero() {
		elapsed := time.Since(ev.StartedAt).Round(time.Second)
		if elapsed >= time.Second {
			header += " " + stylePickerDesc.Render(formatMessageDuration(elapsed))
		}
	}
	return "  " + header
}

// tickSubagentPanel schedules a refresh tick for the sub-agent panel.
func tickSubagentPanel() tea.Cmd {
	return tea.Tick(time.Second, func(_ time.Time) tea.Msg {
		return subagentPanelRefreshMsg{}
	})
}
