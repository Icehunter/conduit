package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/tools/todowritetool"
)

// maxTodoStripRows is the maximum number of terminal rows the todo strip can
// occupy. It consists of: 1 header, 1 rule, up to (maxTodoStripRows-2) tasks.
// Extra tasks are ellipsized into the final task row.
const maxTodoStripRows = 7

// todoStripRows returns how many rows the strip should occupy. Zero means the
// strip is hidden (toggled off or no active todos).
func (m Model) todoStripRows() int {
	if m.todoStripHidden {
		return 0
	}
	todos := todowritetool.GetTodos()
	if len(todos) == 0 {
		return 0
	}
	// header + rule + tasks, capped.
	rows := 2 + len(todos)
	if rows > maxTodoStripRows {
		rows = maxTodoStripRows
	}
	return rows
}

// renderTodoStrip renders the todo strip as a flat block of rows (no border).
// It is drawn between the chat viewport and the working row.
func (m Model) renderTodoStrip() string {
	todos := todowritetool.GetTodos()
	if len(todos) == 0 {
		return ""
	}

	w := m.width
	w = max(w, 10)

	var sb strings.Builder

	// ── Header ───────────────────────────────────────────────────────────────
	counts := todoStatusCounts(todos)
	summary := fmt.Sprintf("[%d ▶  %d ✅  %d ○]", counts.inProgress, counts.done, counts.pending)
	title := styleStatusAccent.Render("◆ Tasks")
	hint := stylePickerDesc.Render("ctrl+t hide")
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

	// ── Rule ─────────────────────────────────────────────────────────────────
	sb.WriteString(panelRule(w) + "\n")

	// ── Task rows ─────────────────────────────────────────────────────────────
	// maxTodoStripRows = header(1) + rule(1) + tasks. We can show at most
	// (maxTodoStripRows - 2) task rows.
	maxTaskRows := maxTodoStripRows - 2
	visible := sortedTodos(todos)
	overflow := 0
	if len(visible) > maxTaskRows {
		visible = visible[:maxTaskRows-1] // leave one row for "+N more…"
		overflow = len(todos) - (maxTaskRows - 1)
	}

	for i, td := range visible {
		line := renderTodoRow(td, w)
		if i < len(visible)-1 || overflow == 0 {
			sb.WriteString(line + "\n")
		} else {
			// Last visible row — either a real task or the overflow placeholder.
			sb.WriteString(line + "\n")
		}
	}
	if overflow > 0 {
		more := stylePickerDesc.Render(fmt.Sprintf("  +%d more…", overflow))
		sb.WriteString(more + "\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}

// renderTodoRow renders one task line, padded/truncated to width w.
func renderTodoRow(td todowritetool.Todo, w int) string {
	icon := todoIcon(td.Status)
	badge := ""
	if td.Status == todowritetool.StatusInProgress {
		badge = "  now"
	}

	// Max content width: icon (2) + space (1) + content + badge
	badgeW := len(badge)
	maxContent := w - 3 - badgeW
	maxContent = max(maxContent, 4)
	content := td.Content
	if len([]rune(content)) > maxContent {
		runes := []rune(content)
		content = string(runes[:maxContent-1]) + "…"
	}

	label := fmt.Sprintf("%s %s", icon, content)
	if badge != "" {
		// Right-align the "now" badge.
		labelW := lipgloss.Width(label)
		pad := w - labelW - badgeW
		pad = max(pad, 1)
		line := label + strings.Repeat(" ", pad) + badge
		return stylePickerItemSelected.Render(line)
	}

	switch td.Status {
	case todowritetool.StatusCompleted:
		return stylePickerDesc.Render(label)
	default:
		return stylePickerItem.Render(label)
	}
}

func todoIcon(status string) string {
	switch status {
	case todowritetool.StatusInProgress:
		return "▶"
	case todowritetool.StatusCompleted:
		return "✅"
	default:
		return "○"
	}
}

type todoCounts struct {
	inProgress int
	done       int
	pending    int
}

// sortedTodos returns a copy of todos sorted by status priority:
// in_progress first, then pending, then completed.
func sortedTodos(todos []todowritetool.Todo) []todowritetool.Todo {
	out := make([]todowritetool.Todo, len(todos))
	copy(out, todos)
	statusOrder := func(s string) int {
		switch s {
		case todowritetool.StatusInProgress:
			return 0
		case todowritetool.StatusPending:
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return statusOrder(out[i].Status) < statusOrder(out[j].Status)
	})
	return out
}

func todoStatusCounts(todos []todowritetool.Todo) todoCounts {
	var c todoCounts
	for _, td := range todos {
		switch td.Status {
		case todowritetool.StatusInProgress:
			c.inProgress++
		case todowritetool.StatusCompleted:
			c.done++
		default:
			c.pending++
		}
	}
	return c
}
