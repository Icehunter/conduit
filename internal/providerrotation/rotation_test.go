package providerrotation

import (
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/settings"
)

// makeProvider builds a minimal provider with the given key derivation fields.
func makeProvider(model string) settings.ActiveProviderSettings {
	return settings.ActiveProviderSettings{
		Kind:  settings.ProviderKindAnthropicAPI,
		Model: model,
	}
}

func TestNextAvailable_NoCooldowns(t *testing.T) {
	s := New()
	now := time.Now()
	p1 := makeProvider("model-1")
	p2 := makeProvider("model-2")
	p3 := makeProvider("model-3")
	chain := []settings.ActiveProviderSettings{p1, p2, p3}

	got, ok := s.NextAvailable(chain, settings.ProviderKey(p1), now)
	if !ok {
		t.Fatal("expected a provider, got none")
	}
	if got.Model != p2.Model {
		t.Errorf("NextAvailable() = %q, want %q", got.Model, p2.Model)
	}
}

func TestNextAvailable_FirstTwoCooledDown(t *testing.T) {
	s := New()
	now := time.Now()
	p1 := makeProvider("model-1")
	p2 := makeProvider("model-2")
	p3 := makeProvider("model-3")
	chain := []settings.ActiveProviderSettings{p1, p2, p3}

	// Penalize p2 (would be first candidate after p1).
	s.Penalize(settings.ProviderKey(p2), now.Add(5*time.Minute))

	got, ok := s.NextAvailable(chain, settings.ProviderKey(p1), now)
	if !ok {
		t.Fatal("expected p3, got none")
	}
	if got.Model != p3.Model {
		t.Errorf("NextAvailable() = %q, want %q", got.Model, p3.Model)
	}
}

func TestNextAvailable_AllCooledDown(t *testing.T) {
	s := New()
	now := time.Now()
	p1 := makeProvider("model-1")
	p2 := makeProvider("model-2")
	p3 := makeProvider("model-3")
	chain := []settings.ActiveProviderSettings{p1, p2, p3}

	s.Penalize(settings.ProviderKey(p2), now.Add(5*time.Minute))
	s.Penalize(settings.ProviderKey(p3), now.Add(5*time.Minute))

	_, ok := s.NextAvailable(chain, settings.ProviderKey(p1), now)
	if ok {
		t.Error("expected no provider available, but got one")
	}
}

func TestNextAvailable_SingleChain(t *testing.T) {
	s := New()
	now := time.Now()
	p1 := makeProvider("model-1")
	chain := []settings.ActiveProviderSettings{p1}

	_, ok := s.NextAvailable(chain, settings.ProviderKey(p1), now)
	if ok {
		t.Error("single-provider chain should never return a next provider")
	}
}

func TestPenalize_ExtendsOnlyIfLater(t *testing.T) {
	s := New()
	now := time.Now()
	key := "test-key"

	later := now.Add(10 * time.Minute)
	earlier := now.Add(2 * time.Minute)

	s.Penalize(key, later)
	s.Penalize(key, earlier) // should NOT override the later expiry

	if !s.IsOnCooldown(key, now.Add(5*time.Minute)) {
		t.Error("cooldown should still be active 5 min from now (expiry is 10 min)")
	}
}

func TestIsOnCooldown_ExpiredCooldown(t *testing.T) {
	s := New()
	past := time.Now().Add(-1 * time.Minute)
	key := "test-key"

	s.Penalize(key, past)

	if s.IsOnCooldown(key, time.Now()) {
		t.Error("cooldown should have expired")
	}
}

func TestNextAvailable_WrapAround(t *testing.T) {
	s := New()
	now := time.Now()
	p1 := makeProvider("model-1")
	p2 := makeProvider("model-2")
	p3 := makeProvider("model-3")
	chain := []settings.ActiveProviderSettings{p1, p2, p3}

	// Start from p3 — wrap around and skip p1 (cooled down), returning p2.
	s.Penalize(settings.ProviderKey(p1), now.Add(5*time.Minute))

	got, ok := s.NextAvailable(chain, settings.ProviderKey(p3), now)
	if !ok {
		t.Fatal("expected p2 after wrapping, got none")
	}
	if got.Model != p2.Model {
		t.Errorf("NextAvailable() = %q, want %q", got.Model, p2.Model)
	}
}
