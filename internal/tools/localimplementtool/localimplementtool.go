// Package localimplementtool exposes a configured local/private MCP
// implementation model as a first-class agent tool.
package localimplementtool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
)

const (
	toolName             = "LocalImplement"
	defaultServer        = "local-router"
	defaultImplementTool = "local_implement"
)

// Caller is the MCP call surface used by Tool. *mcp.Manager satisfies it.
type Caller interface {
	CallTool(ctx context.Context, qualifiedName string, input []byte) (mcp.CallResult, error)
}

// Config identifies the local implementation target.
type Config struct {
	Server        string
	ImplementTool string
	Model         string
}

// ConfigResolver returns the current local implementation target.
type ConfigResolver func() (Config, bool)

// Tool asks a local/private model for a bounded implementation draft.
type Tool struct {
	caller  Caller
	cfg     Config
	resolve ConfigResolver
}

// New returns a LocalImplement tool for cfg.
func New(caller Caller, cfg Config) *Tool {
	if cfg.Server == "" {
		cfg.Server = defaultServer
	}
	if cfg.ImplementTool == "" {
		cfg.ImplementTool = defaultImplementTool
	}
	return &Tool{caller: caller, cfg: cfg}
}

// NewDynamic returns a LocalImplement tool whose target is resolved whenever
// the tool is described or executed. This lets role changes in conduit.json
// take effect without rebuilding the registry.
func NewDynamic(caller Caller, resolve ConfigResolver) *Tool {
	return &Tool{caller: caller, resolve: resolve}
}

func (*Tool) Name() string { return toolName }

func (t *Tool) Description() string {
	cfg, ok := t.config()
	target := "the configured implement role"
	if ok {
		target = cfg.Server
		if cfg.Model != "" {
			target = cfg.Model + " on " + cfg.Server
		}
	}
	return "Offload a small, bounded implementation draft to the configured local/private model (" + target + "). " +
		"Use this when a local model can draft a focused diff or code change from explicit requirements and supplied context. " +
		"Read any required files first and include the relevant context in the prompt. " +
		"Ask for a unified diff when changing existing files, include non-goals, and keep the request narrow. " +
		"The tool returns a draft diff or implementation text only; review it before applying changes. " +
		"Do not use it for broad architecture, ambiguous product decisions, or work that requires hidden conversation context."
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"prompt": {
				"type": "string",
				"description": "Specific implementation request and acceptance criteria for the local model."
			},
			"context": {
				"type": "string",
				"description": "Relevant file excerpts, helper APIs, constraints, or repository facts the local model needs."
			},
			"files": {
				"type": "array",
				"description": "Repository paths the request concerns. Include excerpts in context when needed.",
				"items": {"type": "string"}
			}
		},
		"required": ["prompt"]
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool        { return true }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

type input struct {
	Prompt  string   `json:"prompt"`
	Context string   `json:"context,omitempty"`
	Files   []string `json:"files,omitempty"`
}

func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	if t.caller == nil {
		return tool.ErrorResult("LocalImplement unavailable: MCP manager is not configured."), nil
	}
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return tool.ErrorResult("prompt is required"), nil
	}
	cfg, ok := t.config()
	if !ok {
		return tool.ErrorResult("LocalImplement unavailable: no connected MCP server exposes local_implement."), nil
	}

	prompt := buildPrompt(in)
	args := map[string]any{
		"prompt":                  prompt,
		"output_format":           "diff",
		"include_review_reminder": false,
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid local implement payload: %v", err)), nil
	}

	qualified := mcp.ToolNamePrefix(cfg.Server) + cfg.ImplementTool
	result, err := t.caller.CallTool(ctx, qualified, payload)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	text := flattenText(result)
	if result.IsError {
		if strings.TrimSpace(text) == "" {
			text = "local implement tool returned an error"
		}
		return tool.ErrorResult(text), nil
	}
	if strings.TrimSpace(text) == "" {
		text = "(empty local implement response)"
	}
	return tool.TextResult(text), nil
}

func (t *Tool) config() (Config, bool) {
	if t.resolve != nil {
		cfg, ok := t.resolve()
		if ok {
			if cfg.Server == "" {
				cfg.Server = defaultServer
			}
			if cfg.ImplementTool == "" {
				cfg.ImplementTool = defaultImplementTool
			}
			return cfg, true
		}
	}
	if t.cfg.Server == "" && t.cfg.ImplementTool == "" && t.cfg.Model == "" {
		return Config{}, false
	}
	cfg := t.cfg
	if cfg.Server == "" {
		cfg.Server = defaultServer
	}
	if cfg.ImplementTool == "" {
		cfg.ImplementTool = defaultImplementTool
	}
	return cfg, true
}

func buildPrompt(in input) string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(in.Prompt))
	if len(in.Files) > 0 {
		sb.WriteString("\n\nTarget files:\n")
		for _, f := range in.Files {
			if f = strings.TrimSpace(f); f != "" {
				sb.WriteString("- ")
				sb.WriteString(f)
				sb.WriteString("\n")
			}
		}
	}
	if strings.TrimSpace(in.Context) != "" {
		sb.WriteString("\n\nContext:\n")
		sb.WriteString(strings.TrimSpace(in.Context))
	}
	return sb.String()
}

func flattenText(result mcp.CallResult) string {
	var parts []string
	for _, block := range result.Content {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// ResolveConfig chooses the local implementation provider from the configured
// implement provider when possible, otherwise from connected MCP servers
// exposing local_implement.
func ResolveConfig(manager *mcp.Manager, provider *settings.ActiveProviderSettings) (Config, bool) {
	if manager == nil {
		return Config{}, false
	}
	if provider != nil && provider.Kind == "mcp" {
		cfg := Config{
			Server:        provider.Server,
			ImplementTool: provider.ImplementTool,
			Model:         provider.Model,
		}
		if cfg.Server == "" {
			cfg.Server = defaultServer
		}
		if cfg.ImplementTool == "" {
			cfg.ImplementTool = defaultImplementTool
		}
		if hasTool(manager, cfg.Server, cfg.ImplementTool) {
			if cfg.Model == "" {
				cfg.Model = modelName(manager, cfg.Server)
			}
			return cfg, true
		}
	}

	for _, preferred := range []string{defaultServer, ""} {
		for _, srv := range manager.Servers() {
			if srv == nil || srv.Status != mcp.StatusConnected {
				continue
			}
			if preferred != "" && srv.Name != preferred {
				continue
			}
			if serverHasTool(srv, defaultImplementTool) {
				return Config{
					Server:        srv.Name,
					ImplementTool: defaultImplementTool,
					Model:         srv.Config.Env["LOCAL_LLM_MODEL"],
				}, true
			}
		}
	}
	return Config{}, false
}

func hasTool(manager *mcp.Manager, server, name string) bool {
	for _, srv := range manager.Servers() {
		if srv == nil || srv.Name != server || srv.Status != mcp.StatusConnected {
			continue
		}
		return serverHasTool(srv, name)
	}
	return false
}

func serverHasTool(srv *mcp.ConnectedServer, name string) bool {
	for _, t := range srv.Tools {
		if t.Name == name {
			return true
		}
	}
	return false
}

func modelName(manager *mcp.Manager, server string) string {
	for _, srv := range manager.Servers() {
		if srv != nil && srv.Name == server {
			return srv.Config.Env["LOCAL_LLM_MODEL"]
		}
	}
	return ""
}
