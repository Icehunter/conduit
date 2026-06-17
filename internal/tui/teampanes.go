package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/subagent"
)

// maxTeammateStripRows caps the teammate strip height.
// Breakdown: 1 header + 1 rule + up to (maxTeammateStripRows-2) agent rows.
const maxTeammateStripRows = 8

// teammateStripRows returns how many rows the teammate strip occupies.
// Zero when team is not active, strip is hidden, or no teammates exist.
func (m Model) teammateStripRows() int {
	if !m.teamActive || m.teammateStripHidden {
		return 0
	}
	entries := teammateEntries()
	if len(entries) == 0 {
		return 0
	}
	rows := min(2+len(entries), maxTeammateStripRows)
	return rows
}

// renderTeammateStrip renders the teammate strip as a flat block of rows drawn
// between the chat viewport and the todo strip.
func (m Model) renderTeammateStrip() string {
	entries := teammateEntries()
	if len(entries) == 0 {
		return ""
	}

	w := max(m.width, 10)
	var sb strings.Builder

	// Count running vs. done.
	running := 0
	for _, e := range entries {
		if e.IsRunning() {
			running++
		}
	}
	done := len(entries) - running

	// Header line.
	summary := fmt.Sprintf("[▶ %d  ✓ %d]", running, done)
	title := styleStatusAccent.Render("◆ Agents")
	hint := stylePickerDesc.Render("enter view  ctrl+t hide")
	hintW := lipgloss.Width(hint)
	titleW := lipgloss.Width(title)
	summaryW := lipgloss.Width(summary)
	gap := w - titleW - 1 - summaryW - hintW
	gap = max(gap, 1)
	fmt.Fprintf(&sb, "%s %s%s%s\n",
		title,
		stylePickerDesc.Render(summary),
		strings.Repeat(" ", gap),
		hint,
	)

	// Rule.
	sb.WriteString(panelRule(w) + "\n")

	// Agent rows — capped at (maxTeammateStripRows - 2) visible entries.
	maxAgentRows := maxTeammateStripRows - 2
	visible := entries
	overflow := 0
	if len(visible) > maxAgentRows {
		visible = visible[:maxAgentRows-1]
		overflow = len(entries) - (maxAgentRows - 1)
	}

	for _, e := range visible {
		sb.WriteString(renderTeammateRow(e, w) + "\n")
	}
	if overflow > 0 {
		sb.WriteString(stylePickerDesc.Render(fmt.Sprintf("  +%d more…", overflow)) + "\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}

// renderTeammateRow renders one agent row in the teammate strip.
func renderTeammateRow(e subagent.Entry, w int) string {
	icon := "▶"
	if !e.IsRunning() {
		icon = "✓"
	}

	name := e.TeammateFor

	activity := "done"
	if e.IsRunning() {
		evs := subagent.Default.GetEvents(e.ID)
		if len(evs) == 0 {
			activity = "starting…"
		} else {
			last := evs[len(evs)-1]
			if last.Status == "running" {
				activity = last.ToolName + " · " + truncateTeammate(last.ToolInput, 35)
			} else {
				activity = last.ToolName
			}
		}
	}

	prefix := fmt.Sprintf("%s %-18s  ", icon, name)
	line := prefix + activity
	if lipgloss.Width(line) > w {
		avail := max(w-lipgloss.Width(prefix), 4)
		line = prefix + truncateTeammate(activity, avail)
	}

	if e.IsRunning() {
		return stylePickerItemSelected.Render(line)
	}
	return stylePickerDesc.Render(line)
}

// teammateEntries returns all sub-agent tracker entries that belong to a teammate.
func teammateEntries() []subagent.Entry {
	all := subagent.Default.SnapshotAll()
	var out []subagent.Entry
	for _, e := range all {
		if e.TeammateFor != "" {
			out = append(out, e)
		}
	}
	return out
}

// truncateTeammate truncates s to at most maxRunes runes, appending "…" if cut.
func truncateTeammate(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	if maxRunes <= 1 {
		return "…"
	}
	return string(r[:maxRunes-1]) + "…"
}
