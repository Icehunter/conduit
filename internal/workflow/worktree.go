package workflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree is an isolated git worktree created for one agent() call with
// isolation: 'worktree'.
type Worktree struct {
	Path   string
	Branch string
	root   string
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
	path := filepath.Join(dir, name)
	cmd := exec.CommandContext(ctx, "git", "-C", root, "worktree", "add", "--detach", path, "HEAD") //nolint:gosec // fixed argv
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("workflow: git worktree add: %w\n%s", err, out)
	}
	return &Worktree{Path: path, Branch: name, root: root}, nil
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

// Finish removes the worktree when it is unchanged and keeps it otherwise,
// returning the kept path (or "") so the caller can report where the edit is.
func (w *Worktree) Finish(ctx context.Context) (kept string, err error) {
	dirty, err := w.Dirty(ctx)
	if err != nil {
		return w.Path, err
	}
	if dirty {
		return w.Path, nil
	}
	return "", w.Remove(ctx, false)
}

func gitRoot(ctx context.Context, cwd string) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel").Output() //nolint:gosec // fixed argv
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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
