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

// hunkContextLines is how many equal-line context rows we render around each
// change region. Matches conventional `git diff -U3`.
const hunkContextLines = 3

// diffReviewAction is the user's per-hunk decision.
type diffReviewAction int

const (
	diffReviewPending   diffReviewAction = iota
	diffReviewApproved                   // include hunk in the flushed file
	diffReviewReverted                   // drop hunk, keep original lines
	diffReviewRequested                  // ask the agent to redo this hunk
)

// hunkReview pairs a single hunk with the user's decision on it and an
// optional free-text note the user can attach via the 'n' minibuffer before
// rejecting. The note travels back to the agent in the follow-up message.
type hunkReview struct {
	hunk   pendingedits.Hunk
	action diffReviewAction
	note   string
}

// diffReviewEntry pairs a pending edit with its hunk-level review state and
// the pre-computed diff lines used for display.
type diffReviewEntry struct {
	entry     pendingedits.Entry
	diffLines []pendingedits.DiffLine
	hunks     []hunkReview
}

// DiffReviewResult is sent back to the caller once the user finishes the review.
//
// Approved holds entries with their NewContent rebuilt from only the hunks the
// user approved (Apply collapses any rejected hunks back to original lines).
// Entries where the user approved zero hunks are omitted entirely so the
// flusher does not perform a no-op write.
//
// Requested holds entries that contained at least one hunk the user marked
// "request change". The Entry's NewContent reflects the proposed diff (what
// the agent wrote) so callers can show the agent the original proposal when
// asking it to redo the work.
//
// FollowupMessage is non-empty when any hunk was marked "requested". It is a
// ready-to-send user message describing each rejected hunk (including any
// inline notes) so the caller can enqueue it as the agent's next turn input.
type DiffReviewResult struct {
	Approved        []pendingedits.Entry
	Requested       []pendingedits.Entry
	FollowupMessage string
}

// diffReviewAskMsg is sent by the end-of-turn wiring to open the overlay.
type diffReviewAskMsg struct {
	entries []pendingedits.Entry
	reply   chan<- DiffReviewResult
}

// diffReviewFollowupMsg is sent when the diff-review result contains a
// FollowupMessage. The TUI appends it to pendingMessages so it fires as the
// next user turn immediately after agentDoneMsg is processed.
type diffReviewFollowupMsg struct{ text string }

// diffReviewState drives the diff-review full-screen overlay.
//
// The cursor identifies a (file, hunk) pair. Files with zero hunks (which
// occur when an Entry was staged but the proposed content equals the original)
// are still listed but their hunk slice is empty; the cursor skips over them.
type diffReviewState struct {
	reply     chan<- DiffReviewResult
	entries   []diffReviewEntry
	fileIdx   int  // which file is focused
	hunkIdx   int  // which hunk inside that file is focused
	diffFocus bool // true → Tab moved focus to the diff viewport (free-scroll)

	// noteMode is true while the user is typing a note for the focused hunk.
	// In note mode all keys go to noteInput except Enter (commit) and Esc (cancel).
	noteMode  bool
	noteInput string

	diffVP viewport.Model
}

// newDiffReviewState constructs the overlay from a list of drained pending edits.
// Initial viewport dimensions are placeholders — the first renderDiffReview
// call resizes them to fit the modal frame.
func newDiffReviewState(entries []pendingedits.Entry, reply chan<- DiffReviewResult) *diffReviewState {
	const initW, initH = 80, 24
	dr := &diffReviewState{reply: reply}
	dr.entries = make([]diffReviewEntry, len(entries))
	for i, e := range entries {
		lines := pendingedits.Diff(e.OrigContent, e.NewContent)
		hunks := pendingedits.Hunks(lines, hunkContextLines)
		hrs := make([]hunkReview, len(hunks))
		for j, h := range hunks {
			hrs[j] = hunkReview{hunk: h, action: diffReviewPending}
		}
		dr.entries[i] = diffReviewEntry{entry: e, diffLines: lines, hunks: hrs}
	}
	dr.diffVP = viewport.New(viewport.WithWidth(initW), viewport.WithHeight(initH))
	dr.diffVP.Style = lipgloss.NewStyle().Background(colorWindowBg)
	dr.diffVP.KeyMap = viewport.KeyMap{}
	dr.diffVP.MouseWheelEnabled = false
	dr.syncDiffVP()
	return dr
}

// syncDiffVP re-renders the current file's diff into the viewport, marking
// the focused hunk so the user can see which one a/r/x will act on.
func (dr *diffReviewState) syncDiffVP() {
	if dr == nil || len(dr.entries) == 0 {
		return
	}
	e := dr.entries[dr.fileIdx]
	rendered, focusedTopLine := renderHunkDiff(e, dr.hunkIdx, dr.diffVP.Width())
	dr.diffVP.SetContent(rendered)
	// Auto-scroll so the focused hunk is visible.
	if focusedTopLine >= 0 {
		dr.diffVP.SetYOffset(focusedTopLine)
	} else {
		dr.diffVP.GotoTop()
	}
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

// currentHunk returns a pointer to the focused hunkReview, or nil if the
// focused file has no hunks.
func (dr *diffReviewState) currentHunk() *hunkReview {
	if dr.fileIdx < 0 || dr.fileIdx >= len(dr.entries) {
		return nil
	}
	e := &dr.entries[dr.fileIdx]
	if dr.hunkIdx < 0 || dr.hunkIdx >= len(e.hunks) {
		return nil
	}
	return &e.hunks[dr.hunkIdx]
}

// advanceHunk moves the cursor to the next hunk, crossing file boundaries.
// Returns false when already at the last hunk of the last file.
func (dr *diffReviewState) advanceHunk() bool {
	if dr.fileIdx >= len(dr.entries) {
		return false
	}
	if dr.hunkIdx+1 < len(dr.entries[dr.fileIdx].hunks) {
		dr.hunkIdx++
		return true
	}
	for f := dr.fileIdx + 1; f < len(dr.entries); f++ {
		if len(dr.entries[f].hunks) > 0 {
			dr.fileIdx = f
			dr.hunkIdx = 0
			return true
		}
	}
	return false
}

// retreatHunk moves the cursor to the previous hunk, crossing file boundaries.
func (dr *diffReviewState) retreatHunk() bool {
	if dr.hunkIdx > 0 {
		dr.hunkIdx--
		return true
	}
	for f := dr.fileIdx - 1; f >= 0; f-- {
		if len(dr.entries[f].hunks) > 0 {
			dr.fileIdx = f
			dr.hunkIdx = len(dr.entries[f].hunks) - 1
			return true
		}
	}
	return false
}

// nextFile / prevFile jump file boundaries (`]` / `[`).
func (dr *diffReviewState) nextFile() {
	for f := dr.fileIdx + 1; f < len(dr.entries); f++ {
		if len(dr.entries[f].hunks) > 0 {
			dr.fileIdx = f
			dr.hunkIdx = 0
			return
		}
	}
}

func (dr *diffReviewState) prevFile() {
	for f := dr.fileIdx - 1; f >= 0; f-- {
		if len(dr.entries[f].hunks) > 0 {
			dr.fileIdx = f
			dr.hunkIdx = 0
			return
		}
	}
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
			for j := range dr.entries[i].hunks {
				if dr.entries[i].hunks[j].action == diffReviewPending {
					dr.entries[i].hunks[j].action = diffReviewApproved
				}
			}
		}
	}

	setAll := func(a diffReviewAction) {
		for i := range dr.entries {
			for j := range dr.entries[i].hunks {
				dr.entries[i].hunks[j].action = a
			}
		}
	}

	setHunk := func(a diffReviewAction) {
		if h := dr.currentHunk(); h != nil {
			h.action = a
		}
	}

	key := msg.String()

	// Note-mode: the user pressed 'n' to attach a note to the focused hunk.
	// All printable keys feed into noteInput; Enter commits, Esc cancels.
	if dr.noteMode {
		switch key {
		case "enter":
			if h := dr.currentHunk(); h != nil {
				h.note = dr.noteInput
			}
			dr.noteMode = false
			dr.noteInput = ""
		case "esc", "ctrl+c":
			dr.noteMode = false
			dr.noteInput = ""
		case "backspace":
			if len(dr.noteInput) > 0 {
				runes := []rune(dr.noteInput)
				dr.noteInput = string(runes[:len(runes)-1])
			}
		default:
			if len(key) == 1 {
				dr.noteInput += key
			}
		}
		m.diffReview = dr
		return m, nil
	}

	if key == "tab" || key == "shift+tab" {
		dr.diffFocus = !dr.diffFocus
		m.diffReview = dr
		return m, nil
	}

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

	switch key {
	case "up", "k":
		if dr.retreatHunk() {
			dr.syncDiffVP()
		}
	case "down", "j":
		if dr.advanceHunk() {
			dr.syncDiffVP()
		}
	case "]":
		dr.nextFile()
		dr.syncDiffVP()
	case "[":
		dr.prevFile()
		dr.syncDiffVP()
	case "a":
		setHunk(diffReviewApproved)
		if dr.advanceHunk() {
			dr.syncDiffVP()
		}
	case "r":
		setHunk(diffReviewRequested)
		if dr.advanceHunk() {
			dr.syncDiffVP()
		}
	case "x":
		setHunk(diffReviewReverted)
		if dr.advanceHunk() {
			dr.syncDiffVP()
		}
	case "n":
		// Open the note minibuffer for the focused hunk. The note is saved
		// on Enter and travels to the agent in the follow-up feedback message.
		if dr.currentHunk() != nil {
			dr.noteMode = true
			if h := dr.currentHunk(); h != nil {
				dr.noteInput = h.note // pre-fill with any existing note
			}
		}
	case "A":
		setAll(diffReviewApproved)
		return close(buildDiffReviewResult(dr))
	case "X":
		setAll(diffReviewReverted)
		return close(buildDiffReviewResult(dr))
	case "enter", "esc":
		pendingToApproved()
		return close(buildDiffReviewResult(dr))
	case "ctrl+c":
		for i := range dr.entries {
			for j := range dr.entries[i].hunks {
				if dr.entries[i].hunks[j].action == diffReviewPending {
					dr.entries[i].hunks[j].action = diffReviewReverted
				}
			}
		}
		return close(buildDiffReviewResult(dr))
	}

	m.diffReview = dr
	return m, nil
}

// buildDiffReviewResult maps per-hunk decisions into approved/requested entries.
// For each file we Apply only the approved hunks against the original content;
// rejected hunks fall back to the original. Files with zero approved hunks are
// omitted from Approved (no-op write avoided).
func buildDiffReviewResult(dr *diffReviewState) DiffReviewResult {
	var result DiffReviewResult
	var feedbackItems []agentFollowupItem
	for _, e := range dr.entries {
		if len(e.hunks) == 0 {
			continue
		}
		approvedMask := make([]bool, len(e.hunks))
		anyApproved := false
		anyRequested := false
		hunks := make([]pendingedits.Hunk, len(e.hunks))
		for i, hr := range e.hunks {
			hunks[i] = hr.hunk
			// Treat any still-pending hunk as approved (Enter/Esc fall-through).
			if hr.action == diffReviewApproved || hr.action == diffReviewPending {
				approvedMask[i] = true
				anyApproved = true
			}
			if hr.action == diffReviewRequested {
				anyRequested = true
				feedbackItems = append(feedbackItems, agentFollowupItem{
					path: e.entry.Path,
					hunk: hr.hunk,
					note: hr.note,
				})
			}
		}
		if anyApproved {
			rebuilt := pendingedits.Apply(
				e.entry.OrigContent, e.entry.NewContent,
				e.diffLines, hunks, approvedMask,
			)
			out := e.entry
			out.NewContent = rebuilt
			result.Approved = append(result.Approved, out)
		}
		if anyRequested {
			result.Requested = append(result.Requested, e.entry)
		}
	}
	if len(feedbackItems) > 0 {
		result.FollowupMessage = buildFollowupText(feedbackItems)
	}
	return result
}

// agentFollowupItem holds one rejected hunk's data for the follow-up message.
type agentFollowupItem struct {
	path string
	hunk pendingedits.Hunk
	note string
}

// buildFollowupText produces the user-facing follow-up message text from a
// list of rejected hunks. Kept in the tui package to avoid a circular import
// with internal/agent; the text format is identical to what
// agent.BuildDiffFeedbackMessage would produce.
func buildFollowupText(items []agentFollowupItem) string {
	var sb strings.Builder
	sb.WriteString("The following edits were rejected during diff review. Please address each before continuing:\n\n")
	sb.WriteString("<diff_feedback>\n")
	for _, item := range items {
		fmt.Fprintf(&sb, "  <hunk path=%q old_start=%d old_length=%d new_start=%d new_length=%d>\n",
			item.path, item.hunk.OldStart, item.hunk.OldLength, item.hunk.NewStart, item.hunk.NewLength)
		// Render the proposed diff lines.
		sb.WriteString("    <proposed>\n")
		for _, ln := range item.hunk.Lines {
			switch ln.Op {
			case pendingedits.DiffInsert:
				fmt.Fprintf(&sb, "      +%s\n", ln.Text)
			case pendingedits.DiffDelete:
				fmt.Fprintf(&sb, "      -%s\n", ln.Text)
			default:
				fmt.Fprintf(&sb, "       %s\n", ln.Text)
			}
		}
		sb.WriteString("    </proposed>\n")
		sb.WriteString("    <decision>rejected</decision>\n")
		if item.note != "" {
			fmt.Fprintf(&sb, "    <note>%s</note>\n", strings.TrimSpace(item.note))
		}
		sb.WriteString("  </hunk>\n")
	}
	sb.WriteString("</diff_feedback>")
	return sb.String()
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

	const fixedRows = 5
	contentH := innerH - fixedRows
	if contentH < 3 {
		contentH = 3
	}

	listW := innerW * 35 / 100
	if listW < 14 {
		listW = 14
	}
	diffW := innerW - listW - 3
	if diffW < 10 {
		diffW = 10
	}

	if dr.diffVP.Width() != diffW || dr.diffVP.Height() != contentH {
		dr.resizeDiffVP(diffW, contentH)
	}

	var sb strings.Builder

	totalHunks, decidedHunks := dr.tallyHunks()
	plural := "s"
	if totalHunks == 1 {
		plural = ""
	}
	fmt.Fprintf(&sb, "%s\n", panelHeader("Diff Review", innerW))
	fmt.Fprintf(&sb, "%s\n", stylePickerDesc.Render(
		fmt.Sprintf("  %d/%d hunk%s decided — a approve · r request · x revert · ] / [ next/prev file",
			decidedHunks, totalHunks, plural),
	))
	fmt.Fprintf(&sb, "%s\n", panelRule(innerW))

	fileListStr := renderHunkFileList(dr, listW, contentH)
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

	// Bottom row: note minibuffer when active, otherwise keybinding hint.
	if dr.noteMode {
		cursor := "█"
		notePrompt := fmt.Sprintf("note> %s%s", dr.noteInput, cursor)
		fmt.Fprintf(&sb, "%s", stylePickerItemSelected.Width(innerW).Render(diffTrimRunes(notePrompt, innerW)))
	} else {
		var focusHint string
		if dr.diffFocus {
			focusHint = "↑/↓ scroll diff · tab: hunks"
		} else {
			focusHint = "↑/↓ hunks · n note · tab: scroll diff"
		}
		hint := fmt.Sprintf("%s · A all · X revert all · Enter/Esc done · ^C abort", focusHint)
		fmt.Fprintf(&sb, "%s", stylePickerDesc.Width(innerW).Render(hint))
	}

	return panelFrameStyle(rectWidth, rectHeight).Render(sb.String())
}

// tallyHunks returns (total, decided) hunk counts across all files.
func (dr *diffReviewState) tallyHunks() (int, int) {
	total, decided := 0, 0
	for _, e := range dr.entries {
		for _, hr := range e.hunks {
			total++
			if hr.action != diffReviewPending {
				decided++
			}
		}
	}
	return total, decided
}

// renderHunkFileList renders the left-panel: each file with its hunk decision
// summary (e.g. "main.go (2/5)"). The currently focused file is highlighted.
func renderHunkFileList(dr *diffReviewState, width, height int) string {
	var sb strings.Builder
	for i, e := range dr.entries {
		if i >= height {
			break
		}
		approved, total := 0, len(e.hunks)
		for _, hr := range e.hunks {
			if hr.action == diffReviewApproved {
				approved++
			}
		}
		summary := fmt.Sprintf(" %d/%d", approved, total)
		nameWidth := width - len(summary) - 4
		if nameWidth < 4 {
			nameWidth = 4
		}
		name := diffShortPath(e.entry.Path, nameWidth)
		line := name + summary
		if i == dr.fileIdx {
			fmt.Fprintf(&sb, "%s\n", stylePickerItemSelected.Render("❯ "+diffTrimRunes(line, width-2)))
		} else {
			fmt.Fprintf(&sb, "%s\n", stylePickerItem.Render("  "+diffTrimRunes(line, width-2)))
		}
	}
	return sb.String()
}

// renderHunkDiff renders the focused entry's diff with a marker on the
// focused hunk. Returns (rendered string, top-line index of focused hunk in
// the rendered output) so the viewport can scroll to it.
func renderHunkDiff(e diffReviewEntry, focusedHunk, width int) (string, int) {
	if len(e.hunks) == 0 {
		return stylePickerDesc.Render("(no changes — file staged but identical to disk)"), -1
	}

	var sb strings.Builder
	focusedTop := -1
	currentLine := 0

	for i, hr := range e.hunks {
		// Hunk header line: "@@ -<oldStart>,<oldLen> +<newStart>,<newLen> @@  [action]"
		marker := "  "
		if i == focusedHunk {
			marker = "▶ "
			focusedTop = currentLine
		}
		header := fmt.Sprintf("%s@@ -%d,%d +%d,%d @@  [%s]",
			marker,
			hr.hunk.OldStart, hr.hunk.OldLength,
			hr.hunk.NewStart, hr.hunk.NewLength,
			hunkActionLabel(hr.action),
		)
		fmt.Fprintf(&sb, "%s\n", styleHunkHeader(hr.action, i == focusedHunk).Render(diffTrimRunes(header, width)))
		currentLine++

		for _, ln := range hr.hunk.Lines {
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
			currentLine++
		}
		if i < len(e.hunks)-1 {
			fmt.Fprintf(&sb, "%s\n", stylePickerDesc.Render(strings.Repeat("·", min(width, 20))))
			currentLine++
		}
	}
	return strings.TrimRight(sb.String(), "\n"), focusedTop
}

// hunkActionLabel returns a short tag for the per-hunk decision badge.
func hunkActionLabel(a diffReviewAction) string {
	switch a {
	case diffReviewApproved:
		return "approve"
	case diffReviewReverted:
		return "revert"
	case diffReviewRequested:
		return "request"
	default:
		return "pending"
	}
}

// styleHunkHeader returns a lipgloss style for the @@ header line. Focused
// hunks get the picker-selected style; unfocused fall back to the muted desc
// style. Action-specific colouring rides on the badge inside the line text
// rather than the whole row.
func styleHunkHeader(_ diffReviewAction, focused bool) lipgloss.Style {
	if focused {
		return stylePickerItemSelected
	}
	return stylePickerDesc
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
