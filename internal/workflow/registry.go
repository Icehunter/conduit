package workflow

import (
	"sort"
	"sync"
	"sync/atomic"
)

// Ultracode is the standing opt-in flag toggled by /ultracode. When set,
// every user turn carries the session reminder and the Workflow tool
// description tells the model to author workflows by default.
var Ultracode atomic.Bool

// Registry tracks running and recently completed runs for the UI and the
// Workflow tool's status/kill/result operations.
type Registry struct {
	mu   sync.RWMutex
	runs map[string]*Run
	// order is insertion order so eviction drops the oldest completed run.
	order []string
	keep  int
}

// Default is the process-wide run registry.
var Default = NewRegistry(20)

// NewRegistry returns a registry that retains at most keep completed runs.
func NewRegistry(keep int) *Registry {
	return &Registry{runs: map[string]*Run{}, keep: keep}
}

// Add registers a run.
func (g *Registry) Add(r *Run) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.runs[r.ID] = r
	g.order = append(g.order, r.ID)
	g.evictLocked()
}

// Get returns the run with id, or nil.
func (g *Registry) Get(id string) *Run {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.runs[id]
}

// Snapshot returns every retained run, running first (newest first), then
// completed (most recently finished first).
func (g *Registry) Snapshot() []Snapshot {
	g.mu.RLock()
	out := make([]Snapshot, 0, len(g.runs))
	for _, r := range g.runs {
		out = append(out, r.Snapshot())
	}
	g.mu.RUnlock()
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := out[i].Status == RunRunning, out[j].Status == RunRunning
		if ri != rj {
			return ri
		}
		if ri {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].DoneAt.After(out[j].DoneAt)
	})
	return out
}

// Running reports how many runs are in flight.
func (g *Registry) Running() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	n := 0
	for _, r := range g.runs {
		if r.Status() == RunRunning {
			n++
		}
	}
	return n
}

// KillAll cancels every running run (session shutdown).
func (g *Registry) KillAll() {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for _, r := range g.runs {
		r.Kill()
	}
}

// Reset drops all runs (tests).
func (g *Registry) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.runs = map[string]*Run{}
	g.order = nil
}

func (g *Registry) evictLocked() {
	completed := 0
	for _, id := range g.order {
		if g.runs[id].Status() != RunRunning {
			completed++
		}
	}
	for i := 0; i < len(g.order) && completed > g.keep; i++ {
		id := g.order[i]
		if g.runs[id].Status() == RunRunning {
			continue
		}
		delete(g.runs, id)
		g.order = append(g.order[:i], g.order[i+1:]...)
		i--
		completed--
	}
}
