package tui

import (
	"context"
	"testing"

	"github.com/icehunter/conduit/internal/pendingedits"
)

func TestAskDiffReview_CancelApprovesDrainedSnapshot(t *testing.T) {
	table := pendingedits.NewTable()
	if err := table.Stage(pendingedits.Entry{
		Path:       "/tmp/example.txt",
		NewContent: []byte("staged"),
		Op:         pendingedits.OpEdit,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var sent []pendingedits.Entry
	result := askDiffReview(ctx, table, func(entries []pendingedits.Entry, _ chan<- DiffReviewResult) {
		sent = entries
	})

	if len(sent) != 1 {
		t.Fatalf("sent entries = %d, want 1", len(sent))
	}
	if len(result.Approved) != 1 {
		t.Fatalf("approved entries = %d, want 1", len(result.Approved))
	}
	if result.Approved[0].Path != "/tmp/example.txt" {
		t.Fatalf("approved path = %q", result.Approved[0].Path)
	}
	if table.Len() != 0 {
		t.Fatalf("table len after drain = %d, want 0", table.Len())
	}
}
