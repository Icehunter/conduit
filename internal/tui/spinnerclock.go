package tui

import (
	"github.com/icehunter/conduit/internal/subagent"
	"github.com/icehunter/conduit/internal/workflow"
)

// needsAnimation reports whether anything on screen changes over time: the
// main turn, a running sub-agent (its panel and elapsed counters), or a
// running workflow. The animation clock ticks only while this is true.
// Background shells are not included: the footer's shell count is static, so
// ticking for the life of a job would only redraw.
func (m Model) needsAnimation() bool {
	return m.running || workflow.Default.Running() > 0 || len(subagent.Default.Snapshot()) > 0
}
