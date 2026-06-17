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
	"sort"
	"strings"
	"sync/atomic"

	"github.com/icehunter/conduit/internal/team"
	"github.com/icehunter/conduit/internal/tool"
)

// staticDescription is returned by Description() unconditionally. The agent
// catalog (available subagent_type values and their tool lists) is NOT embedded
// here — doing so re-ships multiple KB of schema on every API request for every
// registered plugin agent. The model can discover available types via ToolSearch
// or by inspecting the subagent_type enum at call time.
const staticDescription = "Launch a sub-agent to handle a complex multi-step task. " +
	"Provide a detailed `prompt` describing exactly what the sub-agent should do. " +
	"Optionally supply a short `description` (shown in the UI while the agent runs). " +
	"Available subagent_type values are listed in the system prompt under 'Available agent types'; " +
	"if none match, omit the subagent_type field."

// AgentDef is the runtime descriptor for a named sub-agent, provided by the
// plugin agent registry.
type AgentDef struct {
	Name          string
	QualifiedName string
	Description   string
	SystemPrompt  string
	// Model is a literal model ID or a CC alias ("sonnet", "haiku"). Takes
	// precedence over Role when both are set.
	Model string
	// Role is a named provider role (e.g. "background", "planning"). When set
	// and Model is empty, the agent loop resolves the model via RoleResolver.
	Role  string
	Tools []string // nil/empty = inherit parent registry
}

// Registry maps subagent_type names to AgentDef. Implemented by plugins.AgentRegistry.
type Registry interface {
	FindAgent(name string) *AgentDef
	ListAgents() []AgentDef
}

// Tool implements the AgentTool.
type Tool struct {
	tool.NotDeferrable
	// runAgent spawns a nested loop with the given prompt and returns the
	// final assistant text. Provided by main at construction time.
	runAgent func(ctx context.Context, prompt string) (string, error)
	// registry provides named sub-agent definitions. May be nil.
	registry Registry
	// runTyped spawns a nested loop for a named sub-agent, with optional
	// system prompt, model, role, and tool restrictions. May be nil (falls back to runAgent).
	// role is a named provider role; model takes precedence when both are non-empty.
	runTyped func(ctx context.Context, prompt, systemPrompt, model, role string, tools []string) (string, error)
	// spawnTeammate spawns an async teammate when team mode is active.
	// Returns the teammate's ID. nil means fall through to synchronous dispatch.
	spawnTeammate func(ctx context.Context, name, prompt string) (string, error)
	// spawnCount generates unique auto-names (teammate-1, teammate-2, …).
	spawnCount atomic.Uint64
}

// New returns an AgentTool.
// runAgent spawns an unrestricted background sub-agent.
// registry and runTyped are optional (may be nil); when provided they enable
// subagent_type dispatch with per-agent system prompts and tool allowlists.
func New(
	runAgent func(ctx context.Context, prompt string) (string, error),
	registry Registry,
	runTyped func(ctx context.Context, prompt, systemPrompt, model, role string, tools []string) (string, error),
) *Tool {
	return &Tool{runAgent: runAgent, registry: registry, runTyped: runTyped}
}

// WithSpawnTeammate sets the function used to spawn an async teammate when
// team mode is active. Returns the receiver for chaining.
func (t *Tool) WithSpawnTeammate(fn func(ctx context.Context, name, prompt string) (string, error)) *Tool {
	t.spawnTeammate = fn
	return t
}

func (*Tool) Name() string { return "Task" }

func (*Tool) Description() string { return staticDescription }

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
			},
			"name": {
				"type": "string",
				"description": "Optional name for this teammate (agent teams mode only). Auto-generated if omitted (teammate-1, teammate-2, …)."
			},
			"subagent_type": {
				"type": "string",
				"description": "Optional named sub-agent type from an installed plugin (e.g. \"pr-review-toolkit:code-reviewer\"). When set, the sub-agent uses the named agent's system prompt and tool allowlist."
			},
			"role": {
				"type": "string",
				"description": "Named provider role for this sub-agent (e.g. \"background\", \"planning\", \"implement\"). Determines the model and provider used. Overrides the default background model. Ignored when subagent_type is set (the plugin agent's role takes precedence)."
			}
		},
		"required": ["prompt"]
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool        { return false }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

type Input struct {
	Prompt       string `json:"prompt"`
	Description  string `json:"description,omitempty"`
	Name         string `json:"name,omitempty"`
	SubagentType string `json:"subagent_type,omitempty"`
	Role         string `json:"role,omitempty"`
}

func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("agenttool: invalid input: %v", err)), nil
	}
	if in.Prompt == "" {
		return tool.ErrorResult("agenttool: prompt is required"), nil
	}

	// Agent teams: async spawn when active and wired.
	if team.IsActive() && t.spawnTeammate != nil {
		name := in.Name
		if name == "" {
			name = fmt.Sprintf("teammate-%d", t.spawnCount.Add(1))
		}
		id, err := t.spawnTeammate(ctx, name, in.Prompt)
		if err != nil {
			return tool.ErrorResult(fmt.Sprintf("agenttool: spawn %q: %v", name, err)), nil
		}
		return tool.TextResult(fmt.Sprintf("Teammate %q launched (id: %s).", name, id)), nil
	}

	if in.SubagentType != "" && t.registry != nil {
		def := t.registry.FindAgent(in.SubagentType)
		if def == nil {
			available := listNames(t.registry.ListAgents())
			return tool.ErrorResult(fmt.Sprintf(
				"agenttool: unknown subagent_type %q; available: %s", in.SubagentType, available,
			)), nil
		}
		if t.runTyped != nil {
			resolved := resolveToolNames(def.Tools)
			result, err := t.runTyped(ctx, in.Prompt, def.SystemPrompt, def.Model, def.Role, resolved)
			if err != nil {
				return tool.ErrorResult(fmt.Sprintf("agenttool: %v", err)), nil
			}
			return tool.TextResult(result), nil
		}
		// runTyped unavailable — fall through to unrestricted run.
	}

	// Plain Task call: use role from input if provided, otherwise unrestricted.
	if in.Role != "" && t.runTyped != nil {
		result, err := t.runTyped(ctx, in.Prompt, "", "", in.Role, nil)
		if err != nil {
			return tool.ErrorResult(fmt.Sprintf("agenttool: %v", err)), nil
		}
		return tool.TextResult(result), nil
	}

	result, err := t.runAgent(ctx, in.Prompt)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("agenttool: %v", err)), nil
	}
	return tool.TextResult(result), nil
}

// resolveToolNames normalises CC plugin tool names to conduit registry key names.
// CC plugins reference tools by short names ("Read", "Bash"); conduit registers
// them under their full struct name ("FileReadTool", "BashTool").
// Unrecognised names are passed through unchanged (the Registry.Subset call
// will silently skip them, which produces a warning in tests but not panics).
func resolveToolNames(names []string) []string {
	// Maps CC plugin shorthand names to conduit's registered tool names.
	// Most CC names already match conduit's registered names exactly (Glob,
	// Grep, Read, Bash, Edit, Write…) so only genuinely different names need
	// an entry here. Registry.Subset does case-insensitive matching, so
	// capitalisation differences are handled without aliases.
	aliases := map[string]string{
		// CC name   → conduit registered name
		"notebookedit": "NotebookEdit",
		"skill":        "SkillTool",
		// CC-only tools without a direct conduit equivalent — map to closest.
		"ls":           "Glob",
		"notebookread": "Read",
		"killshell":    "Bash",
		"bashoutput":   "Bash",
		"shell":        "Bash",
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if resolved, ok := aliases[strings.ToLower(n)]; ok {
			out = append(out, resolved)
		} else {
			out = append(out, n)
		}
	}
	return out
}

func listNames(agents []AgentDef) string {
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.QualifiedName
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
