// Package implguard enforces the implement role: when a separate provider
// owns code changes, the main model may only edit files that provider has
// already touched.
package implguard

import (
	"path/filepath"
	"sort"
	"sync"
)

// Guard records the files the implementer wrote and answers whether the main
// model may edit a path. A nil *Guard is inactive and allows nothing to be
// recorded; callers consult Active before enforcing.
type Guard struct {
	active func() (bool, string)

	mu      sync.Mutex
	touched map[string]struct{}
}

// New returns a Guard. active reports whether the guard is enforcing right now
// and the label of the implement provider; nil means never active.
func New(active func() (bool, string)) *Guard {
	return &Guard{active: active, touched: map[string]struct{}{}}
}

// Active reports whether the guard is enforcing, and the implement provider's
// label for messages.
func (g *Guard) Active() (bool, string) {
	if g == nil || g.active == nil {
		return false, ""
	}
	return g.active()
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
