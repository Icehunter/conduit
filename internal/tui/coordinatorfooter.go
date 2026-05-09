package tui

import (
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/tools/tasktool"
)

// renderCoordinatorPanel renders a footer row per active sub-agent task
// (in_progress) so the user can see what background work is running.
// Mirrors src/components/CoordinatorAgentStatus.tsx CoordinatorTaskPanel
// trimmed to a static one-line-per-task layout. Empty when no tasks
// are in_progress.
func renderCoordinatorPanel(width int) string {
	if width < 20 {
		return ""
	}
	var active []*tasktool.Task
	for _, t := range tasktool.GlobalStore().List() {
		if t.Status == tasktool.StatusInProgress {
			active = append(active, t)
		}
	}
	if len(active) == 0 {
		return ""
	}
	pad := strings.Repeat(" ", outerPad)
	var sb strings.Builder
	for i, t := range active {
		label := t.ActiveForm
		if label == "" {
			label = t.Subject
		}
		elapsed := time.Since(t.CreatedAt).Round(time.Second)
		// Truncate label so [elapsed] fits without wrapping.
		const tailMax = 12 // " · 999s"-ish
		labelMax := max(width-outerPad*2-4-tailMax, 10)
		runes := []rune(label)
		if len(runes) > labelMax {
			label = string(runes[:labelMax-1]) + "…"
		}
		line := pad + styleStatusAccent.Render("⚙ ") + styleStatus.Render(label) + " " + stylePickerDesc.Render("· "+elapsed.String())
		sb.WriteString(line)
		if i < len(active)-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
