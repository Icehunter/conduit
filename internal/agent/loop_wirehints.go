package agent

import (
	"github.com/icehunter/conduit/internal/api"
)

// Request classes for x-claude-code-request-class (CC 2.1.292 rs()/Nls()).
const (
	requestClassMain      = "main"
	requestClassSubagent  = "subagent"
	requestClassWorkflow  = "workflow"
	requestClassAuxiliary = "auxiliary"
)

// wireHints builds the client-hint headers for this loop's next request.
// The root loop sends class "main" plus contextCompacted (the kind taken by
// takeContextCompacted for this prompt, or ""); children send their own
// identity and never the compaction flag. userTurn says the request's trailing
// message came from the user (see api.WireHints.UserTurn); only the root loop
// reports it.
func (l *Loop) wireHints(promptID, prevToolDurations, contextCompacted string, userTurn bool) *api.WireHints {
	l.mu.RLock()
	defer l.mu.RUnlock()
	h := &api.WireHints{
		RequestClass:      l.requestClass,
		AgentType:         l.agentType,
		AgentID:           l.agentID,
		ParentAgentID:     l.parentAgentID,
		PromptID:          promptID,
		PrevToolDurations: prevToolDurations,
		SessionID:         l.cfg.SessionID,
	}
	if h.RequestClass == "" {
		h.RequestClass = requestClassMain
		h.ContextCompacted = contextCompacted
		h.UserTurn = userTurn
	}
	return h
}

// MarkContextCompacted flags the next main-loop prompt's first request as the
// first after a compaction of the given kind ("auto" or "manual"). Ignored on
// sub-agents, which CC never marks.
func (l *Loop) MarkContextCompacted(kind string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.requestClass == "" {
		l.pendingContextCompacted = kind
		// Read state is kept across compaction: the tracker's hash check
		// already guarantees the file still matches what was read.
	}
}

// ForgetContextCompacted drops a pending compaction flag. Call it when the
// conversation is replaced (/clear, /rewind; SetSessionID does it on a
// session switch), as CC does, so a fresh conversation isn't marked compacted.
func (l *Loop) ForgetContextCompacted() {
	l.resetReadState()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.pendingContextCompacted = ""
}

// takeContextCompacted returns the pending compaction kind and clears it.
func (l *Loop) takeContextCompacted() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	kind := l.pendingContextCompacted
	l.pendingContextCompacted = ""
	return kind
}

// toolDurations returns the timings of the tools that actually ran, in
// tool_use order.
func toolDurations(tasks []toolTask, results []toolResult) []api.ToolDuration {
	var durations []api.ToolDuration
	for i, task := range tasks {
		if task.denied || task.tool == nil {
			continue
		}
		durations = append(durations, api.ToolDuration{Name: task.block.Name, Duration: results[i].elapsed})
	}
	return durations
}

// setWireIdentity sets a child loop's hint identity. Call before the child runs.
func (l *Loop) setWireIdentity(agentID, parentAgentID, agentType, class string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.agentID = agentID
	l.parentAgentID = parentAgentID
	l.agentType = agentType
	l.requestClass = class
}

// wireAgentID returns this loop's agent id ("" on the root loop).
func (l *Loop) wireAgentID() string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.agentID
}
