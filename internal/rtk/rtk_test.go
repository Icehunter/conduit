package rtk

import "testing"

func TestFilter_NoMatch(t *testing.T) {
	r := Filter("echo hello", "hello")
	if r.Filtered != "hello" {
		t.Errorf("unmatched command should pass through; got %q", r.Filtered)
	}
	if r.Category != "" {
		t.Errorf("unmatched command should have empty category; got %q", r.Category)
	}
	if r.SavedBytes != 0 {
		t.Errorf("unmatched command should save 0 bytes; got %d", r.SavedBytes)
	}
}

func TestFilter_StripANSI(t *testing.T) {
	r := Filter("echo hello", "\x1b[32mhello\x1b[0m")
	if r.Filtered != "hello" {
		t.Errorf("ANSI should be stripped before filter; got %q", r.Filtered)
	}
}

func TestFilter_GitStatus(t *testing.T) {
	out := `On branch main
Your branch is up to date with 'origin/main'.

nothing to commit, working tree clean`
	r := Filter("git status", out)
	if r.Category != "Git" {
		t.Errorf("expected Git category; got %q", r.Category)
	}
	if r.Filtered == "" {
		t.Error("filtered output should not be empty")
	}
}

func TestFilter_GitLog(t *testing.T) {
	out := `commit abc1234def5678901234567890123456789abcdef
Author: Alice <alice@example.com>
Date:   Mon Jan 1 00:00:00 2024 +0000

    Add feature foo

commit bcd2345ef6789012345678901234567890abcdef
Author: Bob <bob@example.com>
Date:   Sun Dec 31 00:00:00 2023 +0000

    Fix bug bar
`
	r := Filter("git log", out)
	if r.Category != "Git" {
		t.Errorf("expected Git; got %q", r.Category)
	}
	if len(r.Filtered) >= len(out) {
		t.Error("git log should be compressed")
	}
}

func TestFilter_EnvVarStripping(t *testing.T) {
	// FOO=bar git status should match as git
	r := Filter("FOO=bar git status", "On branch main\nnothing to commit, working tree clean")
	if r.Category != "Git" {
		t.Errorf("env-prefixed command should still match git; got %q", r.Category)
	}
}

func TestFilter_GoTest_PassingOnly(t *testing.T) {
	out := `=== RUN   TestFoo
--- PASS: TestFoo (0.00s)
=== RUN   TestBar
--- PASS: TestBar (0.00s)
PASS
ok  	example.com/pkg	0.001s`
	r := Filter("go test ./...", out)
	if r.Category != "Go" {
		t.Errorf("expected Go; got %q", r.Category)
	}
	// Passing-only output should be compressed to the summary line
	if len(r.Filtered) >= len(out) {
		t.Error("passing go test should be compressed")
	}
}

func TestFilter_GoTest_WithFailure(t *testing.T) {
	out := `=== RUN   TestFoo
--- FAIL: TestFoo (0.00s)
    foo_test.go:10: expected 1 got 2
FAIL
FAIL	example.com/pkg	0.001s`
	r := Filter("go test ./...", out)
	if r.Category != "Go" {
		t.Errorf("expected Go; got %q", r.Category)
	}
	if !contains(r.Filtered, "FAIL") {
		t.Error("failure output should be preserved")
	}
}

func TestFilter_Pytest_Failures(t *testing.T) {
	out := `============================= test session starts ==============================
collected 3 items

test_foo.py::test_bar PASSED
test_foo.py::test_baz FAILED

=================================== FAILURES ===================================
______________________________ test_baz ______________________________________
    assert 1 == 2
E   AssertionError: assert 1 == 2
============================== short test summary info =========================
FAILED test_foo.py::test_baz - AssertionError: assert 1 == 2
========================= 1 failed, 1 passed in 0.01s =========================`
	r := Filter("pytest", out)
	if r.Category != "Tests" {
		t.Errorf("expected Tests; got %q", r.Category)
	}
	if !contains(r.Filtered, "FAILED") {
		t.Error("failure should be preserved")
	}
}

func TestFilter_LSOutput(t *testing.T) {
	var lines []string
	for i := 0; i < 300; i++ {
		lines = append(lines, "file.txt")
	}
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	r := Filter("ls -la", out)
	if r.Category != "Files" {
		t.Errorf("expected Files; got %q", r.Category)
	}
	if len(r.Filtered) >= len(out) {
		t.Error("ls with 300 files should be truncated")
	}
}

func TestFilter_SavingsMetrics(t *testing.T) {
	// Build a large git log output that will definitely be compressed
	out := ""
	for i := 0; i < 100; i++ {
		out += "commit abc1234def5678901234567890123456789abcdef\n"
		out += "Author: Alice <alice@example.com>\n"
		out += "Date:   Mon Jan 1 00:00:00 2024 +0000\n\n"
		out += "    Add feature\n\n"
	}
	r := Filter("git log", out)
	if r.SavedBytes <= 0 {
		t.Errorf("expected positive savings; got %d", r.SavedBytes)
	}
	if r.SavingsPct <= 0 {
		t.Errorf("expected positive savings pct; got %.1f", r.SavingsPct)
	}
}

func TestFilter_AWSSecretsRedacted(t *testing.T) {
	out := `{
    "AccessKeyId": "AKIAIOSFODNN7EXAMPLE",
    "SecretAccessKey": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
    "SessionToken": "..."
}`
	r := Filter("aws sts get-caller-identity", out)
	if r.Category != "Infra" {
		t.Errorf("expected Infra; got %q", r.Category)
	}
	if contains(r.Filtered, "AKIAIOSFODNN7EXAMPLE") {
		t.Error("AWS access key should be redacted")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
