package workflow

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewWorktree_BasedOnCallersHEADInLinkedWorktree(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	run := func(dir string, args ...string) string {
		c := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false", "-c", "user.email=a@b", "-c", "user.name=a"}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(repo, "init", "-q", "-b", "main")
	run(repo, "commit", "-q", "--allow-empty", "-m", "main")
	linked := filepath.Join(t.TempDir(), "feat")
	run(repo, "worktree", "add", "-q", "-b", "feat", linked)
	run(linked, "commit", "-q", "--allow-empty", "-m", "feat work")
	featHead := run(linked, "rev-parse", "HEAD")
	mainHead := run(repo, "rev-parse", "HEAD")
	wt, err := NewWorktree(ctx, linked, "probe", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = wt.Remove(ctx, true) }()
	got := run(wt.Path, "rev-parse", "HEAD")
	t.Logf("feat=%s main=%s isolated=%s path=%s", featHead, mainHead, got, wt.Path)
	if got != featHead {
		t.Fatalf("isolated worktree based on %s, want caller's HEAD %s (main is %s)", got, featHead, mainHead)
	}
}
