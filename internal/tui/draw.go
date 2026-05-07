package tui

import (
	"fmt"
	"image"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/icehunter/conduit/internal/theme"
)

func makeTeaView(content string) tea.View {
	var v tea.View
	v.SetContent(content)
	v.AltScreen = true
	v.KeyboardEnhancements.ReportAlternateKeys = true
	// MouseModeCellMotion: scroll wheel arrives as tea.MouseWheelMsg
	// (separate from UP/DOWN key events) so trackpad/wheel scrolls the
	// viewport while UP/DOWN navigates input history — matching CC's UX.
	// Trade-off: text selection requires Shift+drag (standard for
	// mouse-mode TUIs like vim/tmux). Text copy still works via ^Y.
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m Model) Draw(scr uv.Screen, area image.Rectangle) {
	layout := m.computeLayout(area)

	drawString(scr, layout.viewport, m.vp.View())
	drawString(scr, layout.workingRow, m.renderWorkingRow())
	drawString(scr, layout.input, m.renderInputBox())
	if coord := renderCoordinatorPanel(area.Dx()); coord != "" {
		drawString(scr, layout.coordinator, coord)
	}
	drawString(scr, layout.footer, m.renderFooter())

	// Trust dialog covers the entire viewport with no content behind it.
	if m.trustDialog != nil {
		drawFloating(scr, area, m.renderTrustDialog(), floatingModalSpec, false)
		return
	}

	panelModel := m
	panelModel.width = layout.panel.Dx()
	panelModel.panelH = layout.panel.Dy()
	panel := panelModel.renderActivePanel()
	picker := m.renderActivePicker()
	modal := m.renderActiveModal()
	if panel != "" {
		drawFloatingRendered(scr, layout.panel, panel)
	}
	if picker != "" {
		spec := floatingPickerSpec
		if m.picker != nil && m.picker.kind == "model" {
			spec = floatingModelPickerSpec
			drawFloating(scr, layout.panel, picker, spec, false)
		} else if m.commandPickerActive() {
			drawCommandPickerAboveInput(scr, layout, picker)
		} else {
			drawPickerAboveInput(scr, layout, picker, spec)
		}
	}
	if modal != "" {
		drawFloating(scr, layout.panel, modal, floatingModalSpec, false)
	}
	if m.companionBubble != "" && panel == "" && picker == "" && modal == "" {
		drawCompanionOverlay(scr, layout, m.renderCompanionBubble())
	}
}

func drawString(scr uv.Screen, rect image.Rectangle, rendered string) {
	if rect.Empty() || rendered == "" {
		return
	}
	rendered = withSurfaceAfterReset(rendered)
	uv.NewStyledString(rendered).Draw(scr, rect)
}

func withSurfaceAfterReset(rendered string) string {
	bgEsc := theme.AnsiBG(windowBgHex)
	fgEsc := theme.AnsiFG(theme.Active().Primary)
	const fullReset = "\x1b[0m"
	const softReset = "\x1b[22;23;39m"
	rendered = strings.ReplaceAll(rendered, fullReset, softReset+bgEsc+fgEsc)
	rendered = strings.ReplaceAll(rendered, " ", bgEsc+" ")
	return bgEsc + fgEsc + rendered
}

func drawPickerAboveInput(scr uv.Screen, layout uiLayout, rendered string, spec floatingSpec) {
	if rendered == "" {
		return
	}
	area := image.Rect(layout.pickerArea.Min.X, layout.pickerArea.Min.Y, layout.pickerArea.Max.X, layout.input.Min.Y)
	drawFloating(scr, area, rendered, spec, true)
}

func drawCommandPickerAboveInput(scr uv.Screen, layout uiLayout, rendered string) {
	if rendered == "" {
		return
	}
	area := image.Rect(layout.pickerArea.Min.X, layout.pickerArea.Min.Y, layout.pickerArea.Max.X, layout.input.Min.Y)
	if area.Dx() > commandPickerSideMargin*2+floatingCommandSpec.minWidth {
		area.Min.X += commandPickerSideMargin
		area.Max.X -= commandPickerSideMargin
	}
	drawFloatingExactWidth(scr, area, rendered, floatingCommandSpec, true)
}

func drawCompanionOverlay(scr uv.Screen, layout uiLayout, rendered string) {
	if rendered == "" {
		return
	}
	width := lipgloss.Width(rendered)
	height := lipgloss.Height(rendered)
	if width < 1 || height < 1 {
		return
	}
	x := layout.input.Max.X - width - 2
	if x < layout.input.Min.X+2 {
		x = layout.input.Min.X + 2
	}
	y := layout.input.Min.Y - height - 1
	if y < layout.viewport.Min.Y {
		y = layout.viewport.Min.Y
	}
	rect := image.Rect(x, y, min(x+width, layout.input.Max.X-1), y+height)
	drawString(scr, rect, rendered)
}

func drawFloating(scr uv.Screen, area image.Rectangle, content string, spec floatingSpec, above bool) {
	if area.Empty() || content == "" {
		return
	}
	width := floatingOuterWidth(area.Dx(), spec)
	height := floatingOuterHeight(content, area.Dy(), spec)
	rendered := renderFloatingWindow(content, width, height)
	drawFloatingRenderedAt(scr, area, rendered, above)
}

func drawFloatingExactWidth(scr uv.Screen, area image.Rectangle, content string, spec floatingSpec, above bool) {
	if area.Empty() || content == "" {
		return
	}
	width := area.Dx()
	if width < 1 {
		width = 1
	}
	height := spec.maxHeight
	if height <= 0 || height > area.Dy() {
		height = area.Dy()
	}
	if height < 1 {
		height = 1
	}
	rendered := renderFloatingWindow(content, width, height)
	drawFloatingRenderedAt(scr, area, rendered, above)
}

func drawFloatingRendered(scr uv.Screen, area image.Rectangle, rendered string) {
	if area.Empty() || rendered == "" {
		return
	}
	drawFloatingRenderedAt(scr, area, rendered, false)
}

func drawFloatingRenderedAt(scr uv.Screen, area image.Rectangle, rendered string, above bool) {
	width := lipgloss.Width(rendered)
	height := lipgloss.Height(rendered)
	if width < 1 {
		width = area.Dx()
	}
	if height < 1 {
		height = 1
	}
	if width > area.Dx() {
		width = area.Dx()
	}
	if height > area.Dy() {
		height = area.Dy()
	}
	var rect image.Rectangle
	if above {
		rect = floatingRectAbove(area, width, height)
	} else {
		rect = floatingRect(area, width, height)
	}
	drawString(scr, rect, rendered)
}

func (m Model) renderActivePanel() string {
	switch {
	case m.panel != nil:
		return m.renderPanel()
	case m.pluginPanel != nil:
		return m.renderPluginPanel()
	case m.settingsPanel != nil:
		return m.renderSettingsPanel()
	case m.helpOverlay != nil:
		return m.renderHelpOverlay()
	case m.doctorPanel != nil:
		return m.renderDoctorPanel()
	case m.searchPanel != nil:
		return m.renderSearchPanel()
	case m.onboarding != nil:
		return m.renderOnboarding()
	}
	return ""
}

func (m Model) renderActivePicker() string {
	switch {
	case m.loginPrompt != nil:
		return m.renderLoginPicker()
	case m.resumePrompt != nil:
		return m.renderResumePicker()
	case m.picker != nil:
		return m.renderPicker()
	case m.commandPickerActive():
		return m.renderCommandPicker()
	case len(m.atMatches) > 0:
		return m.renderAtPicker()
	}
	return ""
}

func (m Model) renderActiveModal() string {
	switch {
	case m.trustDialog != nil:
		return m.renderTrustDialog()
	case m.permPrompt != nil:
		return m.renderPermissionPrompt()
	case m.questionAsk != nil:
		return m.renderQuestionDialog()
	case m.planApproval != nil:
		return m.renderPlanApprovalPicker()
	}
	return ""
}

func (m Model) renderWorkingRow() string {
	switch {
	case m.flashMsg != "":
		return styleStatusAccent.Width(m.width).Render(m.flashMsg)
	case m.running && m.apiRetryStatus != "":
		return styleModeYellow.Width(m.width).Render(m.apiRetryStatus)
	case m.running:
		m.working.SetLabel("Thinking")
		return styleStatus.Width(m.width).Render(m.working.Render())
	default:
		return styleStatus.Width(m.width).Render("")
	}
}

func (m Model) renderInputBox() string {
	bStyle := styleInputBorder
	if !m.running {
		bStyle = styleInputBorderActive
	}
	innerView := m.input.View()
	{
		innerW := m.width - 4
		if innerW < 1 {
			innerW = 1
		}
		bgEsc := theme.AnsiBG(windowBgHex)
		fgEsc := theme.AnsiFG(theme.Active().Primary)
		const fullReset = "\x1b[0m"
		const softReset = "\x1b[22;23;39m"
		innerLines := strings.Split(innerView, "\n")
		fixed := make([]string, 0, len(innerLines))
		for _, line := range innerLines {
			line = strings.ReplaceAll(line, fullReset, softReset+bgEsc+fgEsc)
			w := lipgloss.Width(line)
			if w < innerW {
				line += surfaceSpaces(innerW - w)
			}
			fixed = append(fixed, bgEsc+fgEsc+line+fullReset)
		}
		innerView = strings.Join(fixed, "\n")
	}
	if n := len(m.pendingImages) + len(m.pendingPDFs); n > 0 {
		parts := []string{}
		if ni := len(m.pendingImages); ni > 0 {
			parts = append(parts, fmt.Sprintf("%d image(s)", ni))
		}
		if np := len(m.pendingPDFs); np > 0 {
			parts = append(parts, fmt.Sprintf("%d PDF(s)", np))
		}
		label := "📎 [" + strings.Join(parts, ", ") + "]"
		badge := styleStatusAccent.Render(label) + "  " + stylePickerDesc.Render("ctrl+v for more · Enter to send · Esc to clear")
		innerView = badge + "\n" + innerView
	}
	return bStyle.Width(m.width).Render(innerView)
}

func (m Model) renderFooter() string {
	edgePad := surfaceSpaces(1)
	left := edgePad + m.renderModeStatus()
	tokStr := m.renderTokenStatus()
	rightMax := m.width - lipgloss.Width(left) - lipgloss.Width(tokStr) - 2
	right := m.renderLocationStatus(rightMax)
	if right != "" {
		right += edgePad
	}
	mid := tokStr
	if mid != "" {
		mid = surfaceSpaces(1) + mid + surfaceSpaces(1)
	}
	pad := m.width - lipgloss.Width(left) - lipgloss.Width(mid) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	statusBar := left + surfaceSpaces(pad) + mid + right
	usageFooter := m.renderUsageFooter(m.width)
	return m.renderFooterStack(usageFooter, statusBar)
}
