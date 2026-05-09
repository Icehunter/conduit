package agent_test

import (
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/pendingedits"
)

func TestBuildDiffFeedbackMessage_Shape(t *testing.T) {
	orig := []byte("a\nb\nc\n")
	updated := []byte("a\nB\nc\n")
	lines := pendingedits.Diff(orig, updated)
	hunks := pendingedits.Hunks(lines, 1)
	if len(hunks) == 0 {
		t.Fatal("want at least one hunk")
	}

	fb := agent.HunkToDiffFeedback("/tmp/foo.go", hunks[0], "use lowercase instead")
	msg := agent.BuildDiffFeedbackMessage([]agent.DiffFeedback{fb})

	if msg.Role != "user" {
		t.Errorf("role: got %q want %q", msg.Role, "user")
	}
	if len(msg.Content) == 0 {
		t.Fatal("content: empty")
	}
	text := msg.Content[0].Text
	for _, want := range []string{
		"<diff_feedback>",
		"</diff_feedback>",
		`path="/tmp/foo.go"`,
		"<decision>rejected</decision>",
		"<note>use lowercase instead</note>",
		"<proposed>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in message text:\n%s", want, text)
		}
	}
}

func TestBuildDiffFeedbackMessage_NoNote(t *testing.T) {
	orig := []byte("x\n")
	updated := []byte("X\n")
	lines := pendingedits.Diff(orig, updated)
	hunks := pendingedits.Hunks(lines, 0)
	if len(hunks) == 0 {
		t.Fatal("want at least one hunk")
	}
	fb := agent.HunkToDiffFeedback("/tmp/bar.go", hunks[0], "")
	msg := agent.BuildDiffFeedbackMessage([]agent.DiffFeedback{fb})
	text := msg.Content[0].Text
	if strings.Contains(text, "<note>") {
		t.Errorf("expected no <note> element when note is empty, got:\n%s", text)
	}
}

func TestBuildDiffFeedbackMessage_MultiHunk(t *testing.T) {
	orig := []byte("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n")
	updated := []byte("A\nb\nc\nd\ne\nf\ng\nh\ni\nJ\n")
	lines := pendingedits.Diff(orig, updated)
	hunks := pendingedits.Hunks(lines, 1)
	if len(hunks) != 2 {
		t.Fatalf("want 2 hunks, got %d", len(hunks))
	}
	items := []agent.DiffFeedback{
		agent.HunkToDiffFeedback("/tmp/multi.go", hunks[0], "keep lowercase a"),
		agent.HunkToDiffFeedback("/tmp/multi.go", hunks[1], ""),
	}
	msg := agent.BuildDiffFeedbackMessage(items)
	text := msg.Content[0].Text
	if strings.Count(text, "<hunk") != 2 {
		t.Errorf("expected 2 <hunk> elements, got:\n%s", text)
	}
	if !strings.Contains(text, "keep lowercase a") {
		t.Errorf("first note missing from text:\n%s", text)
	}
}
