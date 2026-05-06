// Package worktreetool implements EnterWorktree and ExitWorktree tools.
//
// EnterWorktree creates a new git worktree and switches the session's working
// directory into it. ExitWorktree removes or keeps the worktree and restores
// the original working directory.
//
// Port of src/tools/EnterWorktreeTool/ and src/tools/ExitWorktreeTool/.
package worktreetool

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/icehunter/conduit/internal/tool"
)

// CwdSetter is called when the worktree tool needs to change the session cwd.
// Returns an error if the change was rejected.
type CwdSetter func(newCwd string) error

// EnterWorktree creates a git worktree and switches into it.
type EnterWorktree struct {
	// GetCwd returns the current working directory.
	GetCwd func() string
	// SetCwd changes the session's working directory.
	SetCwd CwdSetter
}

type enterInput struct {
	Name string `json:"name,omitempty"`
}

func (t *EnterWorktree) Name() string        { return "EnterWorktree" }
func (t *EnterWorktree) Description() string { return enterDescription }
func (t *EnterWorktree) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"name": {
			"type": "string",
			"description": "Optional worktree branch name. Generated randomly if omitted."
		}
	},
	"additionalProperties": false
}`)
}
func (t *EnterWorktree) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *EnterWorktree) IsConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *EnterWorktree) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var inp enterInput
	_ = json.Unmarshal(raw, &inp)

	cwd := "."
	if t.GetCwd != nil {
		cwd = t.GetCwd()
	}

	// Find the git root.
	gitRoot, err := gitRoot(ctx, cwd)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("not in a git repository: %v", err)), nil
	}

	// Generate a branch/worktree name.
	name := inp.Name
	if name == "" {
		name = fmt.Sprintf("conduit-wt-%06x", rand.IntN(0xFFFFFF))
	}
	// Sanitize: only alphanum, dash, dot, underscore.
	name = sanitizeSlug(name)
	if name == "" {
		return tool.ErrorResult("invalid worktree name"), nil
	}

	wtPath := filepath.Join(gitRoot, "..", name)
	wtPath, _ = filepath.Abs(wtPath)

	// git worktree add <path> -b <branch>
	cmd := exec.CommandContext(ctx, "git", "-C", gitRoot, "worktree", "add", wtPath, "-b", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return tool.ErrorResult(fmt.Sprintf("git worktree add failed: %v\n%s", err, out)), nil
	}

	// Switch cwd into the worktree.
	if t.SetCwd != nil {
		if err := t.SetCwd(wtPath); err != nil {
			return tool.ErrorResult(fmt.Sprintf("cannot switch to worktree: %v", err)), nil
		}
	}

	return tool.TextResult(fmt.Sprintf("Entered worktree at %s (branch: %s).\n\nYou are now in an isolated environment. Use ExitWorktree to leave.", wtPath, name)), nil
}

// ExitWorktree exits the current worktree and returns to the original directory.
type ExitWorktree struct {
	// GetCwd returns the current working directory.
	GetCwd func() string
	// SetCwd changes the session's working directory.
	SetCwd CwdSetter
	// OriginalCwd is the directory to return to on exit.
	OriginalCwd string
}

type exitInput struct {
	Action         string `json:"action"`          // "keep" | "remove"
	DiscardChanges bool   `json:"discard_changes"` // only used with "remove"
}

func (t *ExitWorktree) Name() string        { return "ExitWorktree" }
func (t *ExitWorktree) Description() string { return exitDescription }
func (t *ExitWorktree) InputSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": {
			"type": "string",
			"enum": ["keep", "remove"],
			"description": "\"keep\" leaves the worktree on disk; \"remove\" deletes it."
		},
		"discard_changes": {
			"type": "boolean",
			"description": "When removing: discard uncommitted changes (force). Default false."
		}
	},
	"required": ["action"],
	"additionalProperties": false
}`)
}
func (t *ExitWorktree) IsReadOnly(_ json.RawMessage) bool        { return false }
func (t *ExitWorktree) IsConcurrencySafe(_ json.RawMessage) bool { return false }

func (t *ExitWorktree) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var inp exitInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return tool.ErrorResult("invalid input: " + err.Error()), nil
	}
	if inp.Action != "keep" && inp.Action != "remove" {
		return tool.ErrorResult("action must be \"keep\" or \"remove\""), nil
	}

	wtPath := "."
	if t.GetCwd != nil {
		wtPath = t.GetCwd()
	}

	// Find git root of the worktree.
	gitRoot, err := gitRoot(ctx, wtPath)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("not in a git repository: %v", err)), nil
	}

	// Determine the branch name of this worktree.
	branchBytes, _ := exec.CommandContext(ctx, "git", "-C", wtPath, "branch", "--show-current").Output()
	branch := strings.TrimSpace(string(branchBytes))

	// Restore original cwd first.
	restorePath := t.OriginalCwd
	if restorePath == "" {
		// Fall back to the git root's parent (best guess).
		restorePath = filepath.Dir(gitRoot)
	}
	if t.SetCwd != nil {
		_ = t.SetCwd(restorePath)
	}

	var sb strings.Builder

	if inp.Action == "remove" {
		args := []string{"-C", gitRoot, "worktree", "remove"}
		if inp.DiscardChanges {
			args = append(args, "--force")
		}
		args = append(args, wtPath)
		if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
			fmt.Fprintf(&sb, "Warning: git worktree remove failed: %v\n%s\n", err, out)
		} else {
			// Also delete the branch if it was auto-created.
			if branch != "" {
				_ = exec.CommandContext(ctx, "git", "-C", gitRoot, "branch", "-d", branch).Run()
			}
			fmt.Fprintf(&sb, "Worktree %s removed.\n", wtPath)
		}
	} else {
		fmt.Fprintf(&sb, "Worktree %s kept (branch: %s).\n", wtPath, branch)
	}

	fmt.Fprintf(&sb, "Returned to %s.", restorePath)
	return tool.TextResult(sb.String()), nil
}

// gitRoot returns the absolute path of the git repository root.
func gitRoot(ctx context.Context, cwd string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// sanitizeSlug removes characters not allowed in git branch names.
func sanitizeSlug(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	result := b.String()
	if len(result) > 64 {
		result = result[:64]
	}
	return strings.Trim(result, "-.")
}

// IsInsideWorktree returns true if cwd is a git worktree (not the main checkout).
func IsInsideWorktree(cwd string) bool {
	out, err := exec.CommandContext(context.Background(), "git", "-C", cwd, "rev-parse", "--is-inside-work-tree").Output()
	if err != nil {
		return false
	}
	if strings.TrimSpace(string(out)) != "true" {
		return false
	}
	// Check if it's a linked worktree (not main).
	gitDir, err := exec.CommandContext(context.Background(), "git", "-C", cwd, "rev-parse", "--git-dir").Output()
	if err != nil {
		return false
	}
	// A linked worktree's .git dir is a file, not a directory.
	gitDirPath := strings.TrimSpace(string(gitDir))
	if !filepath.IsAbs(gitDirPath) {
		gitDirPath = filepath.Join(cwd, gitDirPath)
	}
	info, err := os.Stat(gitDirPath)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

const enterDescription = `Creates a new isolated git worktree and switches the session's working directory into it. Each worktree has its own branch so you can work without affecting the main checkout. Use ExitWorktree to leave.`

const exitDescription = `Exits the current git worktree and returns to the original working directory. action="keep" leaves the worktree on disk; action="remove" deletes it (and the branch).`
