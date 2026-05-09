package agent

import (
	"fmt"
	"strings"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/pendingedits"
)

// DiffFeedback describes one hunk the user rejected or asked to be redone,
// together with any inline note they attached.
type DiffFeedback struct {
	Path         string
	OldStart     int
	OldLength    int
	NewStart     int
	NewLength    int
	ProposedHunk string // the +/- lines the agent wrote
	Note         string // user's optional free-text note (may be empty)
}

// BuildDiffFeedbackMessage produces a single user-role api.Message that tells
// the agent which hunks the user rejected and why. The message uses an
// XML-ish envelope so the model can parse each rejection precisely.
//
// The envelope looks like:
//
//	<diff_feedback>
//	  <hunk path="..." old_start="N" new_start="M">
//	    <proposed>
//	      -old line
//	      +new line
//	    </proposed>
//	    <decision>rejected</decision>   <!-- or "request_change" -->
//	    <note>user's optional note</note>
//	  </hunk>
//	  ...
//	</diff_feedback>
func BuildDiffFeedbackMessage(items []DiffFeedback) api.Message {
	var sb strings.Builder
	sb.WriteString("The following edits were rejected by the user during diff review. ")
	sb.WriteString("Please address each rejection before continuing:\n\n")
	sb.WriteString("<diff_feedback>\n")
	for _, fb := range items {
		fmt.Fprintf(&sb, "  <hunk path=%q old_start=%d old_length=%d new_start=%d new_length=%d>\n",
			fb.Path, fb.OldStart, fb.OldLength, fb.NewStart, fb.NewLength)
		if fb.ProposedHunk != "" {
			sb.WriteString("    <proposed>\n")
			for _, line := range strings.Split(fb.ProposedHunk, "\n") {
				fmt.Fprintf(&sb, "      %s\n", line)
			}
			sb.WriteString("    </proposed>\n")
		}
		sb.WriteString("    <decision>rejected</decision>\n")
		if fb.Note != "" {
			fmt.Fprintf(&sb, "    <note>%s</note>\n", strings.TrimSpace(fb.Note))
		}
		sb.WriteString("  </hunk>\n")
	}
	sb.WriteString("</diff_feedback>")
	return api.Message{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: sb.String()}}}
}

// HunkToDiffFeedback converts a pendingedits.Hunk into a DiffFeedback for use
// in BuildDiffFeedbackMessage. `note` may be empty.
func HunkToDiffFeedback(path string, h pendingedits.Hunk, note string) DiffFeedback {
	var sb strings.Builder
	for _, ln := range h.Lines {
		switch ln.Op {
		case pendingedits.DiffInsert:
			fmt.Fprintf(&sb, "+%s\n", ln.Text)
		case pendingedits.DiffDelete:
			fmt.Fprintf(&sb, "-%s\n", ln.Text)
		default:
			fmt.Fprintf(&sb, " %s\n", ln.Text)
		}
	}
	return DiffFeedback{
		Path:         path,
		OldStart:     h.OldStart,
		OldLength:    h.OldLength,
		NewStart:     h.NewStart,
		NewLength:    h.NewLength,
		ProposedHunk: strings.TrimRight(sb.String(), "\n"),
		Note:         note,
	}
}
