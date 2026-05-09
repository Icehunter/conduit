package main

import (
	"errors"
	"testing"

	"github.com/icehunter/conduit/internal/pendingedits"
	"github.com/icehunter/conduit/internal/tui"
)

func TestDiffReviewShouldPause_AllApprovedNoFailuresContinues(t *testing.T) {
	result := tui.DiffReviewResult{
		Approved: []pendingedits.Entry{{Path: "/tmp/a.txt"}},
	}
	flushResults := []pendingedits.FlushResult{{Path: "/tmp/a.txt", Applied: true}}
	if diffReviewShouldPause(result, flushResults) {
		t.Fatal("all-approved review should continue the current loop")
	}
}

func TestDiffReviewShouldPause_FollowupOrFlushFailurePauses(t *testing.T) {
	withFollowup := tui.DiffReviewResult{FollowupMessage: "<diff_feedback/>"}
	if !diffReviewShouldPause(withFollowup, nil) {
		t.Fatal("review feedback should pause for synthetic follow-up")
	}

	withFailure := tui.DiffReviewResult{
		Approved: []pendingedits.Entry{{Path: "/tmp/a.txt"}},
	}
	flushResults := []pendingedits.FlushResult{{
		Path: "/tmp/a.txt",
		Err:  errors.New("conflict"),
	}}
	if !diffReviewShouldPause(withFailure, flushResults) {
		t.Fatal("flush failure should pause for synthetic follow-up")
	}
}

func TestDiffReviewFollowupText_IncludesFlushFailures(t *testing.T) {
	text := diffReviewFollowupText(tui.DiffReviewResult{}, []pendingedits.FlushResult{{
		Path: "/tmp/a.txt",
		Err:  errors.New("conflict"),
	}})
	if text == "" {
		t.Fatal("expected followup text")
	}
	if want := "<diff_apply_errors>"; !contains(text, want) {
		t.Fatalf("followup text %q missing %q", text, want)
	}
	if want := "/tmp/a.txt"; !contains(text, want) {
		t.Fatalf("followup text %q missing %q", text, want)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
