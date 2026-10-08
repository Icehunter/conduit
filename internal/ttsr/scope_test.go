package ttsr

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

func re(s string) *regexp.Regexp { return regexp.MustCompile(s) }

func TestWatcher_ToolArgs(t *testing.T) {
	rules := []Rule{
		{Name: "no-todo", Pattern: re(`TODO`), Scope: ScopeToolArgs},
		{Name: "go-only", Pattern: re(`panic\(`), Tools: []string{"Write", "Edit"}, Paths: []string{"**/*.go"}},
		{Name: "lockfile", Paths: []string{"go.sum", "package-lock.json"}},
		{Name: "bash-rm", Pattern: re(`rm -rf /`), Tools: []string{"bash"}},
	}
	tests := []struct {
		name string
		tool string
		args string
		want string // rule name, "" for none
	}{
		{"any tool, scope only", "Grep", `{"pattern":"TODO fix"}`, "no-todo"},
		{"no match", "Grep", `{"pattern":"x"}`, ""},
		{"tool + path glob", "Write", `{"file_path":"/p/a/b.go","content":"panic(1)"}`, "go-only"},
		{"path glob rejects other ext", "Write", `{"file_path":"/p/a/b.py","content":"panic(1)"}`, ""},
		{"tool filter rejects", "Read", `{"file_path":"/p/a/b.go","content":"panic(1)"}`, ""},
		{"tool filter is case-insensitive", "Edit", `{"file_path":"x.go","new_string":"panic(2)"}`, "go-only"},
		{"path-only rule fires on path", "Write", `{"file_path":"/repo/go.sum","content":"x"}`, "lockfile"},
		{"path-only rule needs a path", "Write", `{"content":"x"}`, ""},
		{"basename glob does not match dir name", "Write", `{"file_path":"/repo/go.sum/other","content":"x"}`, ""},
		{"tool glob case folded", "Bash", `{"command":"rm -rf /"}`, "bash-rm"},
		{"nested args flattened", "Write", `{"edits":[{"new":"has TODO"}]}`, "no-todo"},
		{"non-json args matched raw", "Write", `not json TODO`, "no-todo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewWatcher(rules)
			r, fired := w.CheckToolCall(tt.tool, tt.args)
			got := ""
			if fired {
				got = r.Name
			}
			if got != tt.want {
				t.Errorf("fired %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWatcher_TextRulesIgnoreToolScopeAndViceVersa(t *testing.T) {
	w := NewWatcher([]Rule{
		{Name: "text", Pattern: re(`BAD`)},
		{Name: "tool", Pattern: re(`BAD`), Scope: ScopeToolArgs},
	})
	r, fired := w.Feed("this is BAD")
	if !fired || r.Name != "text" {
		t.Fatalf("Feed fired %v %v", r, fired)
	}
	w2 := NewWatcher([]Rule{{Name: "text", Pattern: re(`BAD`)}})
	if _, fired := w2.CheckToolCall("Write", `{"c":"BAD"}`); fired {
		t.Error("text-scope rule fired on tool args")
	}
	w3 := NewWatcher([]Rule{{Name: "both", Pattern: re(`BAD`), Scope: ScopeText | ScopeToolArgs}})
	if _, fired := w3.CheckToolCall("Write", `{"c":"BAD"}`); !fired {
		t.Error("both-scope rule did not fire on tool args")
	}
}

func TestWatcher_Agents(t *testing.T) {
	rules := []Rule{{Name: "r", Pattern: re(`X`), Agents: []string{"main", "general-*"}}}
	tests := []struct {
		agent string
		want  bool
	}{
		{"", true}, // default agent is "main"
		{"main", true},
		{"general-purpose", true},
		{"Explore", false},
		{"teammate", false},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			var opts []Option
			if tt.agent != "" {
				opts = append(opts, WithAgent(tt.agent))
			}
			if _, fired := NewWatcher(rules, opts...).Feed("X"); fired != tt.want {
				t.Errorf("fired = %v, want %v", fired, tt.want)
			}
		})
	}
}

func TestWatcher_WarnMode(t *testing.T) {
	w := NewWatcher([]Rule{
		{Name: "soft", Pattern: re(`maybe`), Mode: ModeWarn, Correction: "careful"},
		{Name: "hard", Pattern: re(`STOP`), Correction: "halt"},
	})
	if _, fired := w.Feed("maybe so"); fired {
		t.Fatal("warn rule interrupted")
	}
	// Still matching in the tail must not queue it again within the attempt.
	if _, fired := w.Feed(" and maybe more"); fired {
		t.Fatal("warn rule interrupted on a later delta")
	}
	got := w.TakeWarnings()
	if len(got) != 1 || got[0].Name != "soft" {
		t.Fatalf("warnings = %v, want one 'soft'", got)
	}
	if len(w.TakeWarnings()) != 0 {
		t.Error("TakeWarnings did not clear")
	}
	// An interrupting rule still interrupts after a warn.
	if r, fired := w.Feed(" STOP"); !fired || r.Name != "hard" {
		t.Errorf("hard rule: fired=%v rule=%v", fired, r)
	}
	// A new attempt may warn again, and StartAttempt drops stale warnings.
	w.StartAttempt()
	w.Feed("maybe")
	w.StartAttempt()
	if len(w.TakeWarnings()) != 0 {
		t.Error("StartAttempt kept warnings from the discarded attempt")
	}
}

func TestWatcher_WarnOnToolArgs(t *testing.T) {
	w := NewWatcher([]Rule{{Name: "w", Pattern: re(`secret`), Scope: ScopeToolArgs, Mode: ModeWarn, Correction: "c"}})
	if _, fired := w.CheckToolCall("Write", `{"c":"a secret"}`); fired {
		t.Fatal("warn interrupted")
	}
	if got := w.TakeWarnings(); len(got) != 1 {
		t.Fatalf("warnings = %v", got)
	}
}

func TestWatcher_RepeatGating(t *testing.T) {
	tests := []struct {
		name  string
		rule  Rule
		turns []bool // whether the rule fires on each successive turn
	}{
		{"always", Rule{}, []bool{true, true, true}},
		{"once", Rule{Once: true}, []bool{true, false, false}},
		{"gap 2", Rule{Gap: 2}, []bool{true, false, true, false}},
		{"gap 1 is every turn", Rule{Gap: 1}, []bool{true, true}},
		{"max fires per turn does not carry over", Rule{MaxFires: 1}, []bool{true, true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mem Memory
			rule := tt.rule
			rule.Name, rule.Pattern = "r", re(`X`)
			for i, want := range tt.turns {
				w := NewWatcher([]Rule{rule}, WithMemory(&mem))
				if _, fired := w.Feed("X"); fired != want {
					t.Errorf("turn %d: fired = %v, want %v", i, fired, want)
				}
			}
		})
	}
}

func TestWatcher_MaxFiresWithinTurnSurvivesRetries(t *testing.T) {
	w := NewWatcher([]Rule{{Name: "r", Pattern: re(`X`), MaxFires: 1}})
	if _, fired := w.Feed("X"); !fired {
		t.Fatal("first attempt should fire")
	}
	w.StartAttempt()
	if _, fired := w.Feed("X"); fired {
		t.Error("MaxFires=1 fired again on the retry")
	}
}

func TestRepetitionGuard(t *testing.T) {
	unit := "I should check the configuration file again. "
	tests := []struct {
		name string
		feed func(w *Watcher) (*Rule, bool)
		want bool
	}{
		{"text loop", func(w *Watcher) (*Rule, bool) { return w.Feed(strings.Repeat(unit, 80)) }, true},
		{"thinking loop", func(w *Watcher) (*Rule, bool) { return w.FeedThinking(strings.Repeat(unit, 80)) }, true},
		{"too few repeats", func(w *Watcher) (*Rule, bool) { return w.Feed(strings.Repeat(unit, 5)) }, false},
		{"distinct long text", func(w *Watcher) (*Rule, bool) {
			var sb strings.Builder
			for i := range 200 {
				fmt.Fprintf(&sb, "line %d: some different content here\n", i)
			}
			return w.Feed(sb.String())
		}, false},
		{"ruler of dashes", func(w *Watcher) (*Rule, bool) { return w.Feed(strings.Repeat("-", 3000)) }, false},
		{"two-byte padding", func(w *Watcher) (*Rule, bool) { return w.Feed(strings.Repeat("ab", 1500)) }, false},
		{"long unit repeated a few times", func(w *Watcher) (*Rule, bool) {
			var sb strings.Builder
			for i := range 6 {
				fmt.Fprintf(&sb, "paragraph %d of a quoted block that is quite long. ", i)
			}
			return w.Feed(strings.Repeat(sb.String(), 4))
		}, false},
		{"run shorter than the minimum", func(w *Watcher) (*Rule, bool) { return w.Feed(strings.Repeat("go on ", 100)) }, false},
		{"tiny unit is data, not a loop", func(w *Watcher) (*Rule, bool) { return w.Feed(strings.Repeat("go on ", 300)) }, false},
		{"loop after a long varied prefix", func(w *Watcher) (*Rule, bool) {
			var sb strings.Builder
			for i := range 100 {
				fmt.Fprintf(&sb, "varied %d text\n", i)
			}
			sb.WriteString(strings.Repeat(unit, 80))
			return w.Feed(sb.String())
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, fired := tt.feed(NewWatcher(nil, WithRepetitionGuard()))
			if fired != tt.want {
				t.Fatalf("fired = %v, want %v", fired, tt.want)
			}
			if fired && (r.Name != "repetition-guard" || r.Correction == "" || r.Mode != ModeInterrupt) {
				t.Errorf("rule = %+v", r)
			}
		})
	}
}

func TestRepetitionGuard_OffByDefault(t *testing.T) {
	if _, fired := NewWatcher(nil).Feed(strings.Repeat("I should check the file again. ", 80)); fired {
		t.Error("guard fired without WithRepetitionGuard")
	}
}

func TestRepetitionGuard_StreamedInSmallDeltas(t *testing.T) {
	w := NewWatcher(nil, WithRepetitionGuard())
	text := strings.Repeat("The same sentence keeps coming back. ", 100)
	firedAt := -1
	for i := 0; i < len(text); i += 7 {
		end := min(i+7, len(text))
		if _, fired := w.Feed(text[i:end]); fired {
			firedAt = i
			break
		}
	}
	if firedAt < 0 {
		t.Fatal("never fired")
	}
	// It must fire as soon as the run is long enough, not at the end.
	if firedAt > repShortRun+len("The same sentence keeps coming back. ")*2 {
		t.Errorf("fired late at byte %d", firedAt)
	}
}

func TestRepetitionGuard_Bounded(t *testing.T) {
	w := NewWatcher(nil, WithRepetitionGuard())
	loop := strings.Repeat("I will try the very same thing once more. ", 90)
	fires := 0
	for range 6 {
		w.StartAttempt()
		if _, fired := w.Feed(loop); fired {
			fires++
		}
	}
	if fires != repMaxFires {
		t.Errorf("fires = %d, want the cap %d", fires, repMaxFires)
	}
}

func TestLoopingSuffixWorstCase(t *testing.T) {
	// Highly repetitive but unit-poor input must stay cheap and not fire.
	b := []byte(strings.Repeat("a", repWindow))
	if loopingSuffix(b) {
		t.Error("single-byte run flagged")
	}
}

func TestGlobs(t *testing.T) {
	tests := []struct {
		glob, path string
		want       bool
	}{
		{"*.go", "/a/b/c.go", true},
		{"*.go", "/a/b/c.gox", false},
		{"**/*.go", "/a/b/c.go", true},
		{"**/*.go", "c.go", true},
		{"src/**", "/repo/src/a/b.txt", true},
		{"src/*.go", "/repo/src/a.go", true},
		{"src/*.go", "/repo/src/sub/a.go", false},
		{"internal/**/x_test.go", "/r/internal/a/b/x_test.go", true},
		{"go.mod", "/r/go.mod", true},
		{"go.mod", "/r/ago.mod", false},
		{"?.md", "/r/a.md", true},
		{"?.md", "/r/ab.md", false},
		{"./docs/*.md", "/r/docs/a.md", true},
	}
	for _, tt := range tests {
		t.Run(tt.glob+" "+tt.path, func(t *testing.T) {
			if got := pathGlobAny([]string{tt.glob}, tt.path); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func feedChunks(w *Watcher, s string, size int) bool {
	for i := 0; i < len(s); i += size {
		if _, fired := w.Feed(s[i:min(i+size, len(s))]); fired {
			return true
		}
	}
	return false
}

// Repetitive but legitimate output must not trip the guard; real loops must.
func TestRepetitionGuard_FalsePositivesAndLoops(t *testing.T) {
	repeatLines := func(line string, n int) string { return strings.Repeat(line+"\n", n) }
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"byte array in a fence", "```go\nvar buf = [256]byte{\n" + repeatLines("\t0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,", 60), false},
		{"byte array without a fence", repeatLines("\t0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,", 60), false},
		{"numpy zeros", "[" + strings.Repeat("0, ", 1200), false},
		{"identical test log lines", repeatLines("    --- PASS: TestThing/case (0.00s)", 100), false},
		{"table of n/a rows", "| Name | Status |\n|---|---|\n" + repeatLines("| - | n/a |", 120), false},
		{"numbered table", func() string {
			var sb strings.Builder
			for i := range 200 {
				fmt.Fprintf(&sb, "| row %d | ok |\n", i)
			}
			return sb.String()
		}(), false},
		{"short unit below the byte floor", strings.Repeat("the cat sat on a mat, ok\t", 55), false}, // 25B x 55 = 1375B
		{"same short unit past the byte floor", strings.Repeat("the cat sat on a mat, ok\t", 110), true},
		{"loop inside an open fence is left alone", "```\n" + strings.Repeat("I keep writing this very same sentence again. ", 80), false},
		{"loop after the fence closed", "```\ncode\n```\n" + strings.Repeat("I keep writing this very same sentence again. ", 80), true},
		{"prose loop", strings.Repeat("I will check the configuration file again now. ", 80), true},
		{"multi-line prose loop", strings.Repeat("Step one: read the file.\nStep two: read the file again and again.\n", 40), true},
	}
	for _, tt := range tests {
		for _, size := range []int{7, 1000} {
			t.Run(fmt.Sprintf("%s/chunk%d", tt.name, size), func(t *testing.T) {
				w := NewWatcher(nil, WithRepetitionGuard())
				if got := feedChunks(w, tt.text, size); got != tt.want {
					t.Errorf("fired = %v, want %v", got, tt.want)
				}
			})
		}
	}
}

// A ``` split across deltas still opens the fence.
func TestRepetitionGuard_FenceSplitAcrossDeltas(t *testing.T) {
	w := NewWatcher(nil, WithRepetitionGuard())
	w.Feed("text ``")
	w.Feed("`go\n")
	if feedChunks(w, strings.Repeat("I keep writing this very same sentence again. ", 80), 50) {
		t.Error("fired inside a fence opened by a split marker")
	}
}

// A warn rule is recorded as fired when delivered, not when it matches, so a
// discarded or undelivered match does not use up Once or Gap.
func TestWatcher_WarnRecordedOnDelivery(t *testing.T) {
	var mem Memory
	rule := Rule{Name: "w", Pattern: re(`X`), Mode: ModeWarn, Once: true}

	w := NewWatcher([]Rule{rule}, WithMemory(&mem))
	w.Feed("X")
	// Turn ends without another request: the warning is never taken.

	w = NewWatcher([]Rule{rule}, WithMemory(&mem))
	w.Feed("X")
	if got := w.TakeWarnings(); len(got) != 1 {
		t.Fatalf("undelivered warning was lost: %v", got)
	}

	w = NewWatcher([]Rule{rule}, WithMemory(&mem))
	w.Feed("X")
	if got := w.TakeWarnings(); len(got) != 0 {
		t.Errorf("Once rule warned again after delivery: %v", got)
	}
}

func TestWatcher_WarnDiscardedAttemptKeepsRule(t *testing.T) {
	w := NewWatcher([]Rule{{Name: "w", Pattern: re(`X`), Mode: ModeWarn, Gap: 5}})
	w.Feed("X")
	w.StartAttempt() // the attempt was interrupted by another rule
	w.Feed("X")
	if got := w.TakeWarnings(); len(got) != 1 {
		t.Errorf("warnings = %v, want the rule to fire in the retry", got)
	}
}
