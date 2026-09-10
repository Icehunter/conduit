package workflow

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-q", "-m", "init")
	return dir
}

func TestWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	ctx := context.Background()

	t.Run("clean worktree is removed", func(t *testing.T) {
		repo := initRepo(t)
		wt, err := NewWorktree(ctx, repo, "run/1", 3)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(wt.Path, "a.txt")); err != nil {
			t.Fatalf("worktree missing checkout: %v", err)
		}
		wantParent, _ := filepath.EvalSymlinks(repo)
		gotParent, _ := filepath.EvalSymlinks(filepath.Dir(filepath.Dir(filepath.Dir(wt.Path))))
		if gotParent != wantParent || filepath.Base(filepath.Dir(wt.Path)) != "worktrees" {
			t.Errorf("worktree at %s, want under <repo>/.conduit/worktrees", wt.Path)
		}
		kept, err := wt.Finish(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kept != "" {
			t.Errorf("clean worktree kept at %s", kept)
		}
		if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
			t.Errorf("worktree dir still exists: %v", err)
		}
	})

	t.Run("dirty worktree is kept", func(t *testing.T) {
		repo := initRepo(t)
		wt, err := NewWorktree(ctx, repo, "run-2", 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt.Path, "a.txt"), []byte("changed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		kept, err := wt.Finish(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if kept != wt.Path {
			t.Errorf("kept = %q, want %q", kept, wt.Path)
		}
		// The main checkout is untouched.
		b, _ := os.ReadFile(filepath.Join(repo, "a.txt"))
		if string(b) != "a\n" {
			t.Errorf("main checkout modified: %q", b)
		}
		if err := wt.Remove(ctx, true); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("not a repo", func(t *testing.T) {
		if _, err := NewWorktree(ctx, t.TempDir(), "r", 1); err == nil {
			t.Error("expected error outside a git repository")
		}
	})
}
