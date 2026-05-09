package tui

import "context"

// DiffReviewHook is a stub whose AskReview callback is wired by Run() after
// the Bubble Tea program starts. mainrepl creates one, passes it via
// RunOptions.DiffReviewHook, and calls it from OnEndTurn.
type DiffReviewHook struct {
	// AskReview drains the pending-edits table, opens the overlay, blocks
	// until the user finishes, and returns the result. Set by Run().
	// Nil before Run() wires it — callers must guard.
	AskReview func(ctx context.Context) DiffReviewResult
}
