package workflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Worktree is an isolated git worktree created for one agent() call with
// isolation: 'worktree'.
type Worktree struct {
	Path   string
	Branch string
	root   string
	// base is the commit the worktree was created at, so Finish can tell
	// whether the agent committed work that a clean tree would hide.
	base string
}

// worktreeDirName is where per-agent worktrees live inside the repo's
// .conduit directory (gitignored by convention).
const worktreeDirName = "worktrees"

// NewWorktree creates a worktree for runID/seq off the repository containing
// cwd. Setup is ~200–500ms plus disk, so the runtime only calls this when a
// script asked for isolation.
func NewWorktree(ctx context.Context, cwd, runID string, seq int) (*Worktree, error) {
	root, err := gitRoot(ctx, cwd)
	if err != nil {
		return nil, fmt.Errorf("workflow: worktree: not in a git repository: %w", err)
	}
	name := fmt.Sprintf("wf-%s-%d", sanitize(runID), seq)
	dir := filepath.Join(root, ".conduit", worktreeDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("workflow: worktree: %w", err)
	}
	if err := excludeWorktrees(ctx, root); err != nil {
		return nil, fmt.Errorf("workflow: worktree: %w", err)
	}
	path := filepath.Join(dir, name)
	// Base the worktree on the caller's HEAD, not the main checkout's: when cwd
	// is itself a linked worktree (a feature branch, or a nested isolated
	// agent) the sub-agent must see that code.
	headOut, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "HEAD").Output() //nolint:gosec // fixed argv
	if err != nil {
		return nil, fmt.Errorf("worktree: resolve HEAD: %w", err)
	}
	head := strings.TrimSpace(string(headOut))
	cmd := exec.CommandContext(ctx, "git", "-C", root, "worktree", "add", "--detach", "--lock", "--reason", lockReason(), path, head) //nolint:gosec // fixed argv
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("workflow: git worktree add: %w\n%s", err, out)
	}
	base, _ := exec.CommandContext(ctx, "git", "-C", path, "rev-parse", "HEAD").Output() //nolint:gosec // fixed argv
	return &Worktree{Path: path, Branch: name, root: root, base: strings.TrimSpace(string(base))}, nil
}

// Committed reports whether HEAD moved since the worktree was created.
func (w *Worktree) Committed(ctx context.Context) (bool, error) {
	if w.base == "" {
		return false, nil
	}
	out, err := exec.CommandContext(ctx, "git", "-C", w.Path, "rev-parse", "HEAD").Output() //nolint:gosec // fixed argv
	if err != nil {
		return false, fmt.Errorf("workflow: git rev-parse: %w", err)
	}
	return strings.TrimSpace(string(out)) != w.base, nil
}

// Dirty reports whether the worktree has uncommitted changes.
func (w *Worktree) Dirty(ctx context.Context) (bool, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", w.Path, "status", "--porcelain").Output() //nolint:gosec // fixed argv
	if err != nil {
		return false, fmt.Errorf("workflow: git status: %w", err)
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// Remove deletes the worktree. force discards uncommitted changes.
func (w *Worktree) Remove(ctx context.Context, force bool) error {
	// Created locked; a locked worktree cannot be removed without -f -f.
	_ = exec.CommandContext(ctx, "git", "-C", w.root, "worktree", "unlock", w.Path).Run() //nolint:gosec // fixed argv
	args := []string{"-C", w.root, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, w.Path)
	if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil { //nolint:gosec // fixed argv
		return fmt.Errorf("workflow: git worktree remove: %w\n%s", err, out)
	}
	return nil
}

// Finish removes the worktree when it is unchanged (clean and no new commits) and keeps it otherwise,
// returning the kept path (or "") so the caller can report where the edit is.
func (w *Worktree) Finish(ctx context.Context) (kept string, err error) {
	dirty, err := w.Dirty(ctx)
	if err != nil {
		w.unlock(ctx)
		return w.Path, err
	}
	if dirty {
		w.unlock(ctx)
		return w.Path, nil
	}
	// A clean tree can still hold commits on the detached HEAD; removing it
	// would orphan them.
	committed, err := w.Committed(ctx)
	if err != nil || committed {
		w.unlock(ctx)
		return w.Path, err
	}
	return "", w.Remove(ctx, false)
}

// gitRoot returns the main checkout of the repository containing cwd. From
// inside a linked worktree it still names the main checkout, so nested
// isolation lands under one .conduit/worktrees.
func gitRoot(ctx context.Context, cwd string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--path-format=absolute", "--git-common-dir").Output() //nolint:gosec // fixed argv
	if err != nil {
		return "", err
	}
	common := strings.TrimSpace(string(out))
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common), nil
	}
	top, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel").Output() //nolint:gosec // fixed argv
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(top)), nil
}

const excludeEntry = "/.conduit/worktrees/"

// excludeWorktrees keeps the worktree directory out of `git status` and
// `git add -A` in the main checkout (otherwise they stage as gitlinks).
func excludeWorktrees(ctx context.Context, root string) error {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir").Output() //nolint:gosec // fixed argv
	if err != nil {
		return err
	}
	file := filepath.Join(strings.TrimSpace(string(out)), "info", "exclude")
	cur, err := os.ReadFile(file) //nolint:gosec // path derived from git
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(cur), "\n") {
		if strings.TrimSpace(line) == excludeEntry {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	prefix := ""
	if len(cur) > 0 && !strings.HasSuffix(string(cur), "\n") {
		prefix = "\n"
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644) //nolint:gosec // path derived from git
	if err != nil {
		return err
	}
	_, werr := f.WriteString(prefix + excludeEntry + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

func lockReason() string { return fmt.Sprintf("conduit pid %d", os.Getpid()) }

// unlock releases the creation lock so the kept worktree can be managed with
// plain git commands.
func (w *Worktree) unlock(ctx context.Context) {
	_ = exec.CommandContext(ctx, "git", "-C", w.root, "worktree", "unlock", w.Path).Run() //nolint:gosec // fixed argv
}

// lockedPID extracts N from a "conduit pid N" lock reason; 0 when it isn't ours.
func lockedPID(reason string) int {
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(reason), "conduit pid %d", &pid); err != nil {
		return 0
	}
	return pid
}

// lockedWorktrees maps worktree path to its lock reason ("" if locked without one).
func lockedWorktrees(ctx context.Context, root string) map[string]string {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "worktree", "list", "--porcelain").Output() //nolint:gosec // fixed argv
	locked := map[string]string{}
	if err != nil {
		return locked
	}
	cur := ""
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			// git prints forward slashes; callers look up native paths.
			cur = filepath.FromSlash(strings.TrimPrefix(line, "worktree "))
		case line == "locked" || strings.HasPrefix(line, "locked "):
			locked[cur] = strings.TrimSpace(strings.TrimPrefix(line, "locked"))
		}
	}
	return locked
}

// newestMod returns the newest mtime under dir, stopping early once one is
// newer than cutoff (the answer only matters relative to it).
func newestMod(dir string, cutoff time.Time) time.Time {
	var newest time.Time
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
			if newest.After(cutoff) {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return newest
}

// pruneAdminEntries deletes <git-common-dir>/worktrees/<id> entries for
// conduit worktrees whose directory has vanished. Unlike `git worktree prune`
// it never touches other worktrees (e.g. ones on an unmounted drive).
func pruneAdminEntries(ctx context.Context, root string) {
	out, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir").Output() //nolint:gosec // fixed argv
	if err != nil {
		return
	}
	admin := filepath.Join(strings.TrimSpace(string(out)), "worktrees")
	ours := filepath.Join(root, ".conduit", worktreeDirName) + string(filepath.Separator)
	entries, err := os.ReadDir(admin)
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(admin, e.Name())
		gd, err := os.ReadFile(filepath.Join(dir, "gitdir")) //nolint:gosec // path derived from git
		if err != nil {
			continue
		}
		// git writes forward slashes; normalize so the prefix check works on Windows.
		wtGit := filepath.FromSlash(strings.TrimSpace(string(gd))) // <worktree>/.git
		if !strings.HasPrefix(wtGit, ours) {
			continue
		}
		if _, err := os.Stat(wtGit); !os.IsNotExist(err) {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, "locked")); err == nil {
			continue // its owner may still be starting up; Finish/PruneStale unlock
		}
		_ = os.RemoveAll(dir)
	}
}

// PruneStale removes conduit's own leftovers: admin entries of vanished
// worktrees, and wf-* worktree directories idle longer than maxAge that hold
// nothing worth keeping (no uncommitted or ignored files, no commits outside
// any branch/tag/remote) and are not locked by a live process. Best effort.
func PruneStale(ctx context.Context, cwd string, maxAge time.Duration) {
	root, err := gitRoot(ctx, cwd)
	if err != nil {
		return
	}
	pruneAdminEntries(ctx, root)
	base := filepath.Join(root, ".conduit", worktreeDirName)
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	locked := lockedWorktrees(ctx, root)
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "wf-") {
			continue
		}
		path := filepath.Join(base, e.Name())
		if reason, isLocked := locked[path]; isLocked {
			pid := lockedPID(reason)
			if pid == 0 || pidAlive(pid) {
				continue
			}
		}
		if newestMod(path, cutoff).After(cutoff) {
			continue
		}
		out, err := exec.CommandContext(ctx, "git", "-C", path, "status", "--porcelain", "--ignored").Output() //nolint:gosec // fixed argv
		if err != nil || strings.TrimSpace(string(out)) != "" {
			continue
		}
		out, err = exec.CommandContext(ctx, "git", "-C", path, "rev-list", "--count", "HEAD", "--not", "--branches", "--tags", "--remotes").Output() //nolint:gosec // fixed argv
		if err != nil || strings.TrimSpace(string(out)) != "0" {
			continue
		}
		w := &Worktree{Path: path, root: root}
		_ = w.Remove(ctx, false)
	}
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}
