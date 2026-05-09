package tui

import "context"

// DiffReviewHook is a stub whose callbacks are wired by Run() after the Bubble
// Tea program starts. mainrepl creates one, passes it via RunOptions.DiffReview,
// and calls the callbacks from OnEndTurn.
type DiffReviewHook struct {
	// AskReview drains the pending-edits table, opens the overlay, blocks
	// until the user finishes, and returns the result. Set by Run().
	// Nil before Run() wires it — callers must guard.
	AskReview func(ctx context.Context) DiffReviewResult

	// EnqueueFollowup pushes a message into the TUI's pending-messages queue
	// so it fires as the next user turn after the agent-done transition. Set
	// by Run(). Nil before Run() wires it — callers must guard.
	EnqueueFollowup func(text string)
}
