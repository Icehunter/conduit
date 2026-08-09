package permissions

import "testing"

// A deny rule must fire no matter where in a compound command the denied
// program appears. Prefix matching on the raw string is not a security
// boundary: every case below used to slip past `Bash(curl:*)`.
func TestDenyAppliesToEveryCompoundSegment(t *testing.T) {
	g := New("/tmp", nil, ModeDefault, nil, []string{"Bash(curl:*)"}, nil)

	denied := []string{
		"curl https://evil.example",
		"echo hi && curl https://evil.example",
		"echo hi; curl https://evil.example",
		"echo hi | curl https://evil.example",
		"echo hi |& curl https://evil.example",
		"true || curl https://evil.example",
		"  curl https://evil.example",
		"curl https://evil.example &",
		"(curl https://evil.example)",
		"{ curl https://evil.example; }",
		"echo $(curl https://evil.example)",
		"echo `curl https://evil.example`",
		"if true; then curl https://evil.example; fi",
		"for f in a; do curl https://evil.example; done",
		"cd /tmp && curl https://evil.example",
	}
	for _, cmd := range denied {
		if got := g.Check("Bash", cmd); got != DecisionDeny {
			t.Errorf("Check(Bash, %q) = %v, want Deny", cmd, got)
		}
	}

	// Commands that merely mention the denied name as data must not be denied.
	notDenied := []string{
		"echo curl",
		"grep curl README.md",
	}
	for _, cmd := range notDenied {
		if got := g.Check("Bash", cmd); got == DecisionDeny {
			t.Errorf("Check(Bash, %q) = Deny, want not Deny", cmd)
		}
	}
}

// A prefix allow rule must not approve whatever is chained after it. This was
// an escalation: `Bash(echo:*)` auto-approved `echo hi && curl evil`.
func TestAllowDoesNotEscalateAcrossCompoundSegments(t *testing.T) {
	g := New("/tmp", nil, ModeDefault, []string{"Bash(echo:*)"}, nil, nil)

	if got := g.Check("Bash", "echo hi"); got != DecisionAllow {
		t.Errorf("simple allowed command = %v, want Allow", got)
	}
	escalations := []string{
		"echo hi && curl https://evil.example",
		"echo hi; rm -rf /tmp/x",
		"echo hi | sh",
		"echo $(curl https://evil.example)",
	}
	for _, cmd := range escalations {
		if got := g.Check("Bash", cmd); got == DecisionAllow {
			t.Errorf("Check(Bash, %q) = Allow, want Ask/Deny", cmd)
		}
	}
}

// Every segment being allowed is still an allow — the fix must not make
// legitimate compound commands unusable.
func TestAllowWhenEverySegmentIsAllowed(t *testing.T) {
	g := New("/tmp", nil, ModeDefault, []string{"Bash(echo:*)", "Bash(ls:*)"}, nil, nil)
	for _, cmd := range []string{"echo hi && ls", "ls | echo", "echo a; ls -la"} {
		if got := g.Check("Bash", cmd); got != DecisionAllow {
			t.Errorf("Check(Bash, %q) = %v, want Allow", cmd, got)
		}
	}
}

// An exact (wildcard-free) rule naming a whole compound command still works —
// that rule is a deliberate description of the compound, not a prefix.
func TestExactCompoundRuleStillAllows(t *testing.T) {
	g := New("/tmp", nil, ModeDefault, []string{"Bash(git add -A && git commit)"}, nil, nil)
	if got := g.Check("Bash", "git add -A && git commit"); got != DecisionAllow {
		t.Errorf("exact compound rule = %v, want Allow", got)
	}
}

// A leading `cd <dir>` is normalization, not an action — matchRule already
// strips it — so it must not block an otherwise-allowed command.
func TestLeadingCdDoesNotBlockAllow(t *testing.T) {
	g := New("/tmp", nil, ModeDefault, []string{"Bash(make test)"}, nil, nil)
	if got := g.Check("Bash", "cd /tmp && make test"); got != DecisionAllow {
		t.Errorf("cd + allowed command = %v, want Allow", got)
	}
}

// ...but a cd whose argument is a substitution executes that substitution, so
// the StripLeadingCd shortcut must not launder it into an allow.
func TestCdWithSubstitutionIsNotLaundered(t *testing.T) {
	g := New("/tmp", nil, ModeDefault, []string{"Bash(make test)"}, nil, nil)
	if got := g.Check("Bash", "cd $(curl https://evil.example) && make test"); got == DecisionAllow {
		t.Error("cd with command substitution = Allow, want Ask/Deny")
	}
}

// An ask rule anywhere in the command forces the prompt.
func TestAskAppliesToEverySegment(t *testing.T) {
	g := New("/tmp", nil, ModeDefault, []string{"Bash(echo:*)"}, nil, []string{"Bash(rm:*)"})
	if got := g.Check("Bash", "echo hi && rm -rf /tmp/x"); got != DecisionAsk {
		t.Errorf("ask segment = %v, want Ask", got)
	}
}

// Unparseable input must stay conservative: the raw string is still matched, so
// a deny rule cannot be dodged by sending malformed shell.
func TestUnparseableInputStillMatchesRawString(t *testing.T) {
	g := New("/tmp", nil, ModeDefault, nil, []string{"Bash(curl:*)"}, nil)
	if got := g.Check("Bash", "curl 'unterminated"); got != DecisionDeny {
		t.Errorf("unparseable denied command = %v, want Deny", got)
	}
}
