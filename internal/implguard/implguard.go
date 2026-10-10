// Package implguard enforces the implement role: when a separate provider
// owns code changes, the main model may only edit files that provider has
// already touched.
package implguard

import (
	"path/filepath"
	"sort"
	"sync"
)

// Status is one evaluation of the guard's activation state.
type Status struct {
	// Active is true while the guard enforces.
	Active bool
	// Label names the implement provider in messages.
	Label string
	// Unreachable is true when an implement provider is configured but cannot
	// be reached right now. The guard is then inactive and the main model edits
	// files directly.
	Unreachable bool
}

// FallbackNotice is appended to the first file-edit result after the
// implementer becomes unreachable.
const FallbackNotice = "[implement role] The local implementer is unreachable, so the main model is editing files directly. LocalImplement is unavailable until it reconnects."

// Guard records the files the implementer wrote and answers whether the main
// model may edit a path. A nil *Guard is inactive and allows nothing to be
// recorded; callers consult Active before enforcing.
type Guard struct {
	status func() Status

	mu       sync.Mutex
	touched  map[string]struct{}
	down     bool // last evaluation found the implementer unreachable
	notified bool // the fallback notice was handed out for the current outage
}

// New returns a Guard. active reports whether the guard is enforcing right now
// and the label of the implement provider; nil means never active.
func New(active func() (bool, string)) *Guard {
	if active == nil {
		return NewStatus(nil)
	}
	return NewStatus(func() Status {
		on, label := active()
		return Status{Active: on, Label: label}
	})
}

// NewStatus returns a Guard driven by a status function that can also report
// an unreachable implementer; nil means never active.
func NewStatus(status func() Status) *Guard {
	return &Guard{status: status, touched: map[string]struct{}{}}
}

// Active reports whether the guard is enforcing, and the implement provider's
// label for messages. Every call re-evaluates, so an implementer that drops or
// returns mid-session changes the answer immediately.
func (g *Guard) Active() (bool, string) {
	if g == nil || g.status == nil {
		return false, ""
	}
	st := g.status()
	g.mu.Lock()
	g.down = st.Unreachable
	if !st.Unreachable {
		g.notified = false
	}
	g.mu.Unlock()
	return st.Active, st.Label
}

// TakeFallbackNotice returns FallbackNotice once per outage, as seen by the
// latest Active call, and "" while the implementer is reachable or after the
// notice was already given.
func (g *Guard) TakeFallbackNotice() string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.down || g.notified {
		return ""
	}
	g.notified = true
	return FallbackNotice
}

// Record marks absolute paths as written by the implementer.
func (g *Guard) Record(paths ...string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range paths {
		if p == "" {
			continue
		}
		g.touched[normalize(p)] = struct{}{}
	}
}

// Allowed reports whether path was recorded as touched by the implementer.
func (g *Guard) Allowed(path string) bool {
	if g == nil || path == "" {
		return false
	}
	n := normalize(path)
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.touched[n]
	return ok
}

// Touched returns the recorded paths, sorted.
func (g *Guard) Touched() []string {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.touched))
	for p := range g.touched {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// normalize makes a path comparable: absolute, cleaned, and with symlinks
// resolved on the file, or on its parent directory when the file does not
// exist yet (so macOS /var and /private/var compare equal).
func normalize(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r
	}
	dir, base := filepath.Split(abs)
	if r, err := filepath.EvalSymlinks(filepath.Clean(dir)); err == nil {
		return filepath.Join(r, base)
	}
	return abs
}
