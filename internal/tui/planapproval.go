package tui

import (
	"fmt"
	"image"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/tools/planmodetool"
)

// planApprovalKind tags each option's effect on the model's tool result.
type planApprovalKind int

const (
	planApprovalKindBypass planApprovalKind = iota
	planApprovalKindAcceptEdits
	planApprovalKindAcceptEditsLive
	planApprovalKindDefault
	planApprovalKindChat
)

type planApprovalOption struct {
	label string
	kind  planApprovalKind
}

var planApprovalOptions = []planApprovalOption{
	{"Approve — auto mode (run all tools without prompts)", planApprovalKindBypass},
	{"Approve — accept edits only (writes auto, shell still asks)", planApprovalKindAcceptEdits},
	{"Approve — live review (pause mid-turn to review each hunk)", planApprovalKindAcceptEditsLive},
	{"Approve — default mode (prompt for each tool call)", planApprovalKindDefault},
	{"💬 Chat about this — keep planning, share more context", planApprovalKindChat},
}

// planApprovalPickerState drives the plan-approval take-over modal shown when
// the model calls ExitPlanMode. The user reads the plan in a scrollable
// viewport, then chooses how to approve or asks for refinement.
type planApprovalPickerState struct {
	reply chan<- planmodetool.PlanApprovalDecision

	// plan is the raw plan text from the model. Kept verbatim so the user
	// can copy-paste it; the viewport renders a markdown-styled view.
	plan string

	// vp scrolls the plan content. Sized to the modal's inner content area.
	vp viewport.Model

	// planLines holds the un-styled (but post-wrap) lines of the plan as
	// shown in vp. Used for mouse-selection text extraction.
	planLines []string

	// planRect is the absolute screen rectangle occupied by the plan
	// viewport's content (excluding borders). Computed at render time and
	// read by the mouse handlers; the (0,0,0,0) zero value disables
	// selection until the first frame is drawn.
	planRect image.Rectangle

	// planSelect tracks an in-progress mouse selection inside the plan.
	planSelect *mouseSelectionState

	// selected is the highlighted option index.
	selected int
}

// planApprovalAskMsg is sent by the ExitPlanMode callback to open the modal.
type planApprovalAskMsg struct {
	plan  string
	reply chan planmodetool.PlanApprovalDecision
}

// newPlanApprovalState builds the picker state for a freshly-arrived plan.
// width and height are the inner content dimensions of the plan viewport
// (border and surrounding chrome already deducted by the caller).
func newPlanApprovalState(plan string, reply chan<- planmodetool.PlanApprovalDecision, vpWidth, vpHeight int) *planApprovalPickerState {
	if vpWidth < 1 {
		vpWidth = 1
	}
	if vpHeight < 1 {
		vpHeight = 1
	}
	rendered := renderMarkdown(plan, vpWidth)
	if strings.TrimSpace(rendered) == "" {
		rendered = stylePickerDesc.Render("(empty plan)")
	}
	vp := viewport.New(viewport.WithWidth(vpWidth), viewport.WithHeight(vpHeight))
	vp.Style = lipgloss.NewStyle().Background(colorWindowBg)
	vp.KeyMap = viewport.KeyMap{} // we drive scrolling explicitly
	vp.MouseWheelEnabled = false  // we own mouse handling for selection
	vp.SetContent(rendered)
	return &planApprovalPickerState{
		reply:     reply,
		plan:      plan,
		vp:        vp,
		planLines: strings.Split(rendered, "\n"),
		selected:  0,
	}
}

// resizePlanApproval rebuilds the viewport when the panel rect changes (window
// resize, etc.). Preserves cursor position and scroll offset where possible.
func (pa *planApprovalPickerState) resize(vpWidth, vpHeight int) {
	if pa == nil {
		return
	}
	if vpWidth < 1 {
		vpWidth = 1
	}
	if vpHeight < 1 {
		vpHeight = 1
	}
	yOff := pa.vp.YOffset()
	rendered := renderMarkdown(pa.plan, vpWidth)
	if strings.TrimSpace(rendered) == "" {
		rendered = stylePickerDesc.Render("(empty plan)")
	}
	pa.vp.SetWidth(vpWidth)
	pa.vp.SetHeight(vpHeight)
	pa.vp.SetContent(rendered)
	pa.vp.SetYOffset(yOff)
	pa.planLines = strings.Split(rendered, "\n")
	// Rect is reset and recomputed by the next render call.
	pa.planRect = image.Rectangle{}
	pa.planSelect = nil
}

// optionDecision maps an option index to the PlanApprovalDecision sent back
// to the ExitPlanMode tool. The "chat about this" option is functionally
// equivalent to a rejection from the model's perspective; it differs only in
// the user-facing label.
func (o planApprovalOption) decision() planmodetool.PlanApprovalDecision {
	switch o.kind {
	case planApprovalKindBypass:
		return planmodetool.PlanApprovalDecision{Approved: true, Mode: permissions.ModeBypassPermissions}
	case planApprovalKindAcceptEdits:
		return planmodetool.PlanApprovalDecision{Approved: true, Mode: permissions.ModeAcceptEdits}
	case planApprovalKindAcceptEditsLive:
		return planmodetool.PlanApprovalDecision{Approved: true, Mode: permissions.ModeAcceptEditsLive}
	case planApprovalKindDefault:
		return planmodetool.PlanApprovalDecision{Approved: true, Mode: permissions.ModeDefault}
	case planApprovalKindChat:
		return planmodetool.PlanApprovalDecision{Approved: false}
	}
	return planmodetool.PlanApprovalDecision{Approved: false}
}

// handlePlanApprovalKey handles keyboard input while the plan-approval modal
// is active. ↑/↓ moves the option chevron, pgup/pgdn scrolls the plan,
// numeric keys and Enter commit an option, Esc opens the chat path.
func (m Model) handlePlanApprovalKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	pa := m.planApproval
	if pa == nil {
		return m, nil
	}

	send := func(d planmodetool.PlanApprovalDecision) (Model, tea.Cmd) {
		reply := pa.reply
		m.planApproval = nil
		m.refreshViewport()
		return m, func() tea.Msg {
			reply <- d
			return nil
		}
	}

	key := msg.String()
	switch key {
	case "esc", "ctrl+c":
		// Esc collapses to the "chat about this" path: rejected, user can
		// type a follow-up that the model will treat as plan refinement.
		return send(planmodetool.PlanApprovalDecision{Approved: false})
	case "1", "2", "3", "4", "5":
		idx := int(key[0] - '1')
		if idx >= 0 && idx < len(planApprovalOptions) {
			return send(planApprovalOptions[idx].decision())
		}
	case "enter", "space":
		if pa.selected >= 0 && pa.selected < len(planApprovalOptions) {
			return send(planApprovalOptions[pa.selected].decision())
		}
	case "up", "ctrl+p", "k":
		if pa.selected > 0 {
			pa.selected--
		}
	case "down", "ctrl+n", "j":
		if pa.selected < len(planApprovalOptions)-1 {
			pa.selected++
		}
	case "home":
		pa.selected = 0
	case "end":
		pa.selected = len(planApprovalOptions) - 1
	case "pgup":
		pa.vp.PageUp()
	case "pgdown":
		pa.vp.PageDown()
	case "g":
		pa.vp.GotoTop()
	case "G":
		pa.vp.GotoBottom()
	}

	m.planApproval = pa
	return m, nil
}

// renderPlanApprovalPicker renders the modal into a string sized to fit the
// chat-panel rect supplied by the caller. The caller pastes the result via
// drawFloatingRendered.
//
// rectWidth/rectHeight are the OUTER dimensions of the panel frame (border
// included). The function sizes the inner viewport to fill the remaining
// vertical space after the options block.
func (m Model) renderPlanApprovalPicker(rectWidth, rectHeight int) string {
	pa := m.planApproval
	if pa == nil {
		return ""
	}

	// Frame chrome: rounded border (2) + horizontal padding (4) → inner width.
	innerW := rectWidth - 6
	if innerW < 10 {
		innerW = 10
	}
	// Inner content height = rectHeight - border (2) - vertical padding (2).
	innerH := rectHeight - 4
	if innerH < 5 {
		innerH = 5
	}

	// Fixed chrome rows inside the frame:
	//   header (1) + blank (1) + "Here is the plan:" (1)
	//   dashed top (1) + plan vp (N) + dashed bottom (1)
	//   blank (1) + options (len) + blank (1) + hint (≥1)
	// Hint may wrap on narrow widths; measure its actual rendered height so
	// the bottom of the frame doesn't get clipped.
	hint := planApprovalHint()
	hintRendered := stylePickerDesc.Width(innerW).Render(hint)
	hintRows := lipgloss.Height(hintRendered)
	if hintRows < 1 {
		hintRows = 1
	}
	const baseFixedRows = 1 + 1 + 1 + 1 + 1 + 1 + 1 // 7 non-vp rows + dashed borders (excludes hint)
	optionRows := len(planApprovalOptions)
	vpHeight := innerH - baseFixedRows - optionRows - hintRows
	if vpHeight < 3 {
		vpHeight = 3
	}

	// Plan viewport content width: inner minus dashed-border (2) = innerW - 2.
	vpInnerW := innerW - 2
	if vpInnerW < 10 {
		vpInnerW = 10
	}

	// Resize viewport if dimensions changed.
	if pa.vp.Width() != vpInnerW || pa.vp.Height() != vpHeight {
		pa.resize(vpInnerW, vpHeight)
		// Re-apply selection highlight after content rebuild.
	}
	applyPlanApprovalSelection(pa)

	var sb strings.Builder
	sb.WriteString(panelHeader("Ready to code?", innerW) + "\n")
	sb.WriteString("\n")
	sb.WriteString(stylePickerItem.Render("Here is the plan:") + "\n")

	// Dashed border around plan viewport.
	dashed := strings.Repeat("─", vpInnerW)
	sb.WriteString(stylePickerDesc.Render(dashed) + "\n")
	sb.WriteString(pa.vp.View() + "\n")
	sb.WriteString(stylePickerDesc.Render(dashed) + "\n")
	sb.WriteString("\n")

	// Options.
	for i, opt := range planApprovalOptions {
		num := fmt.Sprintf("%d. ", i+1)
		line := num + opt.label
		if i == pa.selected {
			fmt.Fprintf(&sb, "%s\n", stylePickerItemSelected.Render("❯ "+line))
		} else {
			fmt.Fprintf(&sb, "%s\n", stylePickerItem.Render("  "+line))
		}
	}

	sb.WriteString("\n")
	sb.WriteString(hintRendered)

	return panelFrameStyle(rectWidth, rectHeight).Render(sb.String())
}

func planApprovalHint() string {
	return "↑/↓ select · Enter approve · 1-5 quick · Esc chat"
}

// recordPlanApprovalRect stores the absolute screen rect of the plan viewport
// content. Called from the draw layer once the modal's outer rect is known.
// The mouse handlers read this to decide whether a click is inside the plan.
func (pa *planApprovalPickerState) recordPlanRect(modalRect image.Rectangle) {
	if pa == nil {
		return
	}
	// Layout inside the frame, top to bottom:
	//   row 0:           top border
	//   row 1:           top padding
	//   row 2:           header
	//   row 3:           blank
	//   row 4:           "Here is the plan:"
	//   row 5:           dashed top
	//   rows 6..6+vpH-1: plan viewport content
	//   row 6+vpH:       dashed bottom
	//   ...
	const planTopOffset = 6
	planX0 := modalRect.Min.X + 1 + 2 + 1 // border (1) + body padX (2) + dashed-box pad (1)
	planX1 := modalRect.Max.X - 1 - 2 - 1
	planY0 := modalRect.Min.Y + planTopOffset
	planY1 := planY0 + pa.vp.Height()
	if planX1 <= planX0 || planY1 <= planY0 {
		pa.planRect = image.Rectangle{}
		return
	}
	pa.planRect = image.Rect(planX0, planY0, planX1, planY1)
}

// handlePlanApprovalMouseClick starts a selection if the click lands inside
// the plan viewport content. Returns true when the event was consumed.
func (m *Model) handlePlanApprovalMouseClick(msg tea.MouseClickMsg) bool {
	pa := m.planApproval
	if pa == nil {
		return false
	}
	mouse := msg.Mouse()
	if mouse.Button != tea.MouseLeft {
		// Reset any previous selection on non-left-click but consume so the
		// click doesn't bleed through to the chat viewport behind the modal.
		pa.planSelect = nil
		applyPlanApprovalSelection(pa)
		return true
	}
	pt, ok := planApprovalPointInPlan(pa, mouse.X, mouse.Y)
	if !ok {
		pa.planSelect = nil
		applyPlanApprovalSelection(pa)
		// Still consume — clicks elsewhere on the modal shouldn't fall through.
		return true
	}
	pa.planSelect = &mouseSelectionState{start: pt, end: pt}
	applyPlanApprovalSelection(pa)
	return true
}

// handlePlanApprovalMouseMotion extends an in-progress selection.
func (m *Model) handlePlanApprovalMouseMotion(msg tea.MouseMotionMsg) bool {
	pa := m.planApproval
	if pa == nil || pa.planSelect == nil {
		return pa != nil
	}
	mouse := msg.Mouse()
	if mouse.Button != tea.MouseLeft {
		return true
	}
	pt, ok := planApprovalPointInPlan(pa, mouse.X, mouse.Y)
	if !ok {
		pt = planApprovalClampPoint(pa, mouse.X, mouse.Y)
	}
	if pt != pa.planSelect.end {
		pa.planSelect.end = pt
		pa.planSelect.dragged = true
		applyPlanApprovalSelection(pa)
	}
	return true
}

// handlePlanApprovalMouseRelease finalises a selection and copies to clipboard.
func (m *Model) handlePlanApprovalMouseRelease(msg tea.MouseReleaseMsg) (bool, tea.Cmd) {
	pa := m.planApproval
	if pa == nil {
		return false, nil
	}
	if pa.planSelect == nil {
		return true, nil
	}
	mouse := msg.Mouse()
	if mouse.Button != tea.MouseLeft && mouse.Button != tea.MouseNone {
		return true, nil
	}
	if pt, ok := planApprovalPointInPlan(pa, mouse.X, mouse.Y); ok {
		pa.planSelect.end = pt
	}
	text := selectedPlanText(pa)
	dragged := pa.planSelect.dragged
	if strings.TrimSpace(text) == "" || !dragged {
		pa.planSelect = nil
		applyPlanApprovalSelection(pa)
		return true, nil
	}
	copyToClipboard(text)
	m.flashMsg = "✓ Copied selection"
	applyPlanApprovalSelection(pa)
	return true, tea.Batch(
		tea.Tick(1200*time.Millisecond, func(time.Time) tea.Msg { return clearFlash{} }),
		tea.Tick(220*time.Millisecond, func(time.Time) tea.Msg { return clearMouseSelectionMsg{} }),
	)
}

// planApprovalPointInPlan converts an absolute screen (x,y) to a logical
// (line, col) inside pa.planLines, returning false if the click is outside
// the plan viewport's content rect.
func planApprovalPointInPlan(pa *planApprovalPickerState, x, y int) (mouseSelectionPoint, bool) {
	if pa == nil || pa.planRect.Empty() {
		return mouseSelectionPoint{}, false
	}
	if !image.Pt(x, y).In(pa.planRect) {
		return mouseSelectionPoint{}, false
	}
	line := pa.vp.YOffset() + (y - pa.planRect.Min.Y)
	col := x - pa.planRect.Min.X
	if line < 0 || line >= len(pa.planLines) {
		return mouseSelectionPoint{}, false
	}
	return mouseSelectionPoint{
		line: line,
		col:  clampSelectionInt(col, runeCount(ansi.Strip(pa.planLines[line]))),
	}, true
}

// planApprovalClampPoint clamps an out-of-bounds drag to the plan rect edges.
func planApprovalClampPoint(pa *planApprovalPickerState, x, y int) mouseSelectionPoint {
	if pa.planRect.Empty() || len(pa.planLines) == 0 {
		return mouseSelectionPoint{}
	}
	if y < pa.planRect.Min.Y {
		y = pa.planRect.Min.Y
	}
	if y >= pa.planRect.Max.Y {
		y = pa.planRect.Max.Y - 1
	}
	line := clampSelectionInt(pa.vp.YOffset()+(y-pa.planRect.Min.Y), max(0, len(pa.planLines)-1))
	col := x - pa.planRect.Min.X
	if line >= 0 && line < len(pa.planLines) {
		col = clampSelectionInt(col, runeCount(ansi.Strip(pa.planLines[line])))
	}
	return mouseSelectionPoint{line: line, col: col}
}

// applyPlanApprovalSelection re-renders the plan viewport with the current
// selection highlighted (reverse-video). Called after every selection change.
func applyPlanApprovalSelection(pa *planApprovalPickerState) {
	if pa == nil || len(pa.planLines) == 0 {
		return
	}
	if pa.planSelect == nil {
		pa.vp.SetContent(strings.Join(pa.planLines, "\n"))
		return
	}
	lines := append([]string(nil), pa.planLines...)
	start, end := orderedSelection(pa.planSelect.start, pa.planSelect.end)
	for line := start.line; line <= end.line && line < len(lines); line++ {
		if line < 0 {
			continue
		}
		plain := ansi.Strip(lines[line])
		from, to := 0, runeCount(plain)
		if line == start.line {
			from = start.col
		}
		if line == end.line {
			to = end.col
		}
		lines[line] = highlightPlainLine(plain, from, to)
	}
	pa.vp.SetContent(strings.Join(lines, "\n"))
}

// selectedPlanText extracts the user's selected plan text as plain UTF-8.
func selectedPlanText(pa *planApprovalPickerState) string {
	if pa == nil || pa.planSelect == nil || len(pa.planLines) == 0 {
		return ""
	}
	start, end := orderedSelection(pa.planSelect.start, pa.planSelect.end)
	var out []string
	for line := start.line; line <= end.line && line < len(pa.planLines); line++ {
		if line < 0 {
			continue
		}
		plain := ansi.Strip(pa.planLines[line])
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
