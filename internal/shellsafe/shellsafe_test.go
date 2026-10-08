package shellsafe

import (
	"strings"
	"testing"
)

func TestIsReadOnly(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		// A chain from a bug report: the strict policy refuses every
		// redirect, /dev/null included, so it prompts. Without the redirect,
		// the same chain is read-only.
		{"cd chain with echo and discard redirect", `cd web/src/tabs && ls ServerSettingsTab/ && echo "===" && ls BattlepassTab/ 2>/dev/null`, false},
		{"cd chain with echo", `cd web/src/tabs && ls ServerSettingsTab/ && echo "===" && ls BattlepassTab/`, true},

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
		{"semicolon chain", "pwd ; ls", true},
		{"leading cd", "cd src && ls", true},

		// Pipes prompt: they hand one command's output to another.
		{"pipe between read-only", "ls -la | grep foo", false},
		{"nested cd then pipe", "cd src && ls | grep go", false},

		// Redirects prompt, including discards and fd dups.
		{"stderr to dev null", "ls 2>/dev/null", false},
		{"stdout to dev null", "grep x file >/dev/null", false},
		{"fd dup 2>&1", "ls 2>&1", false},
		{"both to dev null", "find . -name x >/dev/null 2>&1", false},

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

func TestIsFileDump(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"bare cat", "cat file.txt", true},
		{"bare head", "head -20 file.txt", true},
		{"bare tail", "tail -f log.txt", true},
		{"bare less", "less file.txt", true},
		{"bare more", "more file.txt", true},
		{"bare bat", "bat file.txt", true},
		{"bare ls not a dump", "ls -la", false},
		{"chained cd and cat not caught by bare check", "cd src && cat file.txt", false},
		{"pipe not caught by bare check", "cat file.txt | grep foo", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsFileDump(tt.cmd); got != tt.want {
				t.Errorf("IsFileDump(%q) = %v, want %v", tt.cmd, got, tt.want)
			}
		})
	}
}

func TestContainsFileDump(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"bare cat", "cat file.txt", true},
		{"cd then cat", "cd web/src/tabs && cat file.go", true},
		{"cd then cat semicolon", "cd src; cat file.go", true},
		{"cat piped through grep", "cat file.txt | grep foo", true},
		{"deep chain with tail at the end", "cd src && ls && tail -20 file.go", true},
		{"or fallback into cat", "cd nope || cat fallback.txt", true},
		{"ls only, no dump", "cd src && ls", false},
		{"git status only", "git status", false},
		{"unrelated compound", "git status && go vet ./...", false},
		{"empty", "", false},
		{"cat as substring of another binary is not matched", "concatenate file.txt", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContainsFileDump(tt.cmd); got != tt.want {
				t.Errorf("ContainsFileDump(%q) = %v, want %v", tt.cmd, got, tt.want)
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
	for _, seed := range []string{
		"ls && pwd",
		"cd x && ls 2>/dev/null",
		"echo $(rm -rf /)",
		"",
		"git branch -D main",
		"env -S 'rm -rf /' ls",
		"find . '-exec' rm {} +",
		`find . -de\lete`,
		"git diff --{ext-diff,stat}",
		"rg foo *",
		"sort --outp=/etc/hosts x",
		"timeout -k5 10 nice -n 3 git --no-pager log",
		"(cd sub && ls) && git stash show -p stash@{1}",
		"echo x >&file",
		"export PATH=.:$PATH; ls",
		"echo $((a))",
		"cd 0\v0",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		// Must never panic and must be deterministic.
		got := IsReadOnly(cmd)
		if got != IsReadOnly(cmd) {
			t.Errorf("IsReadOnly(%q) not deterministic", cmd)
		}
		// Permission rules are matched per segment, so a read-only command
		// must be read-only in every segment it splits into. The printer
		// that renders segments turns some control characters (\v, \f)
		// into spaces, so those inputs do not round-trip and are skipped.
		if got && !strings.ContainsFunc(cmd, isControlNotSpace) {
			segs, ok := SplitForPermissions(cmd)
			if !ok {
				t.Fatalf("IsReadOnly(%q) = true but SplitForPermissions failed", cmd)
			}
			for _, seg := range segs {
				if !IsReadOnly(seg) {
					t.Errorf("IsReadOnly(%q) = true but segment %q is not read-only", cmd, seg)
				}
			}
		}
		_ = HasUnsafeConstructs(cmd)
		_, _ = StripLeadingCd(cmd)
		_ = IsFileDump(cmd)
		_ = ContainsFileDump(cmd)
	})
}

// isControlNotSpace reports an ASCII control character other than the
// newline and tab the shell grammar uses.
func isControlNotSpace(r rune) bool {
	return r < 0x20 && r != '\n' && r != '\t'
}
