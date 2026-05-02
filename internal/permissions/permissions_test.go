package permissions

import "testing"

func TestMatchRule_ToolOnly(t *testing.T) {
	if !matchRule("Bash", "Bash", "") {
		t.Error("tool-only rule should match")
	}
	if matchRule("Bash", "Edit", "") {
		t.Error("tool-only rule should not match different tool")
	}
}

func TestMatchRule_CaseInsensitive(t *testing.T) {
	if !matchRule("bash", "Bash", "") {
		t.Error("rule matching should be case-insensitive")
	}
}

func TestMatchRule_ExactInput(t *testing.T) {
	if !matchRule("Bash(git status)", "Bash", "git status") {
		t.Error("exact match should work")
	}
	if matchRule("Bash(git status)", "Bash", "git log") {
		t.Error("exact match should not match different input")
	}
}

func TestMatchRule_PrefixStar(t *testing.T) {
	if !matchRule("Bash(git log *)", "Bash", "git log --oneline") {
		t.Error("prefix * should match prefix")
	}
	if !matchRule("Bash(git log *)", "Bash", "git log main..HEAD") {
		t.Error("prefix * should match longer input")
	}
	if matchRule("Bash(git log *)", "Bash", "git status") {
		t.Error("prefix * should not match different prefix")
	}
}

func TestMatchRule_LegacyColonStar(t *testing.T) {
	if !matchRule("Bash(npm:*)", "Bash", "npm install") {
		t.Error("legacy :* prefix should match")
	}
}

func TestMatchRule_WildcardGlob(t *testing.T) {
	if !matchRule("Bash(git*)", "Bash", "git status") {
		t.Error("glob * should match prefix")
	}
	if !matchRule("Bash(git*)", "Bash", "git log --oneline") {
		t.Error("glob * should match longer")
	}
}

func TestMatchRule_NoParenMismatch(t *testing.T) {
	// Rule without closing paren is invalid — should not match.
	if matchRule("Bash(git log", "Bash", "git log") {
		t.Error("malformed rule (no closing paren) should not match")
	}
}

func TestGate_BypassMode(t *testing.T) {
	g := New(ModeBypassPermissions, nil, nil, nil)
	if g.Check("Bash", "rm -rf /") != DecisionAllow {
		t.Error("bypass mode should allow everything")
	}
}

func TestGate_DenyTakesPriority(t *testing.T) {
	g := New(ModeDefault, []string{"Bash"}, []string{"Bash"}, nil)
	// deny list takes priority over allow
	if g.Check("Bash", "") != DecisionDeny {
		t.Error("deny should take priority over allow")
	}
}

func TestGate_AllowList(t *testing.T) {
	g := New(ModeDefault, []string{"Bash(git status)"}, nil, nil)
	if g.Check("Bash", "git status") != DecisionAllow {
		t.Error("allowed rule should return Allow")
	}
	if g.Check("Bash", "git push") != DecisionAsk {
		t.Error("non-allowed command should return Ask in default mode")
	}
}

func TestGate_SessionAllow(t *testing.T) {
	g := New(ModeDefault, nil, nil, nil)
	g.AllowForSession("Bash(git log *)")
	if g.Check("Bash", "git log --oneline") != DecisionAllow {
		t.Error("session-allowed rule should return Allow")
	}
}

func TestGate_SetMode(t *testing.T) {
	g := New(ModeDefault, nil, nil, nil)
	g.SetMode(ModeBypassPermissions)
	if g.Mode() != ModeBypassPermissions {
		t.Error("SetMode should update Mode()")
	}
}

func TestSuggestRule(t *testing.T) {
	if SuggestRule("Bash", "git log") != "Bash(git log)" {
		t.Errorf("unexpected: %s", SuggestRule("Bash", "git log"))
	}
	if SuggestRule("Edit", "") != "Edit" {
		t.Errorf("unexpected: %s", SuggestRule("Edit", ""))
	}
}

func TestGate_Lists(t *testing.T) {
	allow := []string{"Bash(git log *)", "Edit"}
	deny := []string{"Bash(rm -rf *)"}
	ask := []string{"Bash(npm *)"}

	g := New(ModeDefault, allow, deny, ask)
	gotAllow, gotDeny, gotAsk := g.Lists()

	if len(gotAllow) != len(allow) {
		t.Errorf("allow list len = %d, want %d", len(gotAllow), len(allow))
	}
	if len(gotDeny) != len(deny) {
		t.Errorf("deny list len = %d, want %d", len(gotDeny), len(deny))
	}
	if len(gotAsk) != len(ask) {
		t.Errorf("ask list len = %d, want %d", len(gotAsk), len(ask))
	}
	if gotAllow[0] != allow[0] {
		t.Errorf("allow[0] = %q, want %q", gotAllow[0], allow[0])
	}
}

func TestGate_Lists_Empty(t *testing.T) {
	g := New(ModeDefault, nil, nil, nil)
	a, d, ask := g.Lists()
	if len(a) != 0 || len(d) != 0 || len(ask) != 0 {
		t.Error("empty gate should return empty lists")
	}
}

func TestGate_Lists_Immutable(t *testing.T) {
	g := New(ModeDefault, []string{"Bash"}, nil, nil)
	a, _, _ := g.Lists()
	// Mutating the returned slice must not affect the gate.
	a[0] = "MUTATED"
	a2, _, _ := g.Lists()
	if a2[0] == "MUTATED" {
		t.Error("Lists() should return a copy, not a reference")
	}
}
