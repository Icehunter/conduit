package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/icehunter/conduit/internal/tool"
)

// Tool gives the planner one explicitly scoped implementation operation.
type Tool struct {
	Run func(context.Context, Task) (Outcome, error)
}

func (*Tool) Name() string { return "CodingTask" }
func (*Tool) Description() string {
	return "Implement a bounded coding task using the configured implement role. Supply an explicit list of repository-relative files (including new files). The runtime applies conflict-checked edits, runs repository checks, sends failures back to the same implementer within a finite repair budget, and optionally reviews. Prefer this tool for implementation while you plan. Only status verified establishes completion; blocked/incomplete require attention. No arbitrary shell or out-of-scope writes are delegated."
}
func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","additionalProperties":false,"required":["prompt","files"],"properties":{"prompt":{"type":"string"},"files":{"type":"array","minItems":1,"maxItems":32,"items":{"type":"string"}}}}`)
}
func (*Tool) IsReadOnly(json.RawMessage) bool        { return false }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var task Task
	if err := json.Unmarshal(raw, &task); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid coding task: %v", err)), nil
	}
	if t.Run == nil {
		return tool.ErrorResult("CodingTask unavailable"), nil
	}
	out, err := t.Run(ctx, task)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	data, err := json.Marshal(out)
	if err != nil {
		return tool.Result{}, err
	}
	if out.Status != "verified" {
		return tool.ErrorResult(string(data)), nil
	}
	return tool.TextResult(string(data)), nil
}
