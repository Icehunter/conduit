//go:build windows

package workflow

// pidAlive is conservative on Windows: a locked worktree is never reclaimed.
func pidAlive(int) bool { return true }
