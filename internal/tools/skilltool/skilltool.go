// Package skilltool implements the SkillTool — the agent's way to invoke
// slash-command skills (plugin commands and ~/.claude/commands/*.md files).
//
// When the model calls SkillTool with a skill name, the tool:
//  1. Looks up the command body from the loaded plugin/skill registry.
//  2. Substitutes $ARGUMENTS with the supplied args string.
//  3. Runs a nested agent loop with the skill body as the first user message.
//  4. Returns the nested agent's final text response.
//
// This mirrors SkillTool.ts executeForkedSkill() — a forked sub-agent that
// runs the skill prompt in an isolated context and returns its result.
//
// Reference: src/tools/SkillTool/SkillTool.ts
package skilltool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/tool"
)

// SubAgent is the minimal interface the SkillTool needs from the agent loop
// to spawn a nested run.
type SubAgent interface {
	Run(ctx context.Context, messages []api.Message, handler func(interface{})) ([]api.Message, error)
}

// Command is one resolved skill command, ready to execute.
type Command struct {
	// QualifiedName is the slash-command name ("pluginName:commandName" or just "commandName").
	QualifiedName string
	// Description is shown in /skills and the system prompt skill listing.
	Description string
	// Body is the markdown body (frontmatter stripped).
	Body string
	// Tools is the optional tool allowlist from frontmatter. Empty = inherit all tools.
	Tools []string
}

// Loader discovers available skill commands. Implemented by the plugin loader.
type Loader interface {
	// FindCommand looks up a command by name (with or without leading slash,
	// with or without plugin prefix). Returns nil if not found.
	FindCommand(name string) *Command
}

// Tool implements the SkillTool.
type Tool struct {
	loader   Loader
	runAgent func(ctx context.Context, prompt string) (string, error)
	// runTools is an optional callback for tool-restricted execution. When non-nil
	// and a skill specifies a tools allowlist, it is called instead of runAgent.
	// tools is the resolved allowlist after alias normalisation.
	runTools func(ctx context.Context, prompt string, tools []string) (string, error)
}

// New returns a SkillTool.
// runAgent runs an unrestricted nested agent loop. runTools is optional; when
// provided it is called for skills that declare a tools allowlist, enabling
// per-skill tool restriction (CC parity). Pass nil to disable restriction.
func New(loader Loader, runAgent func(ctx context.Context, prompt string) (string, error), runTools func(ctx context.Context, prompt string, tools []string) (string, error)) *Tool {
	return &Tool{loader: loader, runAgent: runAgent, runTools: runTools}
}

func (*Tool) Name() string { return "SkillTool" }

func (*Tool) Description() string {
	return "Invokes a slash-command skill by name. " +
		"Skills are markdown prompt files from installed plugins or ~/.claude/commands/. " +
		"Use the `skill` parameter for the command name (e.g. \"commit\", \"review-pr\", \"agent-sdk-dev:new-sdk-app\"). " +
		"Optionally pass `args` to substitute into $ARGUMENTS in the skill body."
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"skill": {
				"type": "string",
				"description": "The skill name. E.g. \"commit\", \"review-pr\", or \"agent-sdk-dev:new-sdk-app\""
			},
			"args": {
				"type": "string",
				"description": "Optional arguments substituted into $ARGUMENTS in the skill body"
			}
		},
		"required": ["skill"]
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool        { return false }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

type Input struct {
	Skill string `json:"skill"`
	Args  string `json:"args,omitempty"`
}

func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("skilltool: invalid input: %v", err)), nil
	}

	name := strings.TrimPrefix(strings.TrimSpace(in.Skill), "/")
	if name == "" {
		return tool.ErrorResult("skilltool: skill name is required"), nil
	}

	cmd := t.loader.FindCommand(name)
	if cmd == nil {
		return tool.ErrorResult(fmt.Sprintf("skilltool: unknown skill %q", name)), nil
	}

	// Substitute $ARGUMENTS.
	prompt := cmd.Body
	if in.Args != "" {
		prompt = strings.ReplaceAll(prompt, "$ARGUMENTS", in.Args)
	} else {
		prompt = strings.ReplaceAll(prompt, "$ARGUMENTS", "")
	}
	prompt = strings.TrimSpace(prompt)

	var result string
	var err error
	if len(cmd.Tools) > 0 && t.runTools != nil {
		result, err = t.runTools(ctx, prompt, cmd.Tools)
	} else {
		result, err = t.runAgent(ctx, prompt)
	}
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("skilltool: %v", err)), nil
	}
	return tool.TextResult(result), nil
}
