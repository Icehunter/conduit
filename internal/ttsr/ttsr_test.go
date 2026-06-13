package ttsr

import (
	"regexp"
	"strings"
	"testing"
)

func TestWatcher_MatchOnFirstDelta(t *testing.T) {
	rule := Rule{
		Name:     "test-rule",
		Pattern:  regexp.MustCompile(`rewrite all`),
		MaxFires: 0,
	}
	w := NewWatcher([]Rule{rule})
	r, fired := w.Feed("I will rewrite all the files")
	if !fired {
		t.Fatal("expected match but got none")
	}
	if r.Name != "test-rule" {
		t.Fatalf("expected rule %q, got %q", "test-rule", r.Name)
	}
}

func TestWatcher_CrossDeltaMatch(t *testing.T) {
	rule := Rule{
		Name:    "cross-delta",
		Pattern: regexp.MustCompile(`hello world`),
	}
	w := NewWatcher([]Rule{rule})
	if _, fired := w.Feed("hel"); fired {
		t.Fatal("should not match on partial delta")
	}
	r, fired := w.Feed("lo world — complete")
	if !fired {
		t.Fatal("expected cross-delta match")
	}
	if r.Name != "cross-delta" {
		t.Fatalf("unexpected rule name %q", r.Name)
	}
}

func TestWatcher_MaxFiresCap(t *testing.T) {
	rule := Rule{
		Name:     "capped",
		Pattern:  regexp.MustCompile(`trigger`),
		MaxFires: 2,
	}
	w := NewWatcher([]Rule{rule})

	_, ok1 := w.Feed("trigger")
	_, ok2 := w.Feed("trigger again")
	_, ok3 := w.Feed("trigger yet again")

	if !ok1 {
		t.Error("first fire should match")
	}
	if !ok2 {
		t.Error("second fire should match")
	}
	if ok3 {
		t.Error("third fire should be capped (MaxFires=2)")
	}
}

func TestWatcher_NoMatch(t *testing.T) {
	rule := Rule{
		Name:    "no-match",
		Pattern: regexp.MustCompile(`very specific phrase xyz`),
	}
	w := NewWatcher([]Rule{rule})
	r, fired := w.Feed("nothing relevant here")
	if fired {
		t.Fatalf("expected no match, got rule %q", r.Name)
	}
}

func TestWatcher_GlobalBudgetNotHere(t *testing.T) {
	// The Watcher itself does not enforce a global per-turn fire budget;
	// that's loop.go's responsibility. Confirm the watcher keeps firing
	// beyond what loop.go's maxTTSRFiresPerTurn=3 would allow — the Watcher
	// has no knowledge of that limit.
	rule := Rule{
		Name:    "no-global-cap",
		Pattern: regexp.MustCompile(`boom`),
	}
	w := NewWatcher([]Rule{rule})
	for i := range 10 {
		_, fired := w.Feed("boom")
		if !fired {
			t.Fatalf("iteration %d: Watcher should not enforce global budget", i)
		}
	}
}

func TestWatcher_TailTrim(t *testing.T) {
	// Fill the buffer past 4KB with noise, then append a delta containing the
	// target pattern. The trim logic must keep the last 4KB so the pattern
	// is still visible.
	rule := Rule{
		Name:    "tail-trim",
		Pattern: regexp.MustCompile(`needle`),
	}
	w := NewWatcher([]Rule{rule})

	// Write 5KB of noise (no match).
	noise := strings.Repeat("x", 5*1024)
	_, fired := w.Feed(noise)
	if fired {
		t.Fatal("noise should not match")
	}

	// Now append the needle — it must fall within the last 4KB of the buffer.
	r, fired := w.Feed(" needle ")
	if !fired {
		t.Fatal("needle should match after tail trim")
	}
	if r.Name != "tail-trim" {
		t.Fatalf("unexpected rule %q", r.Name)
	}
}
