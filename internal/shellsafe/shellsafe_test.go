package shellsafe

import "testing"

func TestIsReadOnly(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		// The exact command from the bug report.
		{"cd chain with echo and discard redirect", `cd web/src/tabs && ls ServerSettingsTab/ && echo "===" && ls BattlepassTab/ 2>/dev/null`, true},

		// Simple read-only commands.
		{"bare ls", "ls", true},
		{"ls with path", "ls /tmp", true},
		{"cat file", "cat README.md", true},
		{"pwd", "pwd", true},
		{"echo", `echo "hello world"`, true},
		{"grep", "grep -rn foo .", true},
		{"git status", "git status", true},
		{"git log oneline", "git log --oneline -10", true},
		{"go version", "go version", true},

		// Compound read-only.
		{"and chain", "ls && pwd && cat x", true},
		{"or fallback", "cat a || cat b", true},
		{"pipe between read-only", "ls -la | grep foo", true},
		{"semicolon chain", "pwd ; ls", true},
		{"leading cd", "cd src && ls", true},
		{"nested cd then pipe", "cd src && ls | grep go", true},

		// Safe redirects.
		{"stderr to dev null", "ls 2>/dev/null", true},
		{"stdout to dev null", "grep x file >/dev/null", true},
		{"fd dup 2>&1", "ls 2>&1", true},
		{"both to dev null", "find . -name x >/dev/null 2>&1", true},

		// Unsafe: mutation / execution.
		{"rm", "rm -rf /tmp/x", false},
		{"git push", "git push origin main", false},
		{"git commit", "git commit -m x", false},
		{"go build", "go build ./...", false},
		{"npm install", "npm install", false},
		{"sed in place", "sed -i s/a/b/ file", false},
		{"sed plain", "sed s/a/b/ file", false},
		{"find with exec", "find . -name x -exec rm {} ;", false},
		{"find with delete", "find . -delete", false},

		// Unsafe: redirect to real file.
		{"redirect to file", "echo x > out.txt", false},
		{"append to file", "echo x >> out.txt", false},
		{"and chain redirect to file", "ls && echo x > out.txt", false},

		// Unsafe: substitution / execution.
		{"command substitution", "echo $(rm -rf /)", false},
		{"backticks", "echo `whoami`", false},
		{"process substitution", "diff <(ls a) <(ls b)", false},

		// Unsafe: mixed read-only with one mutating command.
		{"chain ends in write", "ls && rm x", false},
		{"pipe into write", "cat x | tee out.txt", false},

		// Edge.
		{"empty", "", false},
		{"whitespace", "   ", false},
		{"unknown binary", "frobnicate --all", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsReadOnly(tt.cmd); got != tt.want {
				t.Errorf("IsReadOnly(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestHasUnsafeConstructs(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool // true = unsafe (has metachars / not a single bare command)
	}{
		{"bare command", "ls -la", false},
		{"command with flags", "grep -rn foo .", false},
		{"stderr discard", "ls 2>/dev/null", false},
		{"fd dup", "ls 2>&1", false},

		{"and chain", "ls && pwd", true},
		{"or chain", "ls || pwd", true},
		{"pipe", "ls | grep x", true},
		{"semicolon", "ls ; pwd", true},
		{"background", "sleep 1 &", true},
		{"redirect to file", "echo x > out", true},
		{"append to file", "echo x >> out", true},
		{"command substitution", "echo $(date)", true},
		{"backticks", "echo `date`", true},
		{"process substitution", "cat <(ls)", true},
		{"heredoc", "cat <<EOF\nx\nEOF", true},

		{"parse error", "ls &&", true},
		{"empty", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasUnsafeConstructs(tt.cmd); got != tt.want {
				t.Errorf("HasUnsafeConstructs(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestStripLeadingCd(t *testing.T) {
	tests := []struct {
		name     string
		cmd      string
		wantRest string
		wantOK   bool
	}{
		{"cd and ls", "cd src && ls", "ls", true},
		{"cd and chain", "cd src && ls -la", "ls -la", true},
		{"cd or fallback", "cd src || pwd", "pwd", true},
		{"no cd", "ls && pwd", "", false},
		{"bare command", "ls", "", false},
		{"cd alone", "cd src", "", false},
		{"cd with extra arg not stripped", "cd a b && ls", "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rest, ok := StripLeadingCd(tt.cmd)
			if ok != tt.wantOK {
				t.Fatalf("StripLeadingCd(%q) ok = %v, want %v", tt.cmd, ok, tt.wantOK)
			}
			if rest != tt.wantRest {
				t.Errorf("StripLeadingCd(%q) rest = %q, want %q", tt.cmd, rest, tt.wantRest)
			}
		})
	}
}

func FuzzIsReadOnly(f *testing.F) {
	f.Add("ls && pwd")
	f.Add("cd x && ls 2>/dev/null")
	f.Add("echo $(rm -rf /)")
	f.Add("")
	f.Fuzz(func(t *testing.T, cmd string) {
		// Must never panic and must be deterministic.
		got := IsReadOnly(cmd)
		if got != IsReadOnly(cmd) {
			t.Errorf("IsReadOnly(%q) not deterministic", cmd)
		}
		_ = HasUnsafeConstructs(cmd)
		_, _ = StripLeadingCd(cmd)
	})
}
