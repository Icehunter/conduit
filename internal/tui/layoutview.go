package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/icehunter/conduit/internal/coordinator"
	"github.com/icehunter/conduit/internal/permissions"
)

// applyLayout recalculates component dimensions.
func (m Model) applyLayout() Model {
	if m.width == 0 || m.height == 0 {
		return m
	}
	inputRows := m.input.LineCount()
	inputRows = max(inputRows, 1)
	usageRows := m.usageFooterRows()
	vpHeight := m.height - chromeHeight(inputRows, m.height) - usageRows - m.todoStripRows()
	vpHeight = max(vpHeight, 1)
	// Match the textarea's visible row count to the available chrome budget
	// so it doesn't try to render more rows than the layout reserved.
	visibleRows := m.height - vpHeight - chromeFixed - usageRows - m.todoStripRows()
	if visibleRows < inputMinRows {
		visibleRows = inputMinRows
	}
	if visibleRows > inputMaxRows {
		visibleRows = inputMaxRows
	}
	m.input.SetHeight(visibleRows)
	// Input inner width: full width minus left+right border (2) minus left+right padding (2).
	inputW := m.width - 4
	inputW = max(inputW, 10)

	if !m.ready {
		m.vp = viewport.New(viewport.WithWidth(m.width), viewport.WithHeight(vpHeight))
		m.vp.Style = lipgloss.NewStyle() // app bg behind viewport content
		// Disable the viewport's built-in key bindings entirely — "j","k","u","b",
		// space, etc. would fire for any non-consumed key. We handle scrolling
		// explicitly via Shift+Up/Down/PgUp/PgDn in handleKey.
		m.vp.KeyMap = viewport.KeyMap{} // disable built-in key bindings
		m.vp.MouseWheelEnabled = true   // handle tea.MouseWheelMsg for trackpad/wheel
		m.ready = true
	} else {
		m.vp.SetWidth(m.width)
		m.vp.SetHeight(vpHeight)
	}
	m.input.SetWidth(inputW)
	// Drop bubbles textarea's Placeholder feature — its internal
	// placeholderView path emits ANSI sequences (cursor reverse-video,
	// internal viewport, partial line padding) that our outer bg paint
	// can't reliably override. We render our own placeholder hint inline
	// in View() when input is empty.
	m.input.Placeholder = ""
	m.refreshViewport()
	return m
}

func (m Model) usageFooterRows() int {
	if !m.usageStatusEnabled {
		return 0
	}
	return 4
}

func (m Model) footerChromeRows() int {
	return 1 + m.usageFooterRows()
}

func (m Model) panelHeight() int {
	if m.panelH > 0 {
		return m.panelH
	}
	inputRows := m.input.Height()
	if inputRows < inputMinRows {
		inputRows = inputMinRows
	}
	inputRows += 2
	if len(m.pendingImages)+len(m.pendingPDFs) > 0 {
		inputRows++
	}
	h := m.height - m.footerChromeRows() - inputRows - 1 - renderedLineCount(renderCoordinatorPanel(m.width))
	if h < 4 {
		return 4
	}
	return h
}

// refreshViewport rebuilds the viewport content string.
//
// Sticky-bottom: if the user was already pinned to the bottom (reading
// new content as it streams), we re-pin after rebuilding. If they
// scrolled up to read history, SetContent leaves YOffset alone in
// bubbles v2 — but we explicitly re-call GotoBottom only when AtBottom
// was already true, so in-flight scrollback is preserved while the
// model is streaming new tokens.
func (m *Model) refreshViewport() {
	if !m.ready {
		return
	}
	w := m.vp.Width()
	if w <= 0 {
		return
	}
	wasAtBottom := m.vp.AtBottom()
	var sb strings.Builder
	first := true
	for _, msg := range m.messages {
		rendered := renderMessage(msg, w, m.verboseMode)
		if rendered == "" {
			continue // skip empty renders (e.g. pure companion quip messages)
		}
		if !first && msg.Role != RoleAssistantInfo {
			sb.WriteString(separator(w))
			sb.WriteByte('\n')
		}
		first = false
		sb.WriteString(rendered)
		sb.WriteByte('\n')
	}
	if m.streaming != "" {
		if !first {
			sb.WriteString(separator(w))
			sb.WriteByte('\n')
		}
		displayStreaming := m.stripCompanionMarker(m.streaming)
		if displayStreaming != "" {
			sb.WriteString(renderMessage(m.assistantMessage(displayStreaming), w, m.verboseMode))
			sb.WriteByte('\n')
		}
	}
	m.setViewportContent(sb.String())
	if wasAtBottom {
		m.vp.GotoBottom()
	}
}

// View renders the full TUI frame. v2 returns tea.View — internally we
// still build a string and wrap it via mkView so all the existing
// rendering/paint logic stays unchanged.
//
// Basic keyboard disambiguation (shift+enter, ctrl+i, etc) is enabled by
// default in bubbletea v2 — no opt-in required for those keys. The
// KeyboardEnhancements field below opts into more advanced features:
// ReportAlternateKeys lets terminals report alternate key values (helps
// international keyboards), and we leave ReportEventTypes off because we
// don't need key release events.
func (m Model) View() tea.View {
	if !m.ready {
		return makeTeaView("Loading…\n")
	}

	// Re-apply theme styles to widgets every render. Necessary because
	// Bubble Tea returns NEW Model values from Update — any closure that
	// captured a pointer at startup (e.g. theme.OnChange listener) refers
	// to a stale Model the framework no longer uses. Cheap to do per-frame
	// (just struct field assignment) and guarantees theme switches apply.
	applyTextareaTheme(&m.input)
	m.working.SetColorsWithBackground(colorAccent, colorTool, colorDim, colorWindowBg)

	canvas := uv.NewScreenBuffer(m.width, m.height)
	canvas.Fill(&uv.Cell{
		Content: " ",
		Width:   1,
		Style: uv.Style{
			Fg: colorFg,
			Bg: colorWindowBg,
		},
	})
	m.Draw(canvas, canvas.Bounds())
	rendered := strings.ReplaceAll(canvas.Render(), "\r\n", "\n")
	return makeTeaView(paintApp(m.width, m.height, rendered))
}

func (m Model) renderFooterStack(usageFooter, statusBar string) string {
	if usageFooter == "" {
		return statusBar
	}
	return lipgloss.JoinVertical(lipgloss.Left, usageFooter, statusBar)
}

func (m Model) renderModeStatus() string {
	var mode string
	var style lipgloss.Style
	switch m.permissionMode {
	case permissions.ModeAcceptEdits:
		mode = "⏵⏵ accept edits"
		style = styleModePurple
	case permissions.ModePlan:
		mode = "⏸ plan mode"
		style = styleModeCyan
	case permissions.ModeBypassPermissions:
		mode = "⏵⏵ auto"
		style = styleModeYellow
	case permissions.ModeCouncil:
		switch {
		case m.councilSynthesizing:
			mode = "⚖ council · synthesising…"
		case m.councilActiveCount > 0:
			roundLabel := "propose"
			if m.councilRound > 0 {
				roundLabel = fmt.Sprintf("round %d/%d", m.councilRound, m.councilMaxRounds)
			}
			mode = fmt.Sprintf("⚖ council · %s · %d active", roundLabel, m.councilActiveCount)
		default:
			mode = "⚖ council"
		}
		style = styleModeGreen
	default:
		mode = "default mode"
		style = styleStatus
	}
	renderedMode := style.Render(mode)
	if coordinator.IsActive() {
		renderedMode = styleStatus.Render("⬡ coordinator") + styleStatus.Render(" | ") + renderedMode
	}
	return renderedMode + styleStatus.Render(" (shift+tab to cycle)")
}

func (m Model) renderTokenStatus() string {
	// Plan users see tokens + cost in the Context row of the usage footer.
	// Only show here for API-key users who have no footer.
	if m.usageStatusEnabled || m.contextInputTokens == 0 {
		return ""
	}
	tok := m.contextInputTokens
	var tokStr string
	switch {
	case tok >= 1_000_000:
		tokStr = fmt.Sprintf("%.1fM tok", float64(tok)/1_000_000)
	case tok >= 1_000:
		tokStr = fmt.Sprintf("%.1fk tok", float64(tok)/1_000)
	default:
		tokStr = fmt.Sprintf("%d tok", tok)
	}
	if m.costUSD > 0 {
		return styleStatus.Render(tokStr) + styleStatus.Render(fmt.Sprintf(" · $%.2f", m.costUSD))
	}
	return styleStatus.Render(tokStr)
}

func (m Model) renderLocationStatus(maxWidth int) string {
	if maxWidth < 8 {
		return ""
	}
	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		return ""
	}
	branch := gitBranchName(cwd)
	sep := " | "
	if branch == "" {
		return styleStatus.Render(truncateMiddle(displayPath(cwd), maxWidth))
	}
	branchMax := min(lipgloss.Width(branch), maxWidth/3)
	if branchMax < 8 && maxWidth >= 20 {
		branchMax = 8
	}
	if branchMax > maxWidth {
		branchMax = maxWidth
	}
	branchText := truncateMiddle(branch, branchMax)
	cwdMax := maxWidth - lipgloss.Width(sep) - lipgloss.Width(branchText)
	if cwdMax < 8 {
		return styleStatus.Render(truncateMiddle(displayPath(cwd), maxWidth))
	}
	cwdText := truncateMiddle(displayPath(cwd), cwdMax)
	return styleStatus.Render(cwdText) + styleStatus.Render(sep) + styleStatusAccent.Render(branchText)
}

func displayPath(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(os.PathSeparator)) {
			return "~" + strings.TrimPrefix(path, home)
		}
	}
	return path
}

func gitBranchName(cwd string) string {
	gitDir := findGitDir(cwd)
	if gitDir == "" {
		return ""
	}
	headPath := filepath.Join(gitDir, "HEAD")
	raw, err := os.ReadFile(headPath)
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(raw))
	const prefix = "ref: refs/heads/"
	if strings.HasPrefix(head, prefix) {
		return strings.TrimPrefix(head, prefix)
	}
	if len(head) >= 7 {
		return head[:7]
	}
	return head
}

func findGitDir(cwd string) string {
	for dir := cwd; dir != ""; dir = filepath.Dir(dir) {
		gitPath := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitPath); err == nil && info.IsDir() {
			return gitPath
		}
		raw, err := os.ReadFile(gitPath)
		if err == nil {
			text := strings.TrimSpace(string(raw))
			const prefix = "gitdir: "
			if strings.HasPrefix(text, prefix) {
				gitDir := strings.TrimSpace(strings.TrimPrefix(text, prefix))
				if !filepath.IsAbs(gitDir) {
					gitDir = filepath.Join(dir, gitDir)
				}
				return filepath.Clean(gitDir)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return ""
}

func truncateMiddle(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	if maxWidth == 1 {
		return "…"
	}
	runes := []rune(s)
	leftTarget := (maxWidth - 1) / 2
	rightTarget := maxWidth - 1 - leftTarget
	var left []rune
	for _, r := range runes {
		if lipgloss.Width(string(left))+lipgloss.Width(string(r)) > leftTarget {
			break
		}
		left = append(left, r)
	}
	var right []rune
	for i := len(runes) - 1; i >= 0; i-- {
		r := runes[i]
		if lipgloss.Width(string(right))+lipgloss.Width(string(r)) > rightTarget {
			break
		}
		right = append([]rune{r}, right...)
	}
	return string(left) + "…" + string(right)
}
