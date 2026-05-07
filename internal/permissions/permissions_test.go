package permissions

import (
	"strings"
	"testing"
)

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

func TestMatchRule_DirectoryGlob(t *testing.T) {
	// Read(//dir/**) should match any file under dir.
	if !matchRule("Read(//home/user/project/**)", "Read", "/home/user/project/foo.go") {
		t.Error("directory glob should match file in dir")
	}
	if !matchRule("Read(//home/user/project/**)", "Read", "/home/user/project/sub/bar.go") {
		t.Error("directory glob should match file in subdirectory")
	}
	if matchRule("Read(//home/user/project/**)", "Read", "/home/user/other/baz.go") {
		t.Error("directory glob should not match file outside dir")
	}
}

func TestMatchRule_BashSubcmdColonStar(t *testing.T) {
	// Bash(git log:*) matches "git log ..." but not "git status"
	if !matchRule("Bash(git log:*)", "Bash", "git log --oneline HEAD") {
		t.Error("git log:* should match 'git log ...'")
	}
	if matchRule("Bash(git log:*)", "Bash", "git status") {
		t.Error("git log:* should not match 'git status'")
	}
}

func TestMatchRule_BashReadonly(t *testing.T) {
	readOnly := []string{
		"git status",
		"cd /Volumes/Engineering/Icehunter/conduit && wc -l internal/tui/model.go",
		"cd /Volumes/Engineering/Icehunter/conduit && find internal -type d -maxdepth 2 | sort",
		`cd /Volumes/Engineering/Icehunter/conduit && cat go.mod | head -50 && echo "done"`,
		"rg -n permissions internal/permissions",
	}
	for _, input := range readOnly {
		if !matchRule("Bash(readonly:*)", "Bash", input) {
			t.Errorf("Bash(readonly:*) should match %q", input)
		}
	}

	mutating := []string{
		"cd /tmp && rm -rf x",
		"cd /tmp && cat a > b",
		"find . -delete",
		"sed -i s/a/b/ file",
		"echo $(rm -rf /tmp/nope)",
	}
	for _, input := range mutating {
		if matchRule("Bash(readonly:*)", "Bash", input) {
			t.Errorf("Bash(readonly:*) should not match %q", input)
		}
	}
}

func TestGate_BypassMode(t *testing.T) {
	g := New("", nil, ModeBypassPermissions, nil, nil, nil)
	if g.Check("Bash", "rm -rf /") != DecisionAllow {
		t.Error("bypass mode should allow everything")
	}
}

func TestGate_DenyTakesPriority(t *testing.T) {
	g := New("", nil, ModeDefault, []string{"Bash"}, []string{"Bash"}, nil)
	// deny list takes priority over allow
	if g.Check("Bash", "") != DecisionDeny {
		t.Error("deny should take priority over allow")
	}
}

func TestGate_AllowList(t *testing.T) {
	g := New("", nil, ModeDefault, []string{"Bash(git status)"}, nil, nil)
	if g.Check("Bash", "git status") != DecisionAllow {
		t.Error("allowed rule should return Allow")
	}
	if g.Check("Bash", "git push") != DecisionAsk {
		t.Error("non-allowed command should return Ask in default mode")
	}
}

func TestGate_SessionAllow(t *testing.T) {
	g := New("", nil, ModeDefault, nil, nil, nil)
	g.AllowForSession("Bash(git log *)")
	if g.Check("Bash", "git log --oneline") != DecisionAllow {
		t.Error("session-allowed rule should return Allow")
	}
}

func TestGate_SetMode(t *testing.T) {
	g := New("", nil, ModeDefault, nil, nil, nil)
	g.SetMode(ModeBypassPermissions)
	if g.Mode() != ModeBypassPermissions {
		t.Error("SetMode should update Mode()")
	}
}

func TestSuggestRule(t *testing.T) {
	tests := []struct {
		tool, input, want string
	}{
		{"Bash", "git log --oneline", "Bash(readonly:*)"},
		{"Bash", "git status", "Bash(readonly:*)"},
		{"Bash", "cd /repo && wc -l file.go", "Bash(readonly:*)"},
		{"Bash", "cd /repo && find internal -type d -maxdepth 2 | sort", "Bash(readonly:*)"},
		{"Bash", "npm install", "Bash(npm install:*)"},
		{"Bash", "foobar --flag", "Bash(foobar:*)"},
		{"Bash", "", "Bash"},
		{"Read", "/home/user/project/foo.go", "Read(//home/user/project/**)"},
		{"Edit", "/tmp/file.txt", "Edit(//tmp/**)"},
		{"Edit", "", "Edit"},
		{"Grep", "pattern", "Grep(pattern)"},
	}
	for _, tt := range tests {
		got := SuggestRule(tt.tool, tt.input)
		if got != tt.want {
			t.Errorf("SuggestRule(%q, %q) = %q; want %q", tt.tool, tt.input, got, tt.want)
		}
	}
}

func TestSuggestRule_PathHardening(t *testing.T) {
	tests := []struct {
		tool        string
		input       string
		noTraversal bool // result must not contain ".."
	}{
		// Path traversal must be cleaned before building the allow rule.
		{"Read", "/home/user/project/../../../etc/passwd", true},
		{"Edit", "/tmp/dir/subdir/../../secret.txt", true},
		// Repeated slashes — should produce a clean path.
		{"Write", "/home//user//file.go", false},
		// Root dir — no glob, just tool name.
		{"Read", "/", false},
		// Empty input — just tool name.
		{"Read", "", false},
	}
	for _, tt := range tests {
		got := SuggestRule(tt.tool, tt.input)
		if !strings.HasPrefix(got, tt.tool) {
			t.Errorf("SuggestRule(%q, %q) = %q; must start with tool name", tt.tool, tt.input, got)
		}
		if tt.noTraversal && strings.Contains(got, "..") {
			t.Errorf("SuggestRule(%q, %q) = %q; must not contain path traversal", tt.tool, tt.input, got)
		}
	}
}

func TestGate_Lists(t *testing.T) {
	allow := []string{"Bash(git log *)", "Edit"}
	deny := []string{"Bash(rm -rf *)"}
	ask := []string{"Bash(npm *)"}

	g := New("", nil, ModeDefault, allow, deny, ask)
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
	g := New("", nil, ModeDefault, nil, nil, nil)
	a, d, ask := g.Lists()
	if len(a) != 0 || len(d) != 0 || len(ask) != 0 {
		t.Error("empty gate should return empty lists")
	}
}

func TestGate_Lists_Immutable(t *testing.T) {
	g := New("", nil, ModeDefault, []string{"Bash"}, nil, nil)
	a, _, _ := g.Lists()
	// Mutating the returned slice must not affect the gate.
	a[0] = "MUTATED"
	a2, _, _ := g.Lists()
	if a2[0] == "MUTATED" {
		t.Error("Lists() should return a copy, not a reference")
	}
}

// TestGate_ModeSemantics validates the mode-dispatch behaviour for
// acceptEdits, plan, and default modes.
func TestGate_ModeSemantics(t *testing.T) {
	tests := []struct {
		name  string
		mode  Mode
		tool  string
		input string
		want  Decision
	}{
		// acceptEdits — Bash must ask, file-edit tools must allow.
		{"acceptEdits/Bash deny", ModeAcceptEdits, "Bash", "rm -rf /", DecisionAsk},
		{"acceptEdits/Edit allow", ModeAcceptEdits, "Edit", "/any/file", DecisionAllow},
		{"acceptEdits/Write allow", ModeAcceptEdits, "Write", "/any/file", DecisionAllow},
		// plan — read-only tools allow, mutating tools ask (CC parity: prompt, don't hard-deny).
		{"plan/Read allow", ModePlan, "Read", "/any/file", DecisionAllow},
		{"plan/Glob allow", ModePlan, "Glob", "**/*.go", DecisionAllow},
		{"plan/Edit ask", ModePlan, "Edit", "/any/file", DecisionAsk},
		{"plan/Bash git status allow", ModePlan, "Bash", "git status", DecisionAllow},
		{"plan/Bash npm install ask", ModePlan, "Bash", "npm install", DecisionAsk},
		// default — read-only auto-allow, mutating asks.
		{"default/Read allow", ModeDefault, "Read", "/any/file", DecisionAllow},
		{"default/Bash git log allow", ModeDefault, "Bash", "git log", DecisionAllow},
		{"default/Bash npm install ask", ModeDefault, "Bash", "npm install", DecisionAsk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := New("", nil, tt.mode, nil, nil, nil)
			got := g.Check(tt.tool, tt.input)
			if got != tt.want {
				t.Errorf("mode=%s tool=%s input=%q: got %v, want %v", tt.mode, tt.tool, tt.input, got, tt.want)
			}
		})
	}
}

// TestMatchRule_CrossPlatformPaths ensures path separator normalisation works.
func TestMatchRule_CrossPlatformPaths(t *testing.T) {
	tests := []struct {
		name      string
		rule      string
		tool      string
		input     string
		wantMatch bool
	}{
		// Backslash input should match a forward-slash pattern after normalisation.
		{
			name:      "Edit backslash input vs slash pattern",
			rule:      "Edit(//C:/Users/x/**)",
			tool:      "Edit",
			input:     `C:\Users\x\foo.go`,
			wantMatch: true,
		},
		// Regression: forward-slash input still matches forward-slash pattern.
		{
			name:      "Read forward-slash regression",
			rule:      "Read(//home/u/**)",
			tool:      "Read",
			input:     "/home/u/sub/file",
			wantMatch: true,
		},
		// Bash patterns must NOT have slashes mangled.
		{
			name:      "Bash git log no mangle",
			rule:      "Bash(git log:*)",
			tool:      "Bash",
			input:     "git log /tmp/foo",
			wantMatch: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchRule(tt.rule, tt.tool, tt.input)
			if got != tt.wantMatch {
				t.Errorf("matchRule(%q, %q, %q) = %v; want %v", tt.rule, tt.tool, tt.input, got, tt.wantMatch)
			}
		})
	}
}

// TestSuggestRule_UnknownBin ensures unknown binaries get a bin-only prefix
// (producing Bash(bin:*)) and known read-only commands still use readonly:*.
func TestSuggestRule_UnknownBin(t *testing.T) {
	tests := []struct {
		tool, input, want string
	}{
		// Unknown binary with argument → Bash(bin:*)
		{"Bash", "playwright test", "Bash(playwright:*)"},
		// rg is read-only; takes priority over prefix extraction.
		{"Bash", "rg foo .", "Bash(readonly:*)"},
	}
	for _, tt := range tests {
		got := SuggestRule(tt.tool, tt.input)
		if got != tt.want {
			t.Errorf("SuggestRule(%q, %q) = %q; want %q", tt.tool, tt.input, got, tt.want)
		}
	}
}
