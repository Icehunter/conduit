// Package subagent tracks active sub-agent loops for TUI display.
package subagent

import (
	"sync"
	"time"

	"github.com/icehunter/conduit/internal/permissions"
)

// ToolEvent captures one tool call within a running sub-agent.
type ToolEvent struct {
	ToolID    string
	ToolName  string
	ToolInput string // raw JSON input
	Status    string // "running", "done", "failed"
	StartedAt time.Time
	Duration  time.Duration
}

// Entry describes one sub-agent — either currently running or recently completed.
type Entry struct {
	ID         string
	Label      string // short description for the working row
	Mode       permissions.Mode
	StartedAt  time.Time
	DoneAt     time.Time // zero while running; set when Remove is called
	Background bool      // true for system-initiated agents (memory, hooks); hide from panel
}

// IsRunning reports whether the sub-agent is still active.
func (e Entry) IsRunning() bool { return e.DoneAt.IsZero() }

const maxCompleted = 5 // how many completed entries to keep for drill-in

// Tracker is a thread-safe registry of running sub-agents.
type Tracker struct {
	mu        sync.RWMutex
	active    []Entry                // currently running
	completed []Entry                // ring buffer of recently finished (newest last)
	events    map[string][]ToolEvent // keyed by entry ID
}

// Default is the process-wide sub-agent tracker.
var Default = &Tracker{}

// Add registers a new sub-agent entry.
func (t *Tracker) Add(e Entry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active = append(t.active, e)
}

// UpdateMode updates the mode for the running entry with the given ID.
func (t *Tracker) UpdateMode(id string, mode permissions.Mode) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.active {
		if t.active[i].ID == id {
			t.active[i].Mode = mode
			return
		}
	}
}

// Remove moves the entry to the completed ring buffer (retains events for drill-in).
func (t *Tracker) Remove(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.active[:0]
	for _, e := range t.active {
		if e.ID == id {
			e.DoneAt = time.Now()
			t.completed = append(t.completed, e)
			if len(t.completed) > maxCompleted {
				// evict the oldest, but also drop its events
				evicted := t.completed[0]
				delete(t.events, evicted.ID)
				t.completed = t.completed[1:]
			}
		} else {
			out = append(out, e)
		}
	}
	t.active = out
}

// AppendEvent records a new tool event for the sub-agent with the given ID.
func (t *Tracker) AppendEvent(id string, ev ToolEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.events == nil {
		t.events = make(map[string][]ToolEvent)
	}
	t.events[id] = append(t.events[id], ev)
}

// UpdateEvent marks a tool event as completed or failed.
func (t *Tracker) UpdateEvent(id, toolID string, isError bool, duration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	evs := t.events[id]
	status := "done"
	if isError {
		status = "failed"
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].ToolID == toolID {
			evs[i].Status = status
			evs[i].Duration = duration
			return
		}
	}
}

// GetEvents returns a snapshot of tool events for the sub-agent with the given ID.
func (t *Tracker) GetEvents(id string) []ToolEvent {
	t.mu.RLock()
	defer t.mu.RUnlock()
	src := t.events[id]
	if len(src) == 0 {
		return nil
	}
	out := make([]ToolEvent, len(src))
	copy(out, src)
	return out
}

// Snapshot returns running user-visible entries (Background entries excluded).
// Used by the working-row badge renderer.
func (t *Tracker) Snapshot() []Entry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]Entry, 0, len(t.active))
	for _, e := range t.active {
		if !e.Background {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SnapshotAll returns running and recently completed user-visible entries
// (Background entries are excluded). Newest-last within each group.
func (t *Tracker) SnapshotAll() []Entry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]Entry, 0, len(t.active)+len(t.completed))
	for _, e := range t.completed {
		if !e.Background {
			out = append(out, e)
		}
	}
	for _, e := range t.active {
		if !e.Background {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
