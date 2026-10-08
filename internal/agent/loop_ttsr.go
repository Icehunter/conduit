package agent

import (
	"fmt"

	"github.com/icehunter/conduit/internal/ttsr"
)

// newTTSRWatcher builds the watcher for one Run, or nil when there is nothing
// to watch for (no rules and the repetition guard is off). guard is
// cfg.RepetitionGuard; nil means on.
func (l *Loop) newTTSRWatcher(rules []ttsr.Rule, guard func() bool) *ttsr.Watcher {
	repetition := guard == nil || guard()
	if len(rules) == 0 && !repetition {
		return nil
	}
	opts := []ttsr.Option{ttsr.WithMemory(&l.ttsrMem), ttsr.WithAgent(l.ttsrAgentName())}
	if repetition {
		opts = append(opts, ttsr.WithRepetitionGuard())
	}
	return ttsr.NewWatcher(rules, opts...)
}

// ttsrAgentName is the name TTSR rules scope themselves to with `agents:`:
// "main" on the root loop, the agent type on a sub-agent.
func (l *Loop) ttsrAgentName() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	switch {
	case l.agentID == "":
		return "main"
	case l.agentType != "":
		return l.agentType
	default:
		return "subagent"
	}
}

// deliverTTSRWarnings queues the reminders of warn-mode rules that matched in
// the response just received, so the model sees them with its next request.
// Called only when another request follows (the response made tool calls).
func (l *Loop) deliverTTSRWarnings(w *ttsr.Watcher) {
	for _, r := range w.TakeWarnings() {
		l.InjectMessage(fmt.Sprintf("<system-reminder>\n%s\n</system-reminder>", r.Correction))
	}
}
