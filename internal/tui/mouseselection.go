package tui

import (
	"image"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type mouseSelectionPoint struct {
	line int
	col  int
}

type mouseSelectionState struct {
	start   mouseSelectionPoint
	end     mouseSelectionPoint
	dragged bool
}

type clearMouseSelectionMsg struct{}

var styleMouseSelection = lipgloss.NewStyle().Reverse(true).Bold(true)

func (m *Model) setViewportContent(content string) {
	m.viewportLines = strings.Split(content, "\n")
	m.applyViewportSelection()
}

func (m *Model) applyViewportSelection() {
	if len(m.viewportLines) == 0 {
		m.vp.SetContent("")
		return
	}
	lines := append([]string(nil), m.viewportLines...)
	if m.mouseSelect != nil {
		start, end := orderedSelection(m.mouseSelect.start, m.mouseSelect.end)
		for line := start.line; line <= end.line && line < len(lines); line++ {
			if line < 0 {
				continue
			}
			from, to := 0, runeCount(ansi.Strip(lines[line]))
			if line == start.line {
				from = start.col
			}
			if line == end.line {
				to = end.col
			}
			lines[line] = highlightPlainLine(ansi.Strip(lines[line]), from, to)
		}
	}
	m.vp.SetContent(strings.Join(lines, "\n"))
}

// handleToolExpansionClick checks if the click is on a tool message and toggles expansion
func (m *Model) handleToolExpansionClick(mouseY int, _ image.Rectangle) bool {
	// Calculate which line in the viewport was clicked
	inputRows := m.input.LineCount()
	inputRows = max(inputRows, 1)
	usageRows := m.usageFooterRows()
	vpStartY := 0 // Viewport starts at top
	vpHeight := m.height - chromeHeight(inputRows, m.height) - usageRows - m.todoStripRows()

	if mouseY < vpStartY || mouseY >= vpStartY+vpHeight {
		return false // Click outside viewport
	}

	// Which line in the viewport content was clicked (accounting for scroll)
	relativeY := mouseY - vpStartY
	lineIndex := m.vp.YOffset() + relativeY

	if lineIndex < 0 || lineIndex >= len(m.viewportLines) {
		return false
	}

	// Check if this line is a tool header or expand/collapse hint
	clickedLine := ansi.Strip(m.viewportLines[lineIndex])
	trimmed := strings.TrimSpace(clickedLine)
	isToolHeader := strings.HasPrefix(trimmed, "├─") ||
		strings.HasPrefix(trimmed, "╰─") ||
		strings.HasPrefix(trimmed, "✓") ||
		strings.HasPrefix(trimmed, "●") ||
		strings.HasPrefix(trimmed, "✗")
	isExpandHint := strings.Contains(trimmed, "[click to expand]") ||
		strings.Contains(trimmed, "[click to collapse]")

	if !isToolHeader && !isExpandHint {
		return false // Not a tool header or expand hint
	}

	// Look up which message this line belongs to
	msgIndex, ok := m.lineToMessage[lineIndex]
	if !ok {
		return false // Line not mapped to a message
	}

	if msgIndex < 0 || msgIndex >= len(m.messages) {
		return false
	}

	msg := &m.messages[msgIndex]

	// Only toggle if it's a tool with expandable content
	const collapsedLines = 10
	if msg.Role == RoleTool && msg.Content != "" && msg.Content != "running…" {
		resultLines := strings.Split(strings.TrimSpace(msg.Content), "\n")
		if len(resultLines) > collapsedLines {
			msg.ToolExpanded = !msg.ToolExpanded
			m.refreshViewport()
			return true
		}
	}

	return false
}

func (m Model) selectionOverlayActive() bool {
	// Note: m.planApproval is intentionally NOT in this list — the plan-approval
	// modal handles its own mouse selection so users can copy plan text directly
	// from the modal. Other overlays still block chat-viewport selection.
	return m.loginPrompt != nil || m.resumePrompt != nil ||
		m.panel != nil || m.pluginPanel != nil || m.settingsPanel != nil ||
		m.permPrompt != nil || m.picker != nil || m.onboarding != nil ||
		m.questionAsk != nil || m.trustDialog != nil || m.helpOverlay != nil ||
		m.doctorPanel != nil || m.searchPanel != nil ||
		m.diffReview != nil || m.subagentPanel != nil ||
		m.quitConfirm != nil
}

func (m *Model) handleMouseClick(msg tea.MouseClickMsg, area image.Rectangle) bool {
	// Plan-approval modal owns mouse events when open — its handler decides
	// whether the click is inside the plan content or just somewhere on the
	// modal frame; either way the event is consumed (no chat passthrough).
	if m.planApproval != nil {
		return m.handlePlanApprovalMouseClick(msg)
	}
	mouse := msg.Mouse()
	if mouse.Button != tea.MouseLeft || m.selectionOverlayActive() {
		return false
	}

	// Check if click is on a tool message to toggle expansion (priority over selection)
	if m.handleToolExpansionClick(mouse.Y, area) {
		// Clear any selection that might have started
		m.mouseSelect = nil
		m.applyViewportSelection()
		return true
	}

	pt, ok := m.mousePointInViewport(mouse.X, mouse.Y, area)
	if !ok {
		m.mouseSelect = nil
		m.applyViewportSelection()
		return false
	}
	m.mouseSelect = &mouseSelectionState{start: pt, end: pt}
	m.applyViewportSelection()
	return true
}

func (m *Model) handleMouseMotion(msg tea.MouseMotionMsg, area image.Rectangle) bool {
	if m.planApproval != nil {
		return m.handlePlanApprovalMouseMotion(msg)
	}
	if m.mouseSelect == nil || m.selectionOverlayActive() {
		return false
	}
	mouse := msg.Mouse()
	if mouse.Button != tea.MouseLeft {
		return false
	}
	pt, ok := m.mousePointInViewport(mouse.X, mouse.Y, area)
	if !ok {
		pt = m.clampMousePointToViewport(mouse.X, mouse.Y, area)
	}
	if pt != m.mouseSelect.end {
		m.mouseSelect.end = pt
		m.mouseSelect.dragged = true
		m.applyViewportSelection()
	}
	return true
}

func (m *Model) handleMouseRelease(msg tea.MouseReleaseMsg, area image.Rectangle) (bool, tea.Cmd) {
	if m.planApproval != nil {
		return m.handlePlanApprovalMouseRelease(msg)
	}
	if m.mouseSelect == nil || m.selectionOverlayActive() {
		return false, nil
	}
	mouse := msg.Mouse()
	if mouse.Button != tea.MouseLeft && mouse.Button != tea.MouseNone {
		return false, nil
	}
	if pt, ok := m.mousePointInViewport(mouse.X, mouse.Y, area); ok {
		m.mouseSelect.end = pt
	}
	text := m.selectedViewportText()
	dragged := m.mouseSelect.dragged
	if strings.TrimSpace(text) == "" || !dragged {
		m.mouseSelect = nil
		m.applyViewportSelection()
		return true, nil
	}
	copyToClipboard(text)
	m.flashMsg = "✓ Copied selection"
	m.applyViewportSelection()
	return true, tea.Batch(
		tea.Tick(1200*time.Millisecond, func(time.Time) tea.Msg { return clearFlash{} }),
		tea.Tick(220*time.Millisecond, func(time.Time) tea.Msg { return clearMouseSelectionMsg{} }),
	)
}

func (m *Model) mousePointInViewport(x, y int, area image.Rectangle) (mouseSelectionPoint, bool) {
	if !m.ready || m.width <= 0 || m.height <= 0 {
		return mouseSelectionPoint{}, false
	}
	layout := m.computeLayout(area)
	if !image.Pt(x, y).In(layout.viewport) {
		return mouseSelectionPoint{}, false
	}
	line := m.vp.YOffset() + y - layout.viewport.Min.Y
	col := x - layout.viewport.Min.X
	if line < 0 || line >= len(m.viewportLines) {
		return mouseSelectionPoint{}, false
	}
	return mouseSelectionPoint{line: line, col: clampSelectionInt(col, runeCount(ansi.Strip(m.viewportLines[line])))}, true
}

func (m *Model) clampMousePointToViewport(x, y int, area image.Rectangle) mouseSelectionPoint {
	layout := m.computeLayout(area)
	if y < layout.viewport.Min.Y {
		y = layout.viewport.Min.Y
	}
	if y >= layout.viewport.Max.Y {
		y = layout.viewport.Max.Y - 1
	}
	line := clampSelectionInt(m.vp.YOffset()+y-layout.viewport.Min.Y, max(0, len(m.viewportLines)-1))
	col := x - layout.viewport.Min.X
	if line >= 0 && line < len(m.viewportLines) {
		col = clampSelectionInt(col, runeCount(ansi.Strip(m.viewportLines[line])))
	}
	return mouseSelectionPoint{line: line, col: col}
}

func (m Model) selectedViewportText() string {
	if m.mouseSelect == nil {
		return ""
	}
	start, end := orderedSelection(m.mouseSelect.start, m.mouseSelect.end)
	var out []string
	for line := start.line; line <= end.line && line < len(m.viewportLines); line++ {
		if line < 0 {
			continue
		}
		plain := ansi.Strip(m.viewportLines[line])
		from, to := 0, runeCount(plain)
		if line == start.line {
			from = start.col
		}
		if line == end.line {
			to = end.col
		}
		out = append(out, sliceRunes(plain, from, to))
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func orderedSelection(a, b mouseSelectionPoint) (mouseSelectionPoint, mouseSelectionPoint) {
	if a.line > b.line || (a.line == b.line && a.col > b.col) {
		return b, a
	}
	return a, b
}

func highlightPlainLine(line string, from, to int) string {
	runes := []rune(line)
	from = clampSelectionInt(from, len(runes))
	to = clampSelectionInt(to, len(runes))
	if from > to {
		from, to = to, from
	}
	if from == to {
		return line
	}
	return string(runes[:from]) + styleMouseSelection.Render(string(runes[from:to])) + string(runes[to:])
}

func sliceRunes(s string, from, to int) string {
	runes := []rune(s)
	from = clampSelectionInt(from, len(runes))
	to = clampSelectionInt(to, len(runes))
	if from > to {
		from, to = to, from
	}
	return string(runes[from:to])
}

func runeCount(s string) int {
	return len([]rune(s))
}

func clampSelectionInt(v, hi int) int {
	hi = max(hi, 0)
	if v < 0 {
		return 0
	}
	if v > hi {
		return hi
	}
	return v
}
