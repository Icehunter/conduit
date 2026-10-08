package kernel

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const idleTimeout = 10 * time.Minute

// Manager owns all active kernels across sessions.
// Thread-safe. One Manager per process lifetime (singleton in mainrepl).
type Manager struct {
	mu      sync.Mutex
	kernels map[string]*entry // key: sessionID+":"+lang
}

type entry struct {
	k        *Kernel
	lastUsed time.Time
}

// NewManager creates a new Manager.
func NewManager() *Manager {
	return &Manager{
		kernels: make(map[string]*entry),
	}
}

// Get returns the Kernel for (sessionID, lang), creating one if needed.
// Returns an error if the interpreter is not available.
func (m *Manager) Get(sessionID, lang string) (*Kernel, error) {
	return m.GetIn(sessionID, lang, "")
}

// GetIn is Get with the working directory a newly created kernel starts in
// (empty = the process directory). An existing kernel keeps its directory.
func (m *Manager) GetIn(sessionID, lang, dir string) (*Kernel, error) {
	key := sessionID + ":" + lang
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.kernels[key]; ok {
		e.lastUsed = time.Now()
		return e.k, nil
	}
	k, err := NewIn(lang, dir)
	if err != nil {
		return nil, fmt.Errorf("kernel: manager get(%s, %s): %w", sessionID, lang, err)
	}
	m.kernels[key] = &entry{k: k, lastUsed: time.Now()}
	return k, nil
}

// DisposeSession closes all kernels for sessionID, including those of its
// scoped sub-agent sessions ("<id>/<scope>"), and removes them from the map.
func (m *Manager) DisposeSession(sessionID string) {
	prefix := sessionID + ":"
	scoped := sessionID + "/"
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, e := range m.kernels {
		if strings.HasPrefix(key, prefix) || strings.HasPrefix(key, scoped) {
			_ = e.k.Close()
			delete(m.kernels, key)
		}
	}
}

// Reap closes kernels that have been idle longer than idleTimeout.
// Call from a background goroutine (e.g. time.Ticker). now is injected
// so tests can control time without sleeping.
func (m *Manager) Reap(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, e := range m.kernels {
		if now.Sub(e.lastUsed) > idleTimeout {
			_ = e.k.Close()
			delete(m.kernels, key)
		}
	}
}
