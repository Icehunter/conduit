package pendingedits

import (
	"sort"
	"sync"
	"time"
)

// Table is a session-scoped staging area. Methods are safe for concurrent use.
type Table struct {
	mu      sync.Mutex
	entries map[string]*Entry
}

// NewTable returns an empty staging table.
func NewTable() *Table {
	return &Table{entries: make(map[string]*Entry)}
}

// Stage records a pending change for e.Path.
//
// Composite-merge semantics: if a pending entry already exists for this path,
// only NewContent and the staging timestamp are updated. OrigContent and
// OrigExisted are preserved from the first stage so the diff shown to the
// user is always (disk → final), never (intermediate → final).
//
// Stage takes ownership of e.OrigContent and e.NewContent — callers must not
// mutate the slices after the call.
func (t *Table) Stage(e Entry) error {
	if e.Path == "" {
		return errEmptyPath
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if e.StagedAt.IsZero() {
		e.StagedAt = time.Now()
	}
	if existing, ok := t.entries[e.Path]; ok {
		existing.NewContent = e.NewContent
		existing.StagedAt = e.StagedAt
		// Op promotes from edit → write if a Write tool clobbers a previously
		// staged Edit. The composite is observably a full overwrite.
		if e.Op == OpWrite {
			existing.Op = OpWrite
		}
		// ToolName tracks the most recent stager so JSONL records reflect
		// what last touched the path.
		if e.ToolName != "" {
			existing.ToolName = e.ToolName
		}
		return nil
	}
	cp := e
	t.entries[e.Path] = &cp
	return nil
}

// Len returns the number of pending entries.
func (t *Table) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.entries)
}

// Drain returns all pending entries (sorted by path for stable display) and
// clears the table. The returned slice contains copies; callers may mutate
// them freely.
func (t *Table) Drain() []Entry {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Entry, 0, len(t.entries))
	for _, e := range t.entries {
		out = append(out, *e)
	}
	t.entries = make(map[string]*Entry)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Get returns the pending entry for path (zero value, false if absent).
// Useful for tests; production callers should use Drain.
func (t *Table) Get(path string) (Entry, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.entries[path]; ok {
		return *e, true
	}
	return Entry{}, false
}

// Discard removes the entry for path without writing it. Returns true if an
// entry was removed.
func (t *Table) Discard(path string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.entries[path]; !ok {
		return false
	}
	delete(t.entries, path)
	return true
}

// Clear discards every pending entry.
func (t *Table) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries = make(map[string]*Entry)
}
