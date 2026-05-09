package tui

import (
	"fmt"
	"os"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/pendingedits"
)

// diffReviewAction is the user's per-file decision.
type diffReviewAction int

const (
	diffReviewPending   diffReviewAction = iota
	diffReviewApproved                   // write to disk
	diffReviewReverted                   // discard, don't write
	diffReviewRequested                  // return to agent as follow-up
)

// diffReviewEntry pairs a pending edit with the user's decision and the
// pre-computed diff lines used for display.
type diffReviewEntry struct {
	entry     pendingedits.Entry
	action    diffReviewAction
	diffLines []pendingedits.DiffLine
}

// DiffReviewResult is sent back to the caller once the user finishes the review.
type DiffReviewResult struct {
	Approved  []pendingedits.Entry // caller should Flush these
	Requested []pendingedits.Entry // caller queues these as follow-up user message
	// Reverted entries are silently dropped.
}

// diffReviewAskMsg is sent by the end-of-turn wiring to open the overlay.
type diffReviewAskMsg struct {
	entries []pendingedits.Entry
	reply   chan<- DiffReviewResult
}

// diffReviewState drives the diff-review full-screen overlay.
type diffReviewState struct {
	reply     chan<- DiffReviewResult
	entries   []diffReviewEntry
	cursor    int  // which entry has keyboard focus in the file list
	diffFocus bool // true → Tab has moved focus to the diff viewport

	diffVP viewport.Model
}

// newDiffReviewState constructs the overlay from a list of drained pending edits.
func newDiffReviewState(entries []pendingedits.Entry, reply chan<- DiffReviewResult, vpW, vpH int) *diffReviewState {
	if vpW < 1 {
		vpW = 1
	}
	if vpH < 1 {
		vpH = 1
	}
	dr := &diffReviewState{reply: reply}
	dr.entries = make([]diffReviewEntry, len(entries))
	for i, e := range entries {
		dr.entries[i] = diffReviewEntry{
			entry:     e,
			action:    diffReviewPending,
			diffLines: pendingedits.Diff(e.OrigContent, e.NewContent),
		}
	}
	dr.diffVP = viewport.New(viewport.WithWidth(vpW), viewport.WithHeight(vpH))
	dr.diffVP.Style = lipgloss.NewStyle().Background(colorWindowBg)
	dr.diffVP.KeyMap = viewport.KeyMap{}
	dr.diffVP.MouseWheelEnabled = false
	dr.syncDiffVP()
	return dr
}

// syncDiffVP re-renders the diff for the current cursor entry into diffVP.
func (dr *diffReviewState) syncDiffVP() {
	if dr == nil || len(dr.entries) == 0 {
		return
	}
	e := dr.entries[dr.cursor]
	rendered := renderDiffLines(e.diffLines, dr.diffVP.Width())
	dr.diffVP.SetContent(rendered)
	dr.diffVP.GotoTop()
}

// resizeDiffVP updates the diff viewport dimensions on window resize.
func (dr *diffReviewState) resizeDiffVP(w, h int) {
	if dr == nil {
		return
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dr.diffVP.SetWidth(w)
	dr.diffVP.SetHeight(h)
	dr.syncDiffVP()
}

// handleDiffReviewKey handles keyboard input while the diff-review overlay is active.
func (m Model) handleDiffReviewKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	dr := m.diffReview
	if dr == nil {
		return m, nil
	}

	close := func(result DiffReviewResult) (Model, tea.Cmd) {
		reply := dr.reply
		m.diffReview = nil
		m.refreshViewport()
		return m, func() tea.Msg {
			reply <- result
			return nil
		}
	}

	pendingToApproved := func() {
		for i := range dr.entries {
			if dr.entries[i].action == diffReviewPending {
				dr.entries[i].action = diffReviewApproved
			}
		}
	}

	setAction := func(a diffReviewAction) {
		if dr.cursor >= 0 && dr.cursor < len(dr.entries) {
			dr.entries[dr.cursor].action = a
		}
	}

	advance := func() {
		if dr.cursor < len(dr.entries)-1 {
			dr.cursor++
			dr.syncDiffVP()
		}
	}

	key := msg.String()

	// Tab / shift+tab: toggle focus between file list and diff viewport.
	if key == "tab" || key == "shift+tab" {
		dr.diffFocus = !dr.diffFocus
		m.diffReview = dr
		return m, nil
	}

	// Diff-viewport scrolling when focused.
	if dr.diffFocus {
		switch key {
		case "up", "k":
			dr.diffVP.ScrollUp(1)
		case "down", "j":
			dr.diffVP.ScrollDown(1)
		case "pgup":
			dr.diffVP.PageUp()
		case "pgdown":
			dr.diffVP.PageDown()
		case "g":
			dr.diffVP.GotoTop()
		case "G":
			dr.diffVP.GotoBottom()
		}
		m.diffReview = dr
		return m, nil
	}

	// File-list navigation and per-file actions.
	switch key {
	case "up", "k":
		if dr.cursor > 0 {
			dr.cursor--
			dr.syncDiffVP()
		}
	case "down", "j":
		if dr.cursor < len(dr.entries)-1 {
			dr.cursor++
			dr.syncDiffVP()
		}
	case "a":
		setAction(diffReviewApproved)
		advance()
	case "r":
		setAction(diffReviewRequested)
		advance()
	case "x":
		setAction(diffReviewReverted)
		advance()
	case "A":
		for i := range dr.entries {
			dr.entries[i].action = diffReviewApproved
		}
		return close(buildDiffReviewResult(dr))
	case "X":
		for i := range dr.entries {
			dr.entries[i].action = diffReviewReverted
		}
		return close(buildDiffReviewResult(dr))
	case "enter", "esc":
		// Approve all undecided entries, then close.
		pendingToApproved()
		return close(buildDiffReviewResult(dr))
	case "ctrl+c":
		// Emergency escape: revert all undecided entries.
		for i := range dr.entries {
			if dr.entries[i].action == diffReviewPending {
				dr.entries[i].action = diffReviewReverted
			}
		}
		return close(buildDiffReviewResult(dr))
	}

	m.diffReview = dr
	return m, nil
}

// buildDiffReviewResult maps per-entry decisions into the result.
func buildDiffReviewResult(dr *diffReviewState) DiffReviewResult {
	var result DiffReviewResult
	for _, e := range dr.entries {
		switch e.action {
		case diffReviewApproved, diffReviewPending:
			result.Approved = append(result.Approved, e.entry)
		case diffReviewRequested:
			result.Requested = append(result.Requested, e.entry)
			// diffReviewReverted → dropped
		}
	}
	return result
}

// renderDiffReview renders the diff-review overlay into a string.
func (m Model) renderDiffReview(rectWidth, rectHeight int) string {
	dr := m.diffReview
	if dr == nil || len(dr.entries) == 0 {
		return ""
	}

	innerW := rectWidth - 6
	if innerW < 20 {
		innerW = 20
	}
	innerH := rectHeight - 4
	if innerH < 6 {
		innerH = 6
	}

	// Fixed chrome rows: header + subtitle + rule + rule + hint = 5
	const fixedRows = 5
	contentH := innerH - fixedRows
	if contentH < 3 {
		contentH = 3
	}

	// File list ~35% of width; diff gets the rest.
	listW := innerW * 35 / 100
	if listW < 14 {
		listW = 14
	}
	diffW := innerW - listW - 3 // separator (1) + two gaps (2)
	if diffW < 10 {
		diffW = 10
	}

	// Resize diff VP if dimensions changed.
	if dr.diffVP.Width() != diffW || dr.diffVP.Height() != contentH {
		dr.resizeDiffVP(diffW, contentH)
	}

	var sb strings.Builder

	plural := "s"
	if len(dr.entries) == 1 {
		plural = ""
	}
	fmt.Fprintf(&sb, "%s\n", panelHeader("Diff Review", innerW))
	fmt.Fprintf(&sb, "%s\n", stylePickerDesc.Render(
		fmt.Sprintf("  %d file%s pending — a approve · r request · x revert", len(dr.entries), plural),
	))
	fmt.Fprintf(&sb, "%s\n", panelRule(innerW))

	// Side-by-side: file list (left) │ diff (right).
	fileListStr := renderDiffFileList(dr, listW, contentH)
	diffViewStr := dr.diffVP.View()

	listLines := splitToHeight(fileListStr, contentH)
	diffViewLines := splitToHeight(diffViewStr, contentH)
	sep := stylePickerDesc.Render("│")

	for i := 0; i < contentH; i++ {
		ll := padToVisualWidth("", listW)
		if i < len(listLines) {
			ll = padToVisualWidth(listLines[i], listW)
		}
		dl := ""
		if i < len(diffViewLines) {
			dl = diffViewLines[i]
		}
		fmt.Fprintf(&sb, "%s %s %s\n", ll, sep, dl)
	}

	fmt.Fprintf(&sb, "%s\n", panelRule(innerW))

	var focusHint string
	if dr.diffFocus {
		focusHint = "↑/↓ scroll diff · tab: files"
	} else {
		focusHint = "↑/↓ files · tab: scroll diff"
	}
	hint := fmt.Sprintf("%s · A all · X revert all · Enter/Esc done · ^C abort", focusHint)
	fmt.Fprintf(&sb, "%s", stylePickerDesc.Width(innerW).Render(hint))

	return panelFrameStyle(rectWidth, rectHeight).Render(sb.String())
}

// renderDiffFileList renders the left-panel file list column.
func renderDiffFileList(dr *diffReviewState, width, height int) string {
	var sb strings.Builder
	for i, e := range dr.entries {
		if i >= height {
			break
		}
		badge := diffActionBadge(e.action)
		name := diffShortPath(e.entry.Path, width-5)
		line := badge + " " + name
		if i == dr.cursor {
			fmt.Fprintf(&sb, "%s\n", stylePickerItemSelected.Render("❯ "+diffTrimRunes(line, width-2)))
		} else {
			fmt.Fprintf(&sb, "%s\n", stylePickerItem.Render("  "+diffTrimRunes(line, width-2)))
		}
	}
	return sb.String()
}

// diffActionBadge returns a one-rune status indicator.
func diffActionBadge(a diffReviewAction) string {
	switch a {
	case diffReviewApproved:
		return "✓"
	case diffReviewReverted:
		return "✗"
	case diffReviewRequested:
		return "↺"
	default:
		return "?"
	}
}

// renderDiffLines converts DiffLine records into a color-coded string for the viewport.
func renderDiffLines(lines []pendingedits.DiffLine, width int) string {
	if len(lines) == 0 {
		return stylePickerDesc.Render("(no changes)")
	}
	var sb strings.Builder
	for _, ln := range lines {
		var prefix string
		var style lipgloss.Style
		switch ln.Op {
		case pendingedits.DiffInsert:
			prefix = "+"
			style = cDiffAdd
		case pendingedits.DiffDelete:
			prefix = "-"
			style = cDiffDel
		default:
			prefix = " "
			style = stylePickerDesc
		}
		text := prefix + ln.Text
		if width > 2 && len([]rune(text)) > width {
			text = string([]rune(text)[:width])
		}
		fmt.Fprintf(&sb, "%s\n", style.Render(text))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// splitToHeight splits a rendered string into exactly h lines.
func splitToHeight(s string, h int) []string {
	lines := strings.Split(s, "\n")
	if len(lines) > h {
		return lines[:h]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines
}

// diffShortPath shortens a path to fit maxW runes, using ~/ for $HOME.
func diffShortPath(path string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	home, err := os.UserHomeDir()
	if err == nil && strings.HasPrefix(path, home+"/") {
		path = "~" + path[len(home):]
	}
	return diffTrimRunes(path, maxW)
}

// diffTrimRunes trims s to at most maxW runes, appending "…" when truncated.
func diffTrimRunes(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxW {
		return s
	}
	if maxW <= 1 {
		return string(runes[:maxW])
	}
	return string(runes[:maxW-1]) + "…"
}

// padToVisualWidth pads s (which may have ANSI sequences) to width visible cells.
func padToVisualWidth(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}
