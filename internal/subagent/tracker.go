// Package subagent tracks active sub-agent loops for TUI display.
package subagent

import (
	"sync"
	"time"

	"github.com/icehunter/conduit/internal/permissions"
)

// Entry describes one running sub-agent.
type Entry struct {
	ID        string
	Label     string // short description for the working row
	Mode      permissions.Mode
	StartedAt time.Time
}

// Tracker is a thread-safe registry of running sub-agents.
type Tracker struct {
	mu      sync.RWMutex
	entries []Entry
}

// Default is the process-wide sub-agent tracker.
var Default = &Tracker{}

// Add registers a new sub-agent entry.
func (t *Tracker) Add(e Entry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries = append(t.entries, e)
}

// UpdateMode updates the mode for the entry with the given ID.
func (t *Tracker) UpdateMode(id string, mode permissions.Mode) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.entries {
		if t.entries[i].ID == id {
			t.entries[i].Mode = mode
			return
		}
	}
}

// Remove removes the entry with the given ID.
func (t *Tracker) Remove(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.entries[:0]
	for _, e := range t.entries {
		if e.ID != id {
			out = append(out, e)
		}
	}
	t.entries = out
}

// Snapshot returns a point-in-time copy of all active entries.
func (t *Tracker) Snapshot() []Entry {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if len(t.entries) == 0 {
		return nil
	}
	out := make([]Entry, len(t.entries))
	copy(out, t.entries)
	return out
}
