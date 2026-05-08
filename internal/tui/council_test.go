package tui

import (
	"context"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/settings"
)

// ---- councilAgreeRE ----

func TestCouncilAgreeRegex(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"<council-agree/>", true},
		{"<council-agree />", true},
		{"<COUNCIL-AGREE/>", true},
		{"< council-agree/>", true},
		{"text before <council-agree/> text after", true},
		{"<council-agreement>", false},
		{"<council-agree-not/>", false},
		{"<council-agree", false},
		{"council-agree/", false},
	}
	for _, tc := range cases {
		got := councilAgreeRE.MatchString(tc.input)
		if got != tc.want {
			t.Errorf("councilAgreeRE.MatchString(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// ---- countActive ----

func TestCountActive(t *testing.T) {
	members := []councilMember{
		{active: true},
		{active: false},
		{active: true},
		{active: false},
		{active: true},
	}
	if got := countActive(members); got != 3 {
		t.Errorf("countActive = %d, want 3", got)
	}
}

// ---- buildDebateContext ----

func TestBuildDebateContext(t *testing.T) {
	members := []councilMember{
		{label: "A", active: true, lastResponse: "plan A"},
		{label: "B", active: false, lastResponse: "plan B"}, // ejected — must be excluded
		{label: "C", active: true, lastResponse: ""},        // no response — must be excluded
		{label: "D", active: true, lastResponse: "plan D"},
	}
	ctx := buildDebateContext(members)
	if want := "plan A"; !contains(ctx, want) {
		t.Errorf("buildDebateContext missing %q", want)
	}
	if contains(ctx, "plan B") {
		t.Error("buildDebateContext included inactive member B")
	}
	if contains(ctx, "label C") {
		t.Error("buildDebateContext included member C with empty response")
	}
	if want := "plan D"; !contains(ctx, want) {
		t.Errorf("buildDebateContext missing %q", want)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}

// ---- buildCouncilRoster ----

func TestBuildCouncilRoster_PrefixStripping(t *testing.T) {
	// "provider:" prefix must be stripped when looking up in providers map.
	providers := map[string]settings.ActiveProviderSettings{
		"mykey": {Kind: settings.ProviderKindOpenAICompatible, Model: "gpt-4"},
	}
	members := buildCouncilRoster(
		[]string{"provider:mykey"},
		providers,
		nil,
		map[string]string{},
		nil,
		func(p settings.ActiveProviderSettings) (*api.Client, error) { return nil, nil },
	)
	if len(members) != 1 {
		t.Fatalf("expected 1 member, got %d", len(members))
	}
	if members[0].label != "gpt-4" {
		t.Errorf("label = %q, want %q", members[0].label, "gpt-4")
	}
}

func TestBuildCouncilRoster_UnknownKeyDropped(t *testing.T) {
	members := buildCouncilRoster(
		[]string{"provider:does-not-exist"},
		map[string]settings.ActiveProviderSettings{},
		nil,
		map[string]string{},
		nil, nil,
	)
	if len(members) != 0 {
		t.Errorf("expected 0 members for unknown key, got %d", len(members))
	}
}

func TestBuildCouncilRoster_RoleAssignment(t *testing.T) {
	providers := map[string]settings.ActiveProviderSettings{
		"m1": {Kind: settings.ProviderKindOpenAICompatible, Model: "m1"},
		"m2": {Kind: settings.ProviderKindOpenAICompatible, Model: "m2"},
	}
	roles := map[string]string{
		"m1": "architect",
		"m2": "skeptic",
	}
	members := buildCouncilRoster(
		[]string{"m1", "m2"},
		providers,
		nil,
		roles,
		nil,
		func(p settings.ActiveProviderSettings) (*api.Client, error) { return nil, nil },
	)
	if len(members) != 2 {
		t.Fatalf("expected 2 members, got %d", len(members))
	}
	if members[0].role != "architect" {
		t.Errorf("member[0].role = %q, want architect", members[0].role)
	}
	if members[1].role != "skeptic" {
		t.Errorf("member[1].role = %q, want skeptic", members[1].role)
	}
}

// ---- convergenceScore ----

func TestConvergenceScore_IdenticalTexts(t *testing.T) {
	members := []councilMember{
		{active: true, lastResponse: "the quick brown fox"},
		{active: true, lastResponse: "the quick brown fox"},
	}
	score := convergenceScore(members)
	if score < 0.99 {
		t.Errorf("identical texts should score ~1.0, got %f", score)
	}
}

func TestConvergenceScore_DisjointTexts(t *testing.T) {
	members := []councilMember{
		{active: true, lastResponse: "alpha beta gamma"},
		{active: true, lastResponse: "delta epsilon zeta"},
	}
	score := convergenceScore(members)
	if score > 0.01 {
		t.Errorf("disjoint texts should score ~0, got %f", score)
	}
}

func TestConvergenceScore_SingleMember(t *testing.T) {
	members := []councilMember{
		{active: true, lastResponse: "only one"},
	}
	score := convergenceScore(members)
	if score != 0 {
		t.Errorf("single member score should be 0, got %f", score)
	}
}

// ---- runRoundParallel cancellation ----

func TestRunRoundParallel_ContextCancelShortCircuits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so all goroutines see it immediately

	members := []councilMember{
		{label: "A", model: "m", active: true},
		{label: "B", model: "m", active: true},
	}
	// With a pre-cancelled context, runRoundParallel must return quickly
	// without calling loop.RunSubAgentTyped (which would panic on nil loop).
	done := make(chan struct{})
	go func() {
		defer close(done)
		// loop=nil is safe here because ctx is already cancelled — the goroutines
		// exit via the ctx.Err() check before touching loop.
		runRoundParallel(ctx, nil, members, func(m councilMember) string {
			return "test"
		}, nil, time.Second, nil)
	}()
	select {
	case <-done:
		// Pass — returned promptly
	case <-time.After(3 * time.Second):
		t.Fatal("runRoundParallel did not return after context cancellation")
	}
}

// ---- rolePreamble ----

func TestRolePreamble(t *testing.T) {
	cases := map[string]string{
		"architect":     "Focus on overall structure",
		"skeptic":       "Challenge assumptions",
		"perf-reviewer": "Critique for runtime cost",
		"":              "",
		"unknown":       "",
	}
	for role, wantSubstr := range cases {
		got := rolePreamble(role)
		if wantSubstr != "" && !contains(got, wantSubstr) {
			t.Errorf("rolePreamble(%q) = %q, missing %q", role, got, wantSubstr)
		}
		if wantSubstr == "" && got != "" {
			t.Errorf("rolePreamble(%q) = %q, want empty", role, got)
		}
	}
}

// ---- parseVoteJSON ----

func TestParseVoteJSON(t *testing.T) {
	got := parseVoteJSON(`{"plan_0": 0.6, "plan_1": 0.4}`)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if got["plan_0"] < 0.59 || got["plan_0"] > 0.61 {
		t.Errorf("plan_0 = %f, want ~0.6", got["plan_0"])
	}
}

func TestParseVoteJSON_Invalid(t *testing.T) {
	got := parseVoteJSON("not json at all")
	if len(got) != 0 {
		t.Errorf("expected empty map for invalid JSON, got %v", got)
	}
}

// ---- CostUSDForModel (api package) ----

func TestCostUSDForModel_KnownModel(t *testing.T) {
	u := api.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	cost := api.CostUSDForModel("claude-opus-4-7", u)
	// $15 in + $75 out = $90
	if cost < 89.9 || cost > 90.1 {
		t.Errorf("cost = %f, want ~90.0", cost)
	}
}

func TestCostUSDForModel_UnknownModel(t *testing.T) {
	u := api.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}
	cost := api.CostUSDForModel("openai/gpt-4o", u)
	if cost != 0 {
		t.Errorf("cost for unknown model = %f, want 0", cost)
	}
}

// ---- isCouncilTrivial ----

func TestIsCouncilTrivial(t *testing.T) {
	trivial := []string{
		"hi",
		"Hi!",
		"hello",
		"hey",
		"thanks",
		"thank you",
		"ok",
		"okay",
		"yes",
		"no",
		"sure",
		"bye",
		"goodbye",
		"great",
		"perfect",
		"got it",
		"makes sense",
		"understood",
		"sounds good",
		"  ok  ",
		"Thanks!",
		"OK?",
	}
	for _, s := range trivial {
		if !isCouncilTrivial(s) {
			t.Errorf("isCouncilTrivial(%q) = false, want true", s)
		}
	}

	nontrivial := []string{
		"how do I implement a council debate loop in Go?",
		"explain the architecture of the TUI package",
		"fix the bug in run.go",
		"what is the best approach for streaming SSE responses?",
		"",
		"can you help me with the planmodetool",
	}
	for _, s := range nontrivial {
		if isCouncilTrivial(s) {
			t.Errorf("isCouncilTrivial(%q) = true, want false", s)
		}
	}
}

// ---- helpers ----

// ---- buildSynthesisPrompt (empty member list) ----

func TestBuildSynthesisPrompt_EmptyMembers(t *testing.T) {
	got := buildSynthesisPrompt("preamble", nil)
	if got != "preamble\n\n" {
		t.Errorf("buildSynthesisPrompt with no members = %q, want preamble only", got)
	}
}

// ---- accumulateStats with empty synthesis ----

func TestAccumulateStats_EmptySynthesis(t *testing.T) {
	members := []councilMember{
		{label: "A", model: "claude-opus-4-7", active: true, usage: api.Usage{InputTokens: 100, OutputTokens: 50}},
	}
	totalUsage, totalCost, perMember := accumulateStats(members, agent.SubAgentResult{}, "")
	if totalUsage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", totalUsage.InputTokens)
	}
	if totalUsage.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", totalUsage.OutputTokens)
	}
	if len(perMember) != 1 {
		t.Fatalf("perMember len = %d, want 1", len(perMember))
	}
	if totalCost <= 0 {
		t.Errorf("totalCost = %f, want > 0 for known model", totalCost)
	}
}

// ---- runRoundParallel — all members ejected leaves no active members ----

func TestRunRoundParallel_AllEjected(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel to eject all

	members := []councilMember{
		{label: "A", model: "m", active: true},
		{label: "B", model: "m", active: true},
	}
	allAgreed, atLeastOne := runRoundParallel(ctx, nil, members, func(m councilMember) string {
		return "prompt"
	}, nil, time.Second, nil)
	if atLeastOne {
		t.Error("atLeastOne should be false when all members are ejected via cancellation")
	}
	if allAgreed {
		t.Error("allAgreed should be false when no members responded")
	}
}
