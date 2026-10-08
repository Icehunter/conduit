// Package ttsr implements Token-Time Stopping Rules (TTSR): user-defined regex
// patterns that, when matched in the model's output, either abort the stream
// and inject a correction message so the turn can be retried (interrupt), or
// queue a reminder for the model's next step without disturbing the stream
// (warn). Rules can look at the visible text or at the arguments of a finished
// tool call, can be limited to tools, file paths and agents, and can be gated
// so they do not repeat. A repetition guard, off by default for the Watcher
// and enabled by the agent loop, interrupts output that has degenerated into
// a loop.
package ttsr

import (
	"regexp"
	"strings"
	"sync"
)

// Mode is what a rule does when it matches.
type Mode int

const (
	// ModeInterrupt aborts the stream and restarts it with the correction.
	ModeInterrupt Mode = iota
	// ModeWarn lets the stream finish and hands the correction to the model
	// with the next request.
	ModeWarn
)

// Scope selects which model output a rule is matched against.
type Scope uint8

const (
	// ScopeText matches the visible text stream.
	ScopeText Scope = 1 << iota
	// ScopeToolArgs matches the arguments of a finished tool call.
	ScopeToolArgs
)

// Rule is a compiled mid-stream rule.
type Rule struct {
	Name       string         // unique identifier
	Pattern    *regexp.Regexp // compiled pattern; nil matches anything (path-only rules)
	Correction string         // injected as user <system-reminder> when fired
	MaxFires   int            // max fires for this rule per turn; 0 = no per-rule cap

	// Mode is ModeInterrupt (the zero value) or ModeWarn.
	Mode Mode
	// Scope is where the rule looks. Zero means ScopeText, or ScopeToolArgs
	// when Tools or Paths is set.
	Scope Scope
	// Tools limits a tool-argument rule to calls of these tools (globs,
	// case-insensitive). Empty means every tool.
	Tools []string
	// Paths limits a tool-argument rule to calls whose file path matches one
	// of these globs ("**" crosses directories; a glob without "/" matches the
	// base name). A call with no file path never matches a rule with Paths.
	Paths []string
	// Agents limits the rule to these agents (globs): "main" for the root
	// loop, the agent type for sub-agents. Empty means every agent.
	Agents []string
	// Once stops the rule firing again after its first fire in the loop's
	// lifetime. Gap stops it firing again for that many user turns.
	Once bool
	Gap  int
}

func (r *Rule) scope() Scope {
	if r.Scope != 0 {
		return r.Scope
	}
	if len(r.Tools) > 0 || len(r.Paths) > 0 {
		return ScopeToolArgs
	}
	return ScopeText
}

// ErrMatch is returned by drainStream when an interrupting rule fires.
type ErrMatch struct {
	Rule *Rule
}

func (e *ErrMatch) Error() string { return "ttsr: rule fired: " + e.Rule.Name }

const tailMaxBytes = 4096

// Memory remembers rule fires across the turns of one loop, so Once and Gap
// gating outlive a single Run. The zero value is ready to use.
type Memory struct {
	mu        sync.Mutex
	turn      int
	total     map[string]int // rule name -> fires ever
	last      map[string]int // rule name -> turn of the last fire
	turnFires map[string]int // rule name -> fires this turn
}

func (m *Memory) beginTurn() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.turn++
	m.turnFires = make(map[string]int)
}

// allow reports whether the gating on r lets it fire now.
func (m *Memory) allow(r *Rule) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r.MaxFires > 0 && m.turnFires[r.Name] >= r.MaxFires {
		return false
	}
	if r.Once && m.total[r.Name] > 0 {
		return false
	}
	if last, ok := m.last[r.Name]; ok && r.Gap > 0 && m.turn-last < r.Gap {
		return false
	}
	return true
}

func (m *Memory) record(r *Rule) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.total == nil {
		m.total = make(map[string]int)
		m.last = make(map[string]int)
	}
	if m.turnFires == nil {
		m.turnFires = make(map[string]int)
	}
	m.total[r.Name]++
	m.last[r.Name] = m.turn
	m.turnFires[r.Name]++
}

// Option configures a Watcher.
type Option func(*Watcher)

// WithMemory shares fire history with other watchers of the same loop.
func WithMemory(m *Memory) Option { return func(w *Watcher) { w.mem = m } }

// WithAgent names the agent the watcher runs for, matched against Rule.Agents.
// The default is "main".
func WithAgent(name string) Option { return func(w *Watcher) { w.agent = name } }

// WithRepetitionGuard turns on interruption of looping output.
func WithRepetitionGuard() Option {
	return func(w *Watcher) { w.text, w.thinking = new(loopDetector), new(loopDetector) }
}

// Watcher tracks mid-stream rule firing for a single turn (one Run). Call
// StartAttempt before each request of the turn; a retry after an interrupt
// reuses the Watcher so fire counts and gating carry over.
type Watcher struct {
	rules []*Rule
	mem   *Memory
	agent string

	tail  strings.Builder
	tailN int // byte count written to tail (before trimming)

	warned  map[string]bool // warn rules already fired this attempt
	pending []*Rule         // warn rules to hand to the model

	text, thinking *loopDetector // nil unless the repetition guard is on
	repFires       int
}

// NewWatcher creates a Watcher for one turn.
// Makes a copy of the rules slice; safe to modify the original after this call.
func NewWatcher(rules []Rule, opts ...Option) *Watcher {
	rs := make([]*Rule, len(rules))
	for i := range rules {
		cp := rules[i]
		rs[i] = &cp
	}
	w := &Watcher{rules: rs, agent: "main", warned: make(map[string]bool)}
	for _, o := range opts {
		o(w)
	}
	if w.mem == nil {
		w.mem = new(Memory)
	}
	w.mem.beginTurn()
	return w
}

// StartAttempt clears the per-response state (stream tail, pending warnings,
// repetition buffers) before a request. Fire counts and gating are kept.
func (w *Watcher) StartAttempt() {
	w.tail.Reset()
	w.tailN = 0
	w.pending = nil
	clear(w.warned)
	if w.text != nil {
		w.text.reset()
		w.thinking.reset()
	}
}

// TakeWarnings returns the warn-mode rules that matched since the last call,
// for the caller to hand to the model, and clears them. A warn rule counts as
// fired only here, on delivery: a match whose attempt is discarded or ends
// without another request does not use up a Once or Gap rule.
func (w *Watcher) TakeWarnings() []*Rule {
	out := w.pending
	w.pending = nil
	for _, r := range out {
		w.mem.record(r)
	}
	return out
}

// applies reports whether r is eligible for scope under the watcher's agent
// and the rule's gating.
func (w *Watcher) applies(r *Rule, scope Scope) bool {
	if r.scope()&scope == 0 {
		return false
	}
	if len(r.Agents) > 0 && !globAny(r.Agents, w.agent, true) {
		return false
	}
	return w.mem.allow(r)
}

// fire records an interrupting match and reports whether it interrupts. A warn
// rule is queued instead, at most once per attempt, and recorded on delivery.
func (w *Watcher) fire(r *Rule) bool {
	if r.Mode == ModeWarn {
		if w.warned[r.Name] {
			return false
		}
		w.warned[r.Name] = true
		w.pending = append(w.pending, r)
		return false
	}
	w.mem.record(r)
	return true
}

// Feed appends delta to a sliding 4KB tail buffer and tests each text rule.
// It returns the first interrupting rule that matched, or the repetition
// guard's rule when the text has started to loop. Warn rules that match are
// queued for TakeWarnings. Called on every text_delta event.
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
		if !w.applies(r, ScopeText) {
			continue
		}
		if r.Pattern != nil && !r.Pattern.MatchString(tail) {
			continue
		}
		if w.fire(r) {
			return r, true
		}
	}
	return w.repetition(w.text, delta)
}

// FeedThinking feeds a thinking_delta. Rules never match thinking, but a
// reasoning loop is as wasteful as a text loop, so the repetition guard
// watches it.
func (w *Watcher) FeedThinking(delta string) (*Rule, bool) {
	return w.repetition(w.thinking, delta)
}

// CheckToolCall tests the arguments of a finished tool call (rawArgs is its
// JSON input) against the tool-argument rules. It returns the first
// interrupting rule that matched; warn rules are queued.
func (w *Watcher) CheckToolCall(toolName, rawArgs string) (*Rule, bool) {
	var applicable []*Rule
	for _, r := range w.rules {
		if w.applies(r, ScopeToolArgs) && (len(r.Tools) == 0 || globAny(r.Tools, toolName, true)) {
			applicable = append(applicable, r)
		}
	}
	if len(applicable) == 0 {
		return nil, false
	}
	text, path := flattenArgs(rawArgs)
	for _, r := range applicable {
		if len(r.Paths) > 0 && (path == "" || !pathGlobAny(r.Paths, path)) {
			continue
		}
		if r.Pattern != nil && !r.Pattern.MatchString(text) {
			continue
		}
		if w.fire(r) {
			return r, true
		}
	}
	return nil, false
}
