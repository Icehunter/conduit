// Package tool defines the interface every Claude Code tool implements.
//
// This is the M2 subset — enough for BashTool, FileReadTool, FileWriteTool,
// GrepTool, GlobTool. Additional surface (interruptBehavior, progress,
// outputSchema, MCP integration) lands in later milestones as we port the
// tools that need them. Reference: src/Tool.ts in the leaked TS source.
package tool

import (
	"context"
	"encoding/json"
)

// Tool is the contract every Claude Code tool implements.
//
// The full TS surface is much larger; we ship the slice that matters for
// the agent loop and bulk port the rest in M4. Each method's doc tells
// Qwen exactly what its TS counterpart does.
type Tool interface {
	// Name is the canonical tool name as it appears in the API request's
	// `tools[].name` and the model's `tool_use.name`.
	Name() string

	// Description is the prompt-facing description sent in the API
	// request's `tools[].description`. Mirrors Tool.ts:description() —
	// returns a static string for M2 (some real tools build this from
	// context; we'll accept the simplification until we hit one that
	// actually varies).
	Description() string

	// InputSchema returns the JSON Schema for tool inputs. The TS uses Zod
	// + a Zod→JSON-Schema converter (Tool.ts:14-22, ToolInputJSONSchema);
	// we ship the JSON Schema directly. Returned bytes must be a complete
	// `{"type":"object", ...}` object. Use the schema.Build helper.
	InputSchema() json.RawMessage

	// IsReadOnly reports whether the tool only reads state. Used for
	// permission gating (read-only tools default to allow on auto-mode)
	// and for parallel dispatch decisions in the swarm coordinator.
	// Mirrors Tool.ts:404.
	IsReadOnly(input json.RawMessage) bool

	// IsConcurrencySafe reports whether multiple invocations with the
	// same input may run in parallel. Most tools that mutate state
	// return false. Mirrors Tool.ts:402.
	IsConcurrencySafe(input json.RawMessage) bool

	// Execute runs the tool. The agent loop hands raw JSON input from the
	// model; the tool decodes and validates against its own input schema.
	// Returning an error is allowed — the agent loop turns it into a
	// `tool_result` content block with `is_error: true`.
	//
	// Honor ctx for cancellation. The agent loop cancels ctx when the
	// user interrupts mid-stream.
	Execute(ctx context.Context, input json.RawMessage) (Result, error)
}

// Result is what a tool returns to the agent loop. The agent envelopes
// this into a `tool_result` content block on the next turn.
type Result struct {
	// Content is one or more content blocks (text, image). At minimum a
	// text block summarizing what happened. Mirrors Tool.ts:321
	// (ToolResult).
	Content []ResultBlock
	// IsError indicates the tool failed in a way the model should see.
	// Distinct from Execute returning err — IsError=true is "the tool ran
	// successfully and reports an error result"; an err return means the
	// tool itself blew up.
	IsError bool
}

// ResultBlock is a single content block in a tool's result.
type ResultBlock struct {
	Type string `json:"type"`           // "text" | "image"
	Text string `json:"text,omitempty"` // for type="text"
	// Image fields (Source/MediaType/Data) get added when we land tools
	// that return images (computer-use, screenshots).
}

// TextResult is a convenience constructor for the common case where a
// tool returns one block of text.
func TextResult(text string) Result {
	return Result{Content: []ResultBlock{{Type: "text", Text: text}}}
}

// ErrorResult constructs a Result that signals an in-band error.
func ErrorResult(text string) Result {
	return Result{
		IsError: true,
		Content: []ResultBlock{{Type: "text", Text: text}},
	}
}

// Registry holds the set of tools available to the agent loop. It's a
// thin map; the agent looks up tools by name from `tool_use` events.
type Registry struct {
	tools map[string]Tool
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

// Register adds a tool. Replaces any existing tool with the same name.
func (r *Registry) Register(t Tool) { r.tools[t.Name()] = t }

// Lookup returns the tool registered under the given name, if any.
func (r *Registry) Lookup(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// All returns the tools in registration order is undefined; callers that
// need a stable order should sort by Name().
func (r *Registry) All() []Tool {
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}
