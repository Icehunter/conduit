// Package syntheticoutputtool implements the StructuredOutput tool.
//
// Used in non-interactive sessions to return structured JSON output.
// The model calls this tool exactly once as its final response.
// Port of src/tools/SyntheticOutputTool/.
package syntheticoutputtool

import (
	"context"
	"encoding/json"

	"github.com/icehunter/conduit/internal/tool"
)

const toolName = "StructuredOutput"

// SyntheticOutput accepts any JSON-shaped input and returns it as its result.
// Used when the caller requests structured output (e.g. JSON schema output mode).
type SyntheticOutput struct {
	// OnOutput is called when the model returns structured output.
	// nil means no callback (output is available from the tool result).
	OnOutput func(data json.RawMessage)
}

func (t *SyntheticOutput) Name() string        { return toolName }
func (t *SyntheticOutput) Description() string { return description }
func (t *SyntheticOutput) InputSchema() json.RawMessage {
	// Accepts any object.
	return json.RawMessage(`{"type":"object","additionalProperties":true}`)
}
func (t *SyntheticOutput) IsReadOnly(_ json.RawMessage) bool        { return true }
func (t *SyntheticOutput) IsConcurrencySafe(_ json.RawMessage) bool { return true }

func (t *SyntheticOutput) Execute(_ context.Context, raw json.RawMessage) (tool.Result, error) {
	// Validate it's valid JSON.
	if !json.Valid(raw) {
		return tool.ErrorResult("invalid JSON input"), nil
	}
	if t.OnOutput != nil {
		t.OnOutput(raw)
	}
	// Return the input as canonical JSON.
	var buf any
	_ = json.Unmarshal(raw, &buf)
	out, _ := json.MarshalIndent(buf, "", "  ")
	return tool.TextResult(string(out)), nil
}

const description = `Return structured output in the requested format. Call this tool exactly once at the end of your response to provide the final structured result.`
