package bashtool

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/icehunter/conduit/internal/bgjobs"
	"github.com/icehunter/conduit/internal/tool"
)

// OutputTool implements BashOutput: it returns what a background Bash job has
// written since the previous BashOutput read of that job.
type OutputTool struct {
	tool.NotDeferrable
	jobs *bgjobs.Registry
}

// NewOutput returns a BashOutput tool over the process-wide job registry.
func NewOutput() *OutputTool { return &OutputTool{jobs: bgjobs.Default} }

// Name implements tool.Tool.
func (*OutputTool) Name() string { return "BashOutput" }

// Description is the prompt text the model sees.
func (*OutputTool) Description() string {
	return "Returns the output a background Bash command has produced since the last BashOutput call for it, " +
		"plus its status and exit code. Takes the shell ID returned by Bash with `run_in_background`. " +
		"Optional `filter` is a regular expression; only matching lines are returned (non-matching lines are consumed). " +
		"You are notified when a background command finishes, so use this to look at progress, not to poll."
}

// InputSchema is the JSON Schema sent to the model.
func (*OutputTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"bash_id": {"type": "string", "description": "The ID of the background shell to read from"},
			"filter":  {"type": "string", "description": "Optional regular expression; only lines matching it are returned"}
		},
		"required": ["bash_id"]
	}`)
}

// IsReadOnly implements tool.Tool.
func (*OutputTool) IsReadOnly(json.RawMessage) bool { return true }

// IsConcurrencySafe implements tool.Tool.
func (*OutputTool) IsConcurrencySafe(json.RawMessage) bool { return true }

// Execute implements tool.Tool.
func (t *OutputTool) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	var in struct {
		BashID string `json:"bash_id"`
		Filter string `json:"filter"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if in.BashID == "" {
		return tool.ErrorResult("`bash_id` is required"), nil
	}
	var re *regexp.Regexp
	if in.Filter != "" {
		var err error
		if re, err = regexp.Compile(in.Filter); err != nil {
			return tool.ErrorResult(fmt.Sprintf("invalid filter: %v", err)), nil
		}
	}

	out, err := t.jobs.ReadNext(in.BashID)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}

	text := strings.TrimRight(out.Text, "\n")
	if re != nil {
		var kept []string
		for line := range strings.SplitSeq(text, "\n") {
			if re.MatchString(line) {
				kept = append(kept, line)
			}
		}
		text = strings.Join(kept, "\n")
	}
	maxBytes, maxLines := outputBudget()
	text = fitOutput(text, maxBytes, maxLines)

	var sb strings.Builder
	fmt.Fprintf(&sb, "Status: %s", out.Job.Status)
	switch {
	case out.Job.Running():
	case out.Job.Status == bgjobs.StatusKilled:
		// A signal death has no exit code worth showing.
		if out.Job.TimedOut {
			sb.WriteString("\nThe command hit its timeout and was stopped.")
		}
	default:
		fmt.Fprintf(&sb, "\nExit code: %d", out.Job.ExitCode)
	}
	if out.Skipped > 0 {
		fmt.Fprintf(&sb, "\n[%d earlier bytes skipped; full output: %s]", out.Skipped, out.Job.OutputPath)
	}
	if text == "" {
		sb.WriteString("\n(no new output)")
	} else {
		sb.WriteString("\n" + text)
	}
	return tool.TextResult(sb.String()), nil
}
