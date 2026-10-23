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
	"strings"
	"sync"
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

// DeferrableChecker is an optional extension of Tool. When a tool implements
// this interface and Deferrable() returns true, buildToolDefs omits its full
// schema from the API request when ToolSearch is also registered — the model
// discovers and fetches the schema on demand via ToolSearch. Tools that do not
// implement this interface are treated as non-deferrable (always included).
type DeferrableChecker interface {
	// Deferrable reports whether this tool's full schema may be withheld
	// from the API request when ToolSearch is registered. Return false to
	// always appear in every request.
	Deferrable() bool
}

// IsDeferrable reports whether t opts into deferred schema sending. Tools that
// do not implement DeferrableChecker are never deferred (false by default),
// so existing tools require no changes.
func IsDeferrable(t Tool) bool {
	if d, ok := t.(DeferrableChecker); ok {
		return d.Deferrable()
	}
	return false
}

// NotDeferrable is an embed struct that explicitly satisfies DeferrableChecker
// with a permanent false return. Embed it in any tool that must always appear
// in every API request to make the intent clear in code.
//
// Example:
//
//	type MyTool struct {
//	    tool.NotDeferrable
//	    // ...
//	}
type NotDeferrable struct{}

// Deferrable always returns false — the tool is always sent to the model.
func (NotDeferrable) Deferrable() bool { return false }

// EnabledChecker is an optional extension of Tool. When a tool implements this
// interface and Enabled() returns false, the tool is excluded from the API
// request entirely — the model never sees it and cannot call it. Mirrors
// Tool.ts:isEnabled(); a tool whose precondition is unmet (e.g. the LSP tool
// before any language server has connected) is simply not offered.
type EnabledChecker interface {
	// Enabled reports whether the tool should be included in the API request's
	// tools array. Return false to hide the tool until its precondition is met.
	Enabled() bool
}

// IsEnabled reports whether t should be included in the API request. Tools that
// do not implement EnabledChecker are always enabled (true by default), so
// existing tools require no changes.
func IsEnabled(t Tool) bool {
	if e, ok := t.(EnabledChecker); ok {
		return e.Enabled()
	}
	return true
}

// OutputSchemaProvider is an optional extension of Tool. When a tool implements
// this interface, its OutputSchema() JSON Schema is sent in the API request as
// the tool's output_schema, so the model returns structured results conforming
// to it. Mirrors Tool.ts:outputSchema. Tools that do not implement it declare no
// output schema.
type OutputSchemaProvider interface {
	// OutputSchema returns the JSON Schema for the tool's result. Returned bytes
	// must be a complete {"type":"object", ...} object.
	OutputSchema() json.RawMessage
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
	// StopTurn, when true, tells the agent loop to end the current turn
	// after appending this tool_result — instead of making another API
	// call. Used by tools that hand control back to the user (ExitPlanMode
	// "discuss", AskUserQuestion dismiss) so the model does not keep
	// proposing. The tool_result is still recorded in history; the model
	// reads it plus the user's next message on the following turn.
	StopTurn bool
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

// StopTurnResult constructs a Result that signals the agent loop to end the
// current turn and return control to the user. The tool_result is still
// recorded in history so the model can read it on the next turn.
func StopTurnResult(text string) Result {
	r := TextResult(text)
	r.StopTurn = true
	return r
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
//
// It is safe for concurrent use: MCP tools are registered and removed while
// the session runs (servers connect in the background, reconnect, finish
// OAuth) as the agent loop and ToolSearch read the registry.
//
// WithOverrides and Subset return views for sub-agents and teammates. A view
// holds tools of its own and shows its base registry's tools through as they
// are at each call, so a tool the base gains or loses later (an MCP server
// connecting mid-session) reaches a long-lived child too.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool

	// base, when set, makes this registry a view: a name it doesn't hold
	// itself resolves in base at call time. allow, when non-nil, limits the
	// names that show through from base (keys lowercased).
	base  *Registry
	allow map[string]bool
}

// RegistryBound is implemented by tools that read the registry they belong to
// (ToolSearch). A view made by WithOverrides or Subset holds a copy bound to
// itself, so the tool reports what the view's own loop can call rather than
// everything the base has.
type RegistryBound interface {
	Tool
	BindRegistry(r *Registry) Tool
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

// Register adds a tool. Replaces any existing tool with the same name. On a
// view the tool is the view's own; the base is never changed.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	r.tools[t.Name()] = t
	r.mu.Unlock()
}

// Unregister removes the tool registered under name and reports whether
// there was one. On a view it removes only the view's own tool, and a base
// tool of the same name shows through again.
func (r *Registry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.tools[name]
	delete(r.tools, name)
	return ok
}

// Reconcile replaces the slice of the registry that owned selects with want,
// in one step. A tool owned reports true for is removed unless want holds a
// tool of the same name; every tool in want is registered unless its name is
// held by a tool owned reports false for, which is never displaced. Readers
// see the registry from before or after the call, never part-way. On a view
// it works on the view's own tools only.
func (r *Registry) Reconcile(owned func(Tool) bool, want []Tool) {
	keep := make(map[string]Tool, len(want))
	for _, t := range want {
		keep[t.Name()] = t
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for name, t := range r.tools {
		if _, wanted := keep[name]; !wanted && owned(t) {
			delete(r.tools, name)
		}
	}
	for name, t := range keep {
		if cur, ok := r.tools[name]; ok && !owned(cur) {
			continue
		}
		r.tools[name] = t
	}
}

// Lookup returns the tool registered under the given name, if any.
func (r *Registry) Lookup(name string) (Tool, bool) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	if ok {
		return t, true
	}
	if r.base == nil || !r.showsFromBase(name) {
		return nil, false
	}
	return r.base.Lookup(name)
}

// All returns the tools in registration order is undefined; callers that
// need a stable order should sort by Name().
func (r *Registry) All() []Tool {
	r.mu.RLock()
	out := make([]Tool, 0, len(r.tools))
	own := make(map[string]bool, len(r.tools))
	for name, t := range r.tools {
		out = append(out, t)
		own[name] = true
	}
	r.mu.RUnlock()
	if r.base == nil {
		return out
	}
	for _, t := range r.base.All() {
		if name := t.Name(); !own[name] && r.showsFromBase(name) {
			out = append(out, t)
		}
	}
	return out
}

// WithOverrides returns a view of the Registry in which the given tools
// replace any same-named ones. Used to give sub-agents scoped versions of
// mode-change tools without mutating the parent registry.
func (r *Registry) WithOverrides(tools ...Tool) *Registry {
	own := make(map[string]Tool, len(tools))
	for _, t := range tools {
		own[t.Name()] = t
	}
	return r.view(own, nil)
}

// Subset returns a view of the Registry showing only the tools whose Name()
// matches one of the provided names. Matching is case-insensitive.
// names may use the CC plugin convention (e.g. "Read", "Bash") or the
// internal tool name (e.g. "FileReadTool", "BashTool"); the alias map
// in internal/tools/agenttool normalises these before calling Subset.
// An empty or nil names slice returns a view showing nothing.
func (r *Registry) Subset(names []string) *Registry {
	allow := make(map[string]bool, len(names))
	for _, n := range names {
		allow[strings.ToLower(n)] = true
	}
	return r.view(map[string]Tool{}, allow)
}

// view returns a view of r holding own. Every RegistryBound tool it would
// show from r is replaced in own by a copy bound to the view.
func (r *Registry) view(own map[string]Tool, allow map[string]bool) *Registry {
	v := &Registry{tools: own, base: r, allow: allow}
	for _, t := range r.All() {
		b, ok := t.(RegistryBound)
		if !ok {
			continue
		}
		name := t.Name()
		if _, overridden := own[name]; overridden || !v.showsFromBase(name) {
			continue
		}
		own[name] = b.BindRegistry(v)
	}
	return v
}

// showsFromBase reports whether a view shows its base's tool of that name.
func (r *Registry) showsFromBase(name string) bool {
	return r.allow == nil || r.allow[strings.ToLower(name)]
}
