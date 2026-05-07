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

	"github.com/icehunter/conduit/internal/tool"
)

// AgentDef is the runtime descriptor for a named sub-agent, provided by the
// plugin agent registry.
type AgentDef struct {
	Name          string
	QualifiedName string
	Description   string
	SystemPrompt  string
	Model         string
	Tools         []string // nil/empty = inherit parent registry
}

// Registry maps subagent_type names to AgentDef. Implemented by plugins.AgentRegistry.
type Registry interface {
	FindAgent(name string) *AgentDef
	ListAgents() []AgentDef
}

// Tool implements the AgentTool.
type Tool struct {
	// runAgent spawns a nested loop with the given prompt and returns the
	// final assistant text. Provided by main at construction time.
	runAgent func(ctx context.Context, prompt string) (string, error)
	// registry provides named sub-agent definitions. May be nil.
	registry Registry
	// runTyped spawns a nested loop for a named sub-agent, with optional
	// system prompt, model, and tool restrictions. May be nil (falls back to runAgent).
	runTyped func(ctx context.Context, prompt, systemPrompt, model string, tools []string) (string, error)
}

// New returns an AgentTool.
// runAgent spawns an unrestricted background sub-agent.
// registry and runTyped are optional (may be nil); when provided they enable
// subagent_type dispatch with per-agent system prompts and tool allowlists.
func New(
	runAgent func(ctx context.Context, prompt string) (string, error),
	registry Registry,
	runTyped func(ctx context.Context, prompt, systemPrompt, model string, tools []string) (string, error),
) *Tool {
	return &Tool{runAgent: runAgent, registry: registry, runTyped: runTyped}
}

func (*Tool) Name() string { return "Task" }

func (t *Tool) Description() string {
	base := "Launch a new agent to handle a specific task. " +
		"The sub-agent has access to all the same tools. " +
		"Provide a detailed `prompt` describing exactly what the agent should do. " +
		"Optionally supply a short `description` (shown in the UI while the agent runs)."

	if t.registry == nil {
		return base
	}
	agents := t.registry.ListAgents()
	if len(agents) == 0 {
		return base
	}

	// Sort for stable output.
	sorted := make([]AgentDef, len(agents))
	copy(sorted, agents)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].QualifiedName < sorted[j].QualifiedName
	})

	var sb strings.Builder
	sb.WriteString(base)
	sb.WriteString("\n\nAvailable agent types and the tools they have access to:\n")
	for _, a := range sorted {
		toolDesc := "All tools"
		if len(a.Tools) > 0 {
			toolDesc = strings.Join(a.Tools, ", ")
		}
		fmt.Fprintf(&sb, "- %s: %s (Tools: %s)\n", a.QualifiedName, a.Description, toolDesc)
	}
	return sb.String()
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
			},
			"subagent_type": {
				"type": "string",
				"description": "Optional named sub-agent type from an installed plugin (e.g. \"pr-review-toolkit:code-reviewer\"). When set, the sub-agent uses the named agent's system prompt and tool allowlist."
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
	SubagentType string `json:"subagent_type,omitempty"`
}

func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("agenttool: invalid input: %v", err)), nil
	}
	if in.Prompt == "" {
		return tool.ErrorResult("agenttool: prompt is required"), nil
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
			result, err := t.runTyped(ctx, in.Prompt, def.SystemPrompt, def.Model, resolved)
			if err != nil {
				return tool.ErrorResult(fmt.Sprintf("agenttool: %v", err)), nil
			}
			return tool.TextResult(result), nil
		}
		// runTyped unavailable — fall through to unrestricted run.
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
	aliases := map[string]string{
		"read":         "FileReadTool",
		"edit":         "FileEditTool",
		"write":        "FileWriteTool",
		"bash":         "BashTool",
		"grep":         "GrepTool",
		"glob":         "GlobTool",
		"task":         "Task",
		"webfetch":     "WebFetch",
		"websearch":    "WebSearch",
		"todowrite":    "TodoWriteTool",
		"notebookedit": "NotebookEditTool",
		"lsp":          "LSP",
		"sleep":        "SleepTool",
		"repl":         "REPL",
		"mcp":          "mcp",
		"skill":        "SkillTool",
		"config":       "Config",
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
