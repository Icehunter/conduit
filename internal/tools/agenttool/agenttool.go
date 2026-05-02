// Package agenttool implements the AgentTool — the agent's way to spawn
// a nested sub-agent with a specific prompt.
//
// When the model calls AgentTool, the tool:
//  1. Builds a fresh user message from the supplied prompt.
//  2. Runs a nested agent loop (same tools, same client, new history).
//  3. Returns the sub-agent's final text response.
//
// This mirrors AgentTool.tsx's core call() path (the non-team, non-remote,
// non-background case) — a synchronous forked sub-agent that returns its
// result text.
//
// Reference: src/tools/AgentTool/AgentTool.tsx
package agenttool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/icehunter/conduit/internal/tool"
)

// Tool implements the AgentTool.
type Tool struct {
	// runAgent spawns a nested loop with the given prompt and returns the
	// final assistant text. Provided by main at construction time.
	runAgent func(ctx context.Context, prompt string) (string, error)
}

// New returns an AgentTool.
// runAgent is a closure that runs a nested agent loop with the given prompt
// as the sole user message and returns the final assistant text.
func New(runAgent func(ctx context.Context, prompt string) (string, error)) *Tool {
	return &Tool{runAgent: runAgent}
}

func (*Tool) Name() string { return "Task" }

func (*Tool) Description() string {
	return "Launch a new agent to handle a specific task. " +
		"The sub-agent has access to all the same tools. " +
		"Provide a detailed `prompt` describing exactly what the agent should do. " +
		"Optionally supply a short `description` (shown in the UI while the agent runs)."
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"prompt": {
				"type": "string",
				"description": "The task for the sub-agent to perform. Be specific and detailed."
			},
			"description": {
				"type": "string",
				"description": "Short description of the task shown while the agent runs"
			}
		},
		"required": ["prompt"]
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool        { return false }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

type Input struct {
	Prompt      string `json:"prompt"`
	Description string `json:"description,omitempty"`
}

func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("agenttool: invalid input: %v", err)), nil
	}
	if in.Prompt == "" {
		return tool.ErrorResult("agenttool: prompt is required"), nil
	}

	result, err := t.runAgent(ctx, in.Prompt)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("agenttool: %v", err)), nil
	}
	return tool.TextResult(result), nil
}
