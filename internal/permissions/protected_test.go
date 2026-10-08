package permissions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/icehunter/conduit/internal/settings"
)

func TestGate_Check_ProtectedWritePaths(t *testing.T) {
	tests := []struct {
		name  string
		mode  Mode
		tool  string
		input string
		allow []string
		want  Decision
	}{
		{"acceptEdits Write .git/config asks", ModeAcceptEdits, "Write", ".git/config", nil, DecisionAsk},
		{"acceptEdits Write hook asks", ModeAcceptEdits, "Write", ".git/hooks/pre-commit", nil, DecisionAsk},
		{"acceptEdits Write src file allowed", ModeAcceptEdits, "Write", "src/x.go", nil, DecisionAllow},
		{"case-insensitive component asks", ModeAcceptEdits, "Write", "./.GIT/config", nil, DecisionAsk},
		{"parent traversal asks", ModeAcceptEdits, "Edit", "../repo/.git/config", nil, DecisionAsk},
		{"absolute path asks", ModeAcceptEdits, "Edit", "/work/repo/.git/config", nil, DecisionAsk},
		{"traversal into src is clean", ModeAcceptEdits, "Edit", "a/.git/../x.go", nil, DecisionAllow},
		{"zshrc asks", ModeAcceptEdits, "Write", "/home/u/.zshrc", nil, DecisionAsk},
		{"mcp json asks", ModeAcceptEdits, "Write", ".mcp.json", nil, DecisionAsk},
		{"vscode dir asks", ModeAcceptEdits, "Write", ".vscode/tasks.json", nil, DecisionAsk},
		{"claude commands asks", ModeAcceptEdits, "Write", ".claude/commands/x.md", nil, DecisionAsk},
		{"claude agents asks", ModeAcceptEdits, "Write", ".claude/agents/x.md", nil, DecisionAsk},
		{"claude settings asks", ModeAcceptEdits, "Edit", ".claude/settings.local.json", nil, DecisionAsk},
		{"conduit settings asks", ModeAcceptEdits, "Edit", "/home/u/.conduit/settings.json", nil, DecisionAsk},
		{"claude other file allowed", ModeAcceptEdits, "Write", ".claude/notes.md", nil, DecisionAllow},
		{"MultiEdit asks", ModeAcceptEdits, "MultiEdit", ".git/config", nil, DecisionAsk},
		{"NotebookEdit asks", ModeAcceptEdits, "NotebookEdit", ".idea/n.ipynb", nil, DecisionAsk},
		{"HashEdit asks", ModeAcceptEdits, "HashEdit", ".bashrc", nil, DecisionAsk},
		{"allow rule does not promote", ModeAcceptEdits, "Write", ".git/config", []string{"Write"}, DecisionAsk},
		{"default mode allow rule does not promote", ModeDefault, "Write", ".gitconfig", []string{"Write"}, DecisionAsk},
		{"plan mode denies", ModePlan, "Write", ".git/config", nil, DecisionDeny},
		{"council mode denies", ModeCouncil, "Write", ".git/config", nil, DecisionDeny},
		{"bypass allows", ModeBypassPermissions, "Write", ".git/config", nil, DecisionAllow},
		{"conduit.json asks", ModeAcceptEdits, "Write", "/home/u/.conduit/conduit.json", nil, DecisionAsk},
		{"conduit mcp.json asks", ModeAcceptEdits, "Write", "/home/u/.conduit/mcp.json", nil, DecisionAsk},
		{"conduit agents asks", ModeAcceptEdits, "Write", "/home/u/.conduit/agents/a.md", nil, DecisionAsk},
		{"conduit commands asks", ModeAcceptEdits, "Write", ".conduit/commands/c.md", nil, DecisionAsk},
		{".claude.json asks", ModeAcceptEdits, "Write", "/home/u/.claude.json", nil, DecisionAsk},
		{"git config dir asks", ModeAcceptEdits, "Write", "/home/u/.config/git/config", nil, DecisionAsk},
		{"zshenv inside cwd asks", ModeAcceptEdits, "Write", "/work/repo/.zshenv", nil, DecisionAsk},
		{"zshenv asks", ModeAcceptEdits, "Write", "/home/u/.zshenv", nil, DecisionAsk},
		{"husky hook asks", ModeAcceptEdits, "Write", ".husky/pre-commit", nil, DecisionAsk},
		{"githooks asks", ModeAcceptEdits, "Write", ".githooks/pre-commit", nil, DecisionAsk},
		{"outside cwd ssh asks", ModeAcceptEdits, "Write", "/home/u/.ssh/authorized_keys", nil, DecisionAsk},
		{"outside cwd LaunchAgents asks", ModeAcceptEdits, "Write", "/home/u/Library/LaunchAgents/x.plist", nil, DecisionAsk},
		{"relative escape outside cwd asks", ModeAcceptEdits, "Write", "../other/x.go", nil, DecisionAsk},
		{"inside cwd absolute allowed", ModeAcceptEdits, "Write", "/work/repo/src/x.go", nil, DecisionAllow},
		{"trailing dot component asks", ModeAcceptEdits, "Write", ".git./config", nil, DecisionAsk},
		{"trailing space component asks", ModeAcceptEdits, "Write", ".git /config", nil, DecisionAsk},
		{"read of .git still allowed", ModeDefault, "Read", ".git/config", nil, DecisionAllow},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := New("/work/repo", nil, tc.mode, tc.allow, nil, nil)
			if got := g.Check(tc.tool, tc.input); got != tc.want {
				t.Errorf("Check(%s, %q) in %v = %v, want %v", tc.tool, tc.input, tc.mode, got, tc.want)
			}
		})
	}
}

func TestGate_Check_DenyBeatsTrustShortcuts(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		input string
		roots []string
		deny  []string
	}{
		{"trusted root read", "Read", "/trusted/proj/.env", []string{"/trusted"}, []string{"Read(/trusted/proj/.env)"}},
		{"trusted root read tool-wide deny", "Read", "/trusted/proj/a.txt", []string{"/trusted"}, []string{"Read"}},
		{"implicit trust path", "Read", implicitPathForTest("/work/repo"), nil, []string{"Read"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := New("/work/repo", tc.roots, ModeDefault, nil, tc.deny, nil)
			if got := g.Check(tc.tool, tc.input); got != DecisionDeny {
				t.Errorf("Check(%s, %q) = %v, want Deny", tc.tool, tc.input, got)
			}
		})
	}
}

func TestGate_Check_TrustedRootReadStillAllowedWithoutDeny(t *testing.T) {
	g := New("/work/repo", []string{"/trusted"}, ModeDefault, nil, []string{"Read(/other)"}, nil)
	if got := g.Check("Read", "/trusted/proj/a.txt"); got != DecisionAllow {
		t.Errorf("got %v, want Allow", got)
	}
}

// implicitPathForTest returns a path inside conduit's per-project storage for cwd.
func implicitPathForTest(cwd string) string {
	return filepath.Join(settings.ConduitDir(), "projects", sanitizeCWD(cwd), "memory", "m.md")
}

func TestGate_Check_SymlinkedProtectedDir(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".git", filepath.Join(repo, "g")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo, "escape")); err != nil {
		t.Fatal(err)
	}
	g := New(repo, nil, ModeAcceptEdits, nil, nil, nil)
	tests := []struct {
		name, input string
		want        Decision
	}{
		{"symlink to .git, new leaf", "g/config", DecisionAsk},
		{"symlink to .git, hooks", filepath.Join(repo, "g", "hooks", "pre-commit"), DecisionAsk},
		{"symlink out of cwd asks", "escape/x.go", DecisionAsk},
		{"regular file allowed", "src/x.go", DecisionAllow},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := g.Check("Write", tc.input); got != tc.want {
				t.Errorf("Check(Write, %q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
	if !g.IsProtectedWrite("Write", "g/config") || g.IsProtectedWrite("Write", "src/x.go") || g.IsProtectedWrite("Read", ".git/config") {
		t.Error("IsProtectedWrite classification wrong")
	}
}

func TestMatchesProtected_WindowsShortNames(t *testing.T) {
	old := shortNamesApply
	t.Cleanup(func() { shortNamesApply = old })
	tests := []struct {
		name  string
		apply bool
		path  string
		want  bool
	}{
		{"8.3 git on windows", true, "repo/GIT~1/config", true},
		{"8.3 ignored elsewhere", false, "repo/GIT~1/config", false},
		{"plain path on windows", true, "repo/src/x.go", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			shortNamesApply = tc.apply
			if got := matchesProtected(tc.path); got != tc.want {
				t.Errorf("matchesProtected(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

// "link/.." is resolved by the OS through the link but dropped by a lexical
// clean, so such writes must prompt.
func TestGate_Check_DotDotAfterSymlink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	outsideSub := filepath.Join(root, "outside", "sub")
	for _, d := range []string{filepath.Join(repo, "real"), outsideSub} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outsideSub, filepath.Join(repo, "g")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	g := New(repo, nil, ModeAcceptEdits, nil, nil, nil)
	tests := []struct {
		name, input string
		want        Decision
	}{
		{"relative dotdot after link", "g/../.ssh/authorized_keys", DecisionAsk},
		{"absolute dotdot after link", filepath.Join(repo, "g") + "/../x.go", DecisionAsk},
		{"dotdot after real dir allowed", "real/../src/x.go", DecisionAllow},
		{"plain path allowed", "src/x.go", DecisionAllow},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, tool := range []string{"Write", "Edit", "HashEdit"} {
				if got := g.Check(tool, tc.input); got != tc.want {
					t.Errorf("Check(%s, %q) = %v, want %v", tool, tc.input, got, tc.want)
				}
			}
		})
	}
}
