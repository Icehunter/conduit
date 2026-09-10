package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/workflow"
)

// workflowPanelState is the /workflows overlay: a list of runs floated above
// the input, and a full-panel detail view with the phase tree and narrator
// log. nil means closed.
type workflowPanelState struct {
	runIDs   []string
	selected int
	scroll   int
	view     string // "list" | "detail"
}

const workflowPanelListMax = 8

type workflowPanelRefreshMsg struct{}

func tickWorkflowPanel() tea.Cmd {
	return tea.Tick(time.Second, func(_ time.Time) tea.Msg { return workflowPanelRefreshMsg{} })
}

func workflowRunIDs() []string {
	snaps := workflow.Default.Snapshot()
	ids := make([]string, len(snaps))
	for i, s := range snaps {
		ids[i] = s.ID
	}
	return ids
}

func workflowSnapshotByID(id string) (workflow.Snapshot, bool) {
	for _, s := range workflow.Default.Snapshot() {
		if s.ID == id {
			return s, true
		}
	}
	return workflow.Snapshot{}, false
}

// openWorkflowPanel opens the list view.
func (m Model) openWorkflowPanel() Model {
	m.workflowPanel = &workflowPanelState{runIDs: workflowRunIDs(), view: "list"}
	return m
}

func (m Model) handleWorkflowPanelKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.workflowPanel
	p.runIDs = workflowRunIDs()
	if p.selected >= len(p.runIDs) {
		p.selected = max(len(p.runIDs)-1, 0)
	}
	key := msg.String()
	if p.view == "detail" {
		switch key {
		case "esc", "ctrl+c":
			m.workflowPanel = nil
			return m, nil
		case "b", "backspace":
			p.scroll = 0
			p.view = "list"
		case "up", "k":
			if p.scroll > 0 {
				p.scroll--
			}
		case "down", "j":
			p.scroll++
		case "g":
			p.scroll = 0
		case "x", "ctrl+x":
			return m.killSelectedWorkflow()
		case "s":
			return m.saveSelectedWorkflow()
		}
		m.workflowPanel = p
		return m, tickWorkflowPanel()
	}
	switch key {
	case "esc", "ctrl+c", "q":
		m.workflowPanel = nil
		return m, nil
	case "up", "k":
		if p.selected > 0 {
			p.selected--
		}
	case "down", "j":
		if p.selected < len(p.runIDs)-1 {
			p.selected++
		}
	case "enter":
		if len(p.runIDs) > 0 {
			p.scroll = 0
			p.view = "detail"
		}
	case "x", "ctrl+x":
		return m.killSelectedWorkflow()
	case "s":
		return m.saveSelectedWorkflow()
	}
	m.workflowPanel = p
	return m, tickWorkflowPanel()
}

func (m Model) selectedWorkflow() (workflow.Snapshot, bool) {
	p := m.workflowPanel
	if p == nil || p.selected >= len(p.runIDs) {
		return workflow.Snapshot{}, false
	}
	return workflowSnapshotByID(p.runIDs[p.selected])
}

func (m Model) killSelectedWorkflow() (Model, tea.Cmd) {
	s, ok := m.selectedWorkflow()
	switch {
	case !ok:
		m.flashMsg = "no workflow selected"
	case s.Status != workflow.RunRunning:
		m.flashMsg = "workflow is not running"
	default:
		if r := workflow.Default.Get(s.ID); r != nil {
			r.Kill()
			m.flashMsg = "killed workflow " + s.Name
		}
	}
	return m, tea.Batch(tickWorkflowPanel(), tea.Tick(2*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} }))
}

// saveSelectedWorkflow copies the run's script into ~/.conduit/workflows so it
// becomes a slash command in every project, and registers it immediately.
func (m Model) saveSelectedWorkflow() (Model, tea.Cmd) {
	s, ok := m.selectedWorkflow()
	if !ok || s.ScriptPath == "" {
		m.flashMsg = "no script to save"
		return m, tea.Batch(tickWorkflowPanel(), tea.Tick(2*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} }))
	}
	dest := filepath.Join(workflow.SavedDir(), s.Name+".js")
	if err := saveWorkflowScript(s.ScriptPath, dest); err != nil {
		m.flashMsg = "save failed: " + err.Error()
	} else {
		m.flashMsg = fmt.Sprintf("saved → /%s (%s)", s.Name, dest)
		if m.cfg.Commands != nil {
			cwd, _ := os.Getwd()
			commands.RegisterWorkflowCommands(m.cfg.Commands, cwd)
		}
	}
	return m, tea.Batch(tickWorkflowPanel(), tea.Tick(3*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} }))
}

func saveWorkflowScript(src, dest string) error {
	if filepath.Clean(src) == filepath.Clean(dest) {
		return nil
	}
	b, err := os.ReadFile(src) //nolint:gosec // run script persisted by this process
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, b, 0o644) //nolint:gosec // user's own workflows dir
}

// phaseProgress summarises node states per phase in meta order.
type phaseProgress struct {
	title                        string
	total, done, running, failed int
	cached, queued               int
	current                      bool
}

func workflowPhaseProgress(s workflow.Snapshot) []phaseProgress {
	idx := map[string]int{}
	var out []phaseProgress
	for _, p := range s.Phases {
		idx[p.Title] = len(out)
		out = append(out, phaseProgress{title: p.Title, current: p.Title == s.CurrentPhase && s.Status == workflow.RunRunning})
	}
	for _, n := range s.Nodes {
		i, ok := idx[n.Phase]
		if !ok {
			idx[n.Phase] = len(out)
			out = append(out, phaseProgress{title: n.Phase})
			i = len(out) - 1
		}
		pp := &out[i]
		pp.total++
		switch n.Status {
		case workflow.NodeDone:
			pp.done++
		case workflow.NodeRunning:
			pp.running++
		case workflow.NodeFailed:
			pp.failed++
		case workflow.NodeCached:
			pp.cached++
		case workflow.NodeQueued:
			pp.queued++
		}
	}
	return out
}

func workflowStatusTag(s workflow.Snapshot) string {
	switch s.Status {
	case workflow.RunRunning:
		return styleModeYellow.Render("● running")
	case workflow.RunDone:
		return styleModeGreen.Render("✓ done")
	case workflow.RunFailed:
		return styleErrorText.Render("✗ failed")
	case workflow.RunKilled:
		return stylePickerDesc.Render("■ killed")
	}
	return stylePickerDesc.Render(string(s.Status))
}

func workflowElapsed(s workflow.Snapshot) time.Duration {
	end := s.DoneAt
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(s.StartedAt).Round(time.Second)
}

func workflowPhaseLine(pp phaseProgress) string {
	settled := pp.done + pp.cached
	var sb strings.Builder
	if pp.current {
		sb.WriteString("▶ ")
	} else {
		sb.WriteString("  ")
	}
	fmt.Fprintf(&sb, "%s %d/%d", pp.title, settled, pp.total)
	if pp.running > 0 {
		fmt.Fprintf(&sb, " · %d running", pp.running)
	}
	if pp.queued > 0 {
		fmt.Fprintf(&sb, " · %d queued", pp.queued)
	}
	if pp.failed > 0 {
		fmt.Fprintf(&sb, " · %d failed", pp.failed)
	}
	if pp.cached > 0 {
		fmt.Fprintf(&sb, " · %d cached", pp.cached)
	}
	return sb.String()
}

// renderWorkflowList renders the compact run list floated above the input.
func (m Model) renderWorkflowList() string {
	p := m.workflowPanel
	if p == nil || p.view != "list" {
		return ""
	}
	contentW := floatingInnerWidth(m.width, floatingPickerSpec)
	var sb strings.Builder
	sb.WriteString(panelHeader("Workflows", contentW) + "\n\n")
	if len(p.runIDs) == 0 {
		sb.WriteString(stylePickerDesc.Render("  no workflow runs yet — ask for one with \"ultracode\" or run a saved /<workflow>") + "\n")
	}
	start := 0
	if p.selected >= workflowPanelListMax {
		start = p.selected - workflowPanelListMax + 1
	}
	end := min(start+workflowPanelListMax, len(p.runIDs))
	for i := start; i < end; i++ {
		s, ok := workflowSnapshotByID(p.runIDs[i])
		if !ok {
			continue
		}
		cursor := "  "
		if i == p.selected {
			cursor = styleStatusAccent.Render("▸ ")
		}
		nodes := len(s.Nodes)
		var settled int
		for _, n := range s.Nodes {
			if n.Status == workflow.NodeDone || n.Status == workflow.NodeCached {
				settled++
			}
		}
		meta := fmt.Sprintf("%d/%d agents · %s · %s", settled, nodes, formatTokenCount(s.OutputTokens)+" out", workflowElapsed(s))
		name := truncatePlainToWidth(s.Name, max(contentW-lipgloss.Width(meta)-16, 8))
		fmt.Fprintf(&sb, "%s%s  %s  %s\n", cursor, name, workflowStatusTag(s), stylePickerDesc.Render(meta))
		if s.Status == workflow.RunRunning && s.CurrentPhase != "" {
			fmt.Fprintf(&sb, "    %s\n", stylePickerDesc.Render("phase: "+s.CurrentPhase))
		}
	}
	if len(p.runIDs) > workflowPanelListMax {
		fmt.Fprintf(&sb, "  %s\n", stylePickerDesc.Render(fmt.Sprintf("%d–%d of %d", start+1, end, len(p.runIDs))))
	}
	sb.WriteString("\n" + stylePickerDesc.Render("Enter details · x kill · s save as command · Esc close"))
	return sb.String()
}

// renderWorkflowDetail renders the full-panel phase tree + log for the
// selected run.
func (m Model) renderWorkflowDetail() string {
	p := m.workflowPanel
	if p == nil || p.view != "detail" {
		return ""
	}
	s, ok := m.selectedWorkflow()
	w := max(m.width, 20)
	innerW := w - 8
	var sb strings.Builder
	if !ok {
		sb.WriteString(panelHeader("Workflow", innerW) + "\n\n" + stylePickerDesc.Render("  run is gone") + "\n")
		return panelFrameStyle(w, m.panelH).Render(sb.String())
	}
	tag := workflowStatusTag(s)
	titleW := max(innerW-lipgloss.Width(tag)-4, 8)
	sb.WriteString(panelHeader(truncatePlainToWidth(s.Name, titleW), titleW))
	fmt.Fprintf(&sb, "  %s\n", tag)
	fmt.Fprintf(&sb, "%s\n\n", stylePickerDesc.Render(fmt.Sprintf("run %s · %s · %s in / %s out", s.ID, workflowElapsed(s), formatTokenCount(s.InputTokens), formatTokenCount(s.OutputTokens))))

	phases := workflowPhaseProgress(s)
	for _, pp := range phases {
		line := workflowPhaseLine(pp)
		if pp.current {
			line = styleStatusAccent.Render(line)
		}
		sb.WriteString(truncatePlainToWidth(line, innerW) + "\n")
	}
	if len(phases) == 0 {
		sb.WriteString(stylePickerDesc.Render("  (no agents yet)") + "\n")
	}

	// Running nodes get their own rows so a stuck one is visible.
	var running []workflow.Node
	for _, n := range s.Nodes {
		if n.Status == workflow.NodeRunning {
			running = append(running, n)
		}
	}
	if len(running) > 0 {
		sb.WriteString("\n")
		shown := running
		if len(shown) > 6 {
			shown = shown[:6]
		}
		for _, n := range shown {
			el := time.Since(n.StartedAt).Round(time.Second)
			sb.WriteString(truncatePlainToWidth(fmt.Sprintf("  ↳ %s  %s", n.Label, stylePickerDesc.Render(el.String())), innerW) + "\n")
		}
		if len(running) > len(shown) {
			fmt.Fprintf(&sb, "  %s\n", stylePickerDesc.Render(fmt.Sprintf("… %d more running", len(running)-len(shown))))
		}
	}

	// Narrator log fills the rest; scroll is an offset from the tail.
	used := strings.Count(sb.String(), "\n") + 6
	avail := max(m.panelH-used, 3)
	logs := s.Logs
	end := len(logs) - p.scroll
	end = max(min(end, len(logs)), 0)
	start := max(end-avail, 0)
	sb.WriteString("\n")
	if len(logs) == 0 {
		sb.WriteString(stylePickerDesc.Render("  (no log lines)") + "\n")
	}
	for _, l := range logs[start:end] {
		sb.WriteString(truncatePlainToWidth("  "+l.Text, innerW) + "\n")
	}
	if s.Err != "" {
		sb.WriteString(truncatePlainToWidth(styleErrorText.Render("  error: "+s.Err), innerW) + "\n")
	}
	hint := "B back · ↑↓ log · x kill · s save · Esc close"
	if s.Status == workflow.RunRunning {
		hint = "live · " + hint
	}
	fmt.Fprintf(&sb, "\n%s", stylePickerDesc.Render(hint))
	return panelFrameStyle(w, m.panelH).Render(sb.String())
}

// workflowWorkingLines returns extra working-row lines for live runs.
func workflowWorkingLines() []string {
	var out []string
	for _, s := range workflow.Default.Snapshot() {
		if s.Status != workflow.RunRunning {
			continue
		}
		var settled int
		for _, n := range s.Nodes {
			if n.Status == workflow.NodeDone || n.Status == workflow.NodeCached {
				settled++
			}
		}
		phase := s.CurrentPhase
		if phase == "" {
			phase = "starting"
		}
		out = append(out, fmt.Sprintf("  %s  %s",
			stylePickerDesc.Render(fmt.Sprintf("⟳ workflow %s · %s %d/%d", s.Name, phase, settled, len(s.Nodes))),
			stylePickerDesc.Render("/workflows to view")))
	}
	return out
}

func formatTokenCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%d", n)
}
