package workflow

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func pruneRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "commit.gpgsign")
	t.Setenv("GIT_CONFIG_VALUE_0", "false")
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "commit", "-q", "--allow-empty", "-m", "i")
	return dir
}

func age(t *testing.T, root string) {
	t.Helper()
	old := time.Now().Add(-48 * time.Hour)
	_ = filepath.WalkDir(root, func(p string, _ fs.DirEntry, err error) error {
		if err == nil {
			_ = os.Chtimes(p, old, old) //nolint:gosec // test fixture under t.TempDir
		}
		return nil
	})
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("git", "--version")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

// relock re-locks w as if a conduit process with pid had created it.
func relock(t *testing.T, repo string, w *Worktree, pid int) {
	t.Helper()
	gitRun(t, repo, "worktree", "unlock", w.Path)
	gitRun(t, repo, "worktree", "lock", "--reason", fmt.Sprintf("conduit pid %d", pid), w.Path)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestPruneStale(t *testing.T) {
	repo := pruneRepo(t)
	ctx := context.Background()
	mk := func(seq int) *Worktree {
		w, err := NewWorktree(ctx, repo, "a", seq)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	removable, liveLocked, dirty, ignored, fresh, recentInside := mk(1), mk(2), mk(3), mk(4), mk(5), mk(6)

	// Ignored-only content must still protect a worktree.
	if err := os.WriteFile(filepath.Join(repo, ".git", "info", "exclude"), []byte("*.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ignored.Path, "secret.env"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirty.Path, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Not conduit's own name.
	foreign := filepath.Join(repo, ".conduit", worktreeDirName, "mine")
	gitRun(t, repo, "worktree", "add", "-q", "--detach", foreign, "HEAD")

	for _, w := range []*Worktree{removable, liveLocked, dirty, ignored, recentInside} {
		age(t, w.Path)
	}
	age(t, foreign)
	// Clean, but a file inside was touched recently while the top dir looks old.
	if err := os.Chtimes(filepath.Join(recentInside.Path, ".git"), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}

	// removable is owned by a dead pid; liveLocked keeps this process's lock.
	for _, w := range []*Worktree{removable, dirty, ignored, recentInside} {
		relock(t, repo, w, deadPID(t))
	}

	// A vanished conduit worktree and a vanished foreign one.
	gone := mk(7)
	gitRun(t, repo, "worktree", "unlock", gone.Path)
	if err := os.RemoveAll(gone.Path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere")
	gitRun(t, repo, "worktree", "add", "-q", "--detach", outside, "HEAD")
	if err := os.RemoveAll(outside); err != nil {
		t.Fatal(err)
	}

	PruneStale(ctx, repo, 24*time.Hour)

	if exists(removable.Path) {
		t.Error("old, clean, dead-pid worktree should be removed")
	}
	for name, w := range map[string]string{
		"live-locked": liveLocked.Path, "dirty": dirty.Path, "ignored": ignored.Path,
		"fresh": fresh.Path, "recent file inside": recentInside.Path, "foreign name": foreign,
	} {
		if !exists(w) {
			t.Errorf("%s worktree must survive", name)
		}
	}
	list := gitRun(t, repo, "worktree", "list", "--porcelain")
	if strings.Contains(list, gone.Path) {
		t.Error("admin entry of vanished conduit worktree should be dropped")
	}
	if !strings.Contains(list, outside) {
		t.Error("admin entry of a vanished non-conduit worktree must be left alone")
	}
}

func TestFinishUnlocksKept(t *testing.T) {
	repo := pruneRepo(t)
	ctx := context.Background()
	w, err := NewWorktree(ctx, repo, "k", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gitRun(t, repo, "worktree", "list", "--porcelain"), "locked conduit pid") {
		t.Fatal("new worktree should be locked with the pid")
	}
	if err := os.WriteFile(filepath.Join(w.Path, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if kept, err := w.Finish(ctx); err != nil || kept == "" {
		t.Fatalf("Finish = %q, %v; want kept", kept, err)
	}
	if strings.Contains(gitRun(t, repo, "worktree", "list", "--porcelain"), "locked") {
		t.Error("kept worktree should be unlocked")
	}
}
