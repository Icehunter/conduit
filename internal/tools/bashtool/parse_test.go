package bashtool

import (
	"testing"
)

func TestHasShellMetachars(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		// Benign: no metacharacters
		{"ls -la", false},
		{"cat foo.txt", false},
		{"git log --oneline", false},
		{"git status", false},
		{"echo hello", false},
		{"FOO=bar cat file.txt", false},
		// Protected by single-quotes
		{"git log --format='%H %s'", false},
		{"echo 'foo; bar'", false},
		{"echo 'foo && bar'", false},
		{"echo 'foo > bar'", false},
		// Protected by double-quotes
		{`echo "foo; bar"`, false},
		{`echo "foo && bar"`, false},
		// Backslash escape keeps next char from being a metachar
		{`echo foo\;bar`, false},
		{`echo foo\|bar`, false},

		// Dangerous: command separators
		{"cat foo; rm -rf bar", true},
		{"cat foo && rm bar", true},
		{"cat foo || rm bar", true},
		{"cat foo | rm bar", true},
		{"cat foo & rm bar", true},
		{"cat foo\nrm bar", true},

		// Dangerous: command substitution
		{"cat $(rm bar)", true},
		{"cat `rm bar`", true},
		{"FOO=$(rm evil) cat file", true},
		{`echo "result: $(rm -rf /tmp/victim)"`, true},
		{`echo "result: ` + "`rm evil`" + `"`, true},

		// Dangerous: output redirection
		{"cat foo > /dev/null", true},
		{"cat foo >> bar.txt", true},
		{"cat foo 2>&1", true},

		// Dangerous: heredoc
		{"cat << EOF", true},
	}

	for _, tc := range cases {
		got := hasShellMetachars(tc.cmd)
		if got != tc.want {
			t.Errorf("hasShellMetachars(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

func TestIsReadOnlyCommandMetachars(t *testing.T) {
	// These were previously incorrectly reported as read-only.
	unsafe := []string{
		"cat foo; rm -rf bar",
		"cat foo && rm bar",
		"cat $(rm bar)",
		"cat `rm bar`",
		"FOO=$(rm evil) cat file",
		"echo hello > /tmp/evil",
	}
	for _, cmd := range unsafe {
		if isReadOnlyCommand(cmd) {
			t.Errorf("isReadOnlyCommand(%q) = true, want false (should NOT auto-approve)", cmd)
		}
	}

	// These should still be considered read-only.
	safe := []string{
		"git log --oneline",
		"git status",
		"cat foo.txt",
		"ls -la",
		"grep -r foo .",
		"git log --format='%H %s'",
	}
	for _, cmd := range safe {
		if !isReadOnlyCommand(cmd) {
			t.Errorf("isReadOnlyCommand(%q) = false, want true (should auto-approve)", cmd)
		}
	}
}

func FuzzHasShellMetachars(f *testing.F) {
	seeds := []string{
		"ls -la",
		"cat foo; rm bar",
		"echo $(whoami)",
		"git log --format='%H %s'",
		`echo "hello; world"`,
		"cat foo 2>&1",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		// Must not panic.
		_ = hasShellMetachars(cmd)
		_ = isReadOnlyCommand(cmd)
	})
}
