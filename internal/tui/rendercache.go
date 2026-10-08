package tui

import (
	"sync"
	"sync/atomic"
)

// styleGeneration increments on every RebuildStyles call so cached renders
// from a previous theme are never reused.
var styleGeneration atomic.Uint64

// renderKey is everything a rendered transcript row depends on. Message is
// comparable, so a changed field (streamed content, expanded state, tool
// result) naturally misses the cache.
type renderKey struct {
	msg     Message
	width   int
	verbose bool
	prefix  string // tree prefix; empty for plain messages
	isLast  bool
	gen     uint64
	cwd     string // displayed Bash commands drop a redundant `cd <cwd> &&`
}

type renderEntry struct {
	key renderKey
	out string
}

// renderCache memoizes rendered transcript rows by message index. Streamed
// tokens only change the tail, so earlier rows are served from here instead of
// being re-rendered (markdown + highlighting) on every token. It is shared by
// pointer across Model copies.
type renderCache struct {
	mu      sync.Mutex
	entries []renderEntry
	hits    int
	misses  int
}

func newRenderCache() *renderCache { return &renderCache{} }

func (c *renderCache) get(idx int, key renderKey, render func() string) string {
	if c == nil {
		return render()
	}
	c.mu.Lock()
	if idx < len(c.entries) && c.entries[idx].key == key {
		out := c.entries[idx].out
		c.hits++
		c.mu.Unlock()
		return out
	}
	c.misses++
	c.mu.Unlock()
	out := render()
	c.mu.Lock()
	for len(c.entries) <= idx {
		c.entries = append(c.entries, renderEntry{})
	}
	c.entries[idx] = renderEntry{key: key, out: out}
	c.mu.Unlock()
	return out
}

// trim drops entries beyond n messages (after /clear or compaction).
func (c *renderCache) trim(n int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if len(c.entries) > n {
		clear(c.entries[n:])
		c.entries = c.entries[:n]
	}
	c.mu.Unlock()
}
