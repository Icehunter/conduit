package bashtool

import "testing"

// Shell-safety classification tests live in internal/shellsafe/shellsafe_test.go.
// This file only tests the thin shims in this package to ensure wiring is correct.

func TestHasShellMetachars(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		// Single commands — not unsafe constructs
		{"ls -la", false},
		{"cat foo.txt", false},
		{"git log --oneline", false},
		{"git status", false},
		{"echo hello", false},
		// Quoted metachars are not constructs
		{"git log --format='%H %s'", false},
		{"echo 'foo; bar'", false},
		{`echo "foo && bar"`, false},
		// Safe discard redirect
		{"ls 2>/dev/null", false},
		{"ls 2>&1", false},

		// Unsafe: chains
		{"cat foo; rm -rf bar", true},
		{"cat foo && rm bar", true},
		{"cat foo || rm bar", true},
		{"cat foo | rm bar", true},
		{"cat foo & rm bar", true},
		// Unsafe: substitution
		{"cat $(rm bar)", true},
		{"cat `rm bar`", true},
		{`echo "result: $(rm -rf /)"`, true},
		// Unsafe: redirect to real file
		{"echo x > out.txt", true},
		{"echo x >> bar.txt", true},
		// Unsafe: heredoc
		{"cat << EOF", true},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := hasShellMetachars(tt.cmd); got != tt.want {
				t.Errorf("hasShellMetachars(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestIsReadOnlyCommand(t *testing.T) {
	unsafe := []string{
		"cat foo; rm -rf bar",
		"cat foo && rm bar",
		"cat $(rm bar)",
		"cat `rm bar`",
		"echo hello > /tmp/evil",
		"git push origin main",
		"rm -rf /tmp/x",
	}
	for _, cmd := range unsafe {
		if isReadOnlyCommand(cmd) {
			t.Errorf("isReadOnlyCommand(%q) = true, want false", cmd)
		}
	}

	safe := []string{
		"git log --oneline",
		"git status",
		"cat foo.txt",
		"ls -la",
		"grep -r foo .",
		"git log --format='%H %s'",
		"ls 2>/dev/null",
		"ls && pwd",
		"cd src && ls",
	}
	for _, cmd := range safe {
		if !isReadOnlyCommand(cmd) {
			t.Errorf("isReadOnlyCommand(%q) = false, want true", cmd)
		}
	}
}

func FuzzHasShellMetachars(f *testing.F) {
	f.Add("ls -la")
	f.Add("cat foo; rm bar")
	f.Add("echo $(whoami)")
	f.Add("git log --format='%H %s'")
	f.Add(`echo "hello; world"`)
	f.Add("cat foo 2>&1")
	f.Fuzz(func(t *testing.T, cmd string) {
		_ = hasShellMetachars(cmd)
		_ = isReadOnlyCommand(cmd)
	})
}
