// Package sleeptool implements the Sleep tool — waits for a specified duration.
//
// Mirrors src/tools/SleepTool. Unlike Bash(sleep N), this tool does not hold
// a shell process and can be called concurrently with other tools.
package sleeptool

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/icehunter/conduit/internal/tool"
)

// MaxSleepSeconds caps a single sleep call to prevent runaway sessions.
const MaxSleepSeconds = 300

// Tool implements the Sleep tool.
type Tool struct{}

// New returns a fresh Sleep tool.
func New() *Tool { return &Tool{} }

func (*Tool) Name() string { return "Sleep" }

func (*Tool) Description() string {
	return "Wait for a specified duration. " +
		"Prefer this over Bash(sleep ...) — it doesn't hold a shell process. " +
		"The user can interrupt the sleep at any time."
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"duration_seconds": {
				"type": "number",
				"description": "How many seconds to sleep (max 300)"
			}
		},
		"required": ["duration_seconds"]
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool        { return true }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return true }

// Input is the typed view of the JSON input.
type Input struct {
	DurationSeconds float64 `json:"duration_seconds"`
}

// Execute sleeps for the requested duration, respecting context cancellation.
func (*Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if in.DurationSeconds <= 0 {
		return tool.ErrorResult("duration_seconds must be positive"), nil
	}
	if in.DurationSeconds > MaxSleepSeconds {
		return tool.ErrorResult(fmt.Sprintf("duration_seconds exceeds max of %d", MaxSleepSeconds)), nil
	}

	d := time.Duration(in.DurationSeconds * float64(time.Second))
	select {
	case <-time.After(d):
		return tool.TextResult(fmt.Sprintf("Slept for %.1f seconds.", in.DurationSeconds)), nil
	case <-ctx.Done():
		return tool.ErrorResult("sleep interrupted"), nil
	}
}
