// Package ttsr implements Token-Time Stopping Rules (TTSR): user-defined regex
// patterns that, when matched in the model's streaming text output, abort the
// stream and inject a correction message so the turn can be retried.
package ttsr

import (
	"regexp"
	"strings"
)

// Rule is a compiled mid-stream interruption rule.
type Rule struct {
	Name       string         // unique identifier
	Pattern    *regexp.Regexp // compiled pattern; matched against the tail buffer
	Correction string         // injected as user <system-reminder> when fired
	MaxFires   int            // max fires for this rule per turn; 0 = no per-rule cap
}

// ErrMatch is returned by drainStream when a TTSR rule fires.
type ErrMatch struct {
	Rule *Rule
}

func (e *ErrMatch) Error() string { return "ttsr: rule fired: " + e.Rule.Name }

const tailMaxBytes = 4096

// Watcher tracks mid-stream rule firing for a single turn.
// Create one per Run() call with NewWatcher; do not reuse across turns.
type Watcher struct {
	rules []*Rule
	fired map[string]int // rule name → fire count this turn
	tail  strings.Builder
	tailN int // byte count written to tail (before trimming)
}

// NewWatcher creates a fresh Watcher for a single turn.
// Makes a copy of the rules slice; safe to modify the original after this call.
func NewWatcher(rules []Rule) *Watcher {
	rs := make([]*Rule, len(rules))
	for i := range rules {
		cp := rules[i]
		rs[i] = &cp
	}
	return &Watcher{rules: rs, fired: make(map[string]int)}
}

// Feed appends delta to a sliding 4KB tail buffer and tests each rule.
// Returns the first matching rule that hasn't exceeded MaxFires.
// Called on every text_delta event (NOT thinking_delta).
func (w *Watcher) Feed(delta string) (*Rule, bool) {
	w.tail.WriteString(delta)
	w.tailN += len(delta)
	if w.tailN > tailMaxBytes {
		s := w.tail.String()
		trim := len(s) - tailMaxBytes
		if trim > 0 {
			s = s[trim:]
		}
		w.tail.Reset()
		w.tail.WriteString(s)
		w.tailN = len(s)
	}
	tail := w.tail.String()
	for _, r := range w.rules {
		if r.MaxFires > 0 && w.fired[r.Name] >= r.MaxFires {
			continue
		}
		if r.Pattern.MatchString(tail) {
			w.fired[r.Name]++
			return r, true
		}
	}
	return nil, false
}
