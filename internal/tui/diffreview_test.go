package tui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/pendingedits"
)

// TestBuildDiffReviewResult_HunkLevel verifies that approving only some hunks
// of a multi-hunk file produces an Approved Entry whose NewContent equals the
// expected partial-apply, and that an entry with zero approvals is omitted.
func TestBuildDiffReviewResult_HunkLevel(t *testing.T) {
	orig := []byte("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n")
	updated := []byte("A\nb\nc\nd\ne\nf\ng\nh\ni\nJ\n")

	dr := newDiffReviewState(
		[]pendingedits.Entry{{
			Path:        "/tmp/test.go",
			OrigContent: orig,
			NewContent:  updated,
			OrigExisted: true,
		}},
		make(chan DiffReviewResult, 1),
	)

	if got, want := len(dr.entries), 1; got != want {
		t.Fatalf("entries: got %d want %d", got, want)
	}
	if got, want := len(dr.entries[0].hunks), 2; got != want {
		t.Fatalf("hunks: got %d want %d", got, want)
	}

	// Reject the first hunk, approve the second.
	dr.entries[0].hunks[0].action = diffReviewReverted
	dr.entries[0].hunks[1].action = diffReviewApproved

	res := buildDiffReviewResult(dr)
	if got, want := len(res.Approved), 1; got != want {
		t.Fatalf("Approved: got %d want %d", got, want)
	}
	wantContent := []byte("a\nb\nc\nd\ne\nf\ng\nh\ni\nJ\n")
	if !bytes.Equal(res.Approved[0].NewContent, wantContent) {
		t.Errorf("Approved[0].NewContent: got %q want %q",
			res.Approved[0].NewContent, wantContent)
	}
	if got := len(res.Requested); got != 0 {
		t.Errorf("Requested: got %d want 0", got)
	}
}

// TestBuildDiffReviewResult_AllReverted verifies a file with every hunk
// reverted is dropped from Approved entirely (no no-op writes).
func TestBuildDiffReviewResult_AllReverted(t *testing.T) {
	dr := newDiffReviewState(
		[]pendingedits.Entry{{
			Path:        "/tmp/test.go",
			OrigContent: []byte("a\nb\n"),
			NewContent:  []byte("A\nB\n"),
		}},
		make(chan DiffReviewResult, 1),
	)
	for i := range dr.entries[0].hunks {
		dr.entries[0].hunks[i].action = diffReviewReverted
	}
	res := buildDiffReviewResult(dr)
	if got := len(res.Approved); got != 0 {
		t.Errorf("Approved: got %d want 0 (all reverted should drop the file)", got)
	}
}

// TestBuildDiffReviewResult_RequestedFlag verifies that any hunk marked
// "request change" causes the entry to appear in Requested (carrying the
// agent's proposed NewContent unchanged for follow-up display).
func TestBuildDiffReviewResult_RequestedFlag(t *testing.T) {
	updated := []byte("A\nb\n")
	dr := newDiffReviewState(
		[]pendingedits.Entry{{
			Path:        "/tmp/test.go",
			OrigContent: []byte("a\nb\n"),
			NewContent:  updated,
		}},
		make(chan DiffReviewResult, 1),
	)
	dr.entries[0].hunks[0].action = diffReviewRequested
	res := buildDiffReviewResult(dr)
	if got := len(res.Requested); got != 1 {
		t.Fatalf("Requested: got %d want 1", got)
	}
	if !bytes.Equal(res.Requested[0].NewContent, updated) {
		t.Errorf("Requested[0].NewContent should preserve the agent's proposal: got %q want %q",
			res.Requested[0].NewContent, updated)
	}
}

// TestBuildDiffReviewResult_FollowupMessage verifies that a hunk marked
// "requested" with a note produces a non-empty FollowupMessage containing the
// expected XML envelope, path, and note text.
func TestBuildDiffReviewResult_FollowupMessage(t *testing.T) {
	updated := []byte("A\nb\n")
	dr := newDiffReviewState(
		[]pendingedits.Entry{{
			Path:        "/tmp/review.go",
			OrigContent: []byte("a\nb\n"),
			NewContent:  updated,
		}},
		make(chan DiffReviewResult, 1),
	)
	if len(dr.entries[0].hunks) == 0 {
		t.Fatal("want at least one hunk")
	}
	dr.entries[0].hunks[0].action = diffReviewRequested
	dr.entries[0].hunks[0].note = "prefer snake_case"

	res := buildDiffReviewResult(dr)
	if res.FollowupMessage == "" {
		t.Fatal("FollowupMessage: want non-empty when hunk is requested")
	}
	for _, want := range []string{
		"<diff_feedback>",
		"/tmp/review.go",
		"prefer snake_case",
		"<decision>rejected</decision>",
	} {
		if !strings.Contains(res.FollowupMessage, want) {
			t.Errorf("FollowupMessage missing %q:\n%s", want, res.FollowupMessage)
		}
	}
}

func TestDiffReviewState_Navigation(t *testing.T) {
	dr := newDiffReviewState(
		[]pendingedits.Entry{
			{Path: "/a", OrigContent: []byte("x\n"), NewContent: []byte("X\n")},
			{Path: "/b", OrigContent: []byte("y\n"), NewContent: []byte("Y\n")},
		},
		make(chan DiffReviewResult, 1),
	)
	if dr.fileIdx != 0 || dr.hunkIdx != 0 {
		t.Fatalf("initial cursor: got (%d,%d) want (0,0)", dr.fileIdx, dr.hunkIdx)
	}
	if !dr.advanceHunk() {
		t.Fatal("advanceHunk should cross file boundary")
	}
	if dr.fileIdx != 1 || dr.hunkIdx != 0 {
		t.Errorf("after advance: got (%d,%d) want (1,0)", dr.fileIdx, dr.hunkIdx)
	}
	if dr.advanceHunk() {
		t.Error("advanceHunk should return false at end of last file")
	}
	if !dr.retreatHunk() {
		t.Fatal("retreatHunk should cross file boundary backward")
	}
	if dr.fileIdx != 0 {
		t.Errorf("after retreat: fileIdx=%d want 0", dr.fileIdx)
	}
}
