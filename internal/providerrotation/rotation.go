// Package providerrotation tracks per-provider cooldowns for the current
// process lifetime so the agent loop can rotate to the next available provider
// when one returns a rate-limit or overload error.
//
// Cooldowns are NOT persisted to disk — they reset on binary restart, which
// is intentional: stale cooldowns from a previous run would be surprising.
package providerrotation

import (
	"sync"
	"time"

	"github.com/icehunter/conduit/internal/settings"
)

// State tracks per-provider cooldown expiries.
type State struct {
	mu        sync.Mutex
	cooldowns map[string]time.Time // provider key → cooldown expiry
}

// New returns a fresh rotation State.
func New() *State {
	return &State{cooldowns: make(map[string]time.Time)}
}

// Penalize marks a provider key as unavailable until 'until'.
// Subsequent calls for the same key extend the cooldown only if 'until' is later.
func (s *State) Penalize(key string, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.cooldowns[key]; !ok || until.After(existing) {
		s.cooldowns[key] = until
	}
}

// IsOnCooldown reports whether 'key' is currently penalized.
func (s *State) IsOnCooldown(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	expiry, ok := s.cooldowns[key]
	return ok && now.Before(expiry)
}

// NextAvailable returns the first provider from chain that is NOT on cooldown,
// starting after the provider whose key equals currentKey.
//
// If currentKey is not found in chain, the search starts from index 0.
// Returns the provider and true if one is found; returns the zero value and
// false if all remaining providers are on cooldown or chain has only one entry.
func (s *State) NextAvailable(chain []settings.ActiveProviderSettings, currentKey string, now time.Time) (settings.ActiveProviderSettings, bool) {
	n := len(chain)
	if n <= 1 {
		return settings.ActiveProviderSettings{}, false
	}
	startIdx := 0
	for i, p := range chain {
		if settings.ProviderKey(p) == currentKey {
			startIdx = i
			break
		}
	}
	for offset := range n - 1 {
		idx := (startIdx + 1 + offset) % n
		p := chain[idx]
		if !s.IsOnCooldown(settings.ProviderKey(p), now) {
			return p, true
		}
	}
	return settings.ActiveProviderSettings{}, false
}
