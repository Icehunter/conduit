// Package localimplementtool exposes a configured local/private MCP
// implementation model as a first-class agent tool.
package localimplementtool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
)

const (
	toolName             = "LocalImplement"
	defaultServer        = "local-router"
	defaultImplementTool = "local_implement"
	coderTier            = "coder"
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
	// SupportsFiles is true when the target tool's published input schema
	// declares "files" and "root" properties, meaning the MCP server reads
	// files itself instead of requiring the caller to inline their content.
	SupportsFiles bool
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
	desc := "Offload a small, bounded implementation draft to the configured local/private model (" + target + "). " +
		"Use this when a local model can draft a focused diff or code change from explicit requirements and supplied context. "
	if ok && cfg.SupportsFiles {
		desc += "This target reads files itself: pass repository-relative paths in \"files\" and it will read them " +
			"without you pre-reading them into your own context. Only inline excerpts in \"context\" for content that " +
			"doesn't live in a file (e.g. a paste, an error message). "
	} else {
		desc += "Read any required files first and include the relevant context in the prompt. "
	}
	desc += "Ask for a unified diff when changing existing files, include non-goals, and keep the request narrow. " +
		"The tool returns a draft diff or implementation text only; review it before applying changes. " +
		"Do not use it for broad architecture, ambiguous product decisions, or work that requires hidden conversation context."
	return desc
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
				"description": "Relevant excerpts, helper APIs, constraints, or repository facts the local model needs that don't live in a file."
			},
			"files": {
				"type": "array",
				"description": "Repository paths the request concerns. When the target supports file reads, these are read by the local server itself; otherwise include their content in context.",
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

	args := map[string]any{
		"output_format":           "diff",
		"include_review_reminder": false,
	}
	if cfg.SupportsFiles && len(in.Files) > 0 {
		root, err := tool.Cwd(ctx)
		if err != nil {
			return tool.ErrorResult(fmt.Sprintf("could not resolve working directory: %v", err)), nil
		}
		args["prompt"] = buildPrompt(input{Prompt: in.Prompt, Context: in.Context})
		args["files"] = in.Files
		args["root"] = root
	} else {
		args["prompt"] = buildPrompt(in)
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
		if srv, ok := findServer(manager, cfg.Server, cfg.ImplementTool); ok {
			if cfg.Model == "" {
				cfg.Model = srv.Config.Env["LOCAL_LLM_MODEL"]
			}
			cfg.SupportsFiles = toolSupportsFiles(srv, cfg.ImplementTool)
			return cfg, true
		}
	}

	if srv, ok := bestImplementServer(manager); ok {
		return Config{
			Server:        srv.Name,
			ImplementTool: defaultImplementTool,
			Model:         srv.Config.Env["LOCAL_LLM_MODEL"],
			SupportsFiles: toolSupportsFiles(srv, defaultImplementTool),
		}, true
	}
	return Config{}, false
}

// bestImplementServer picks the connected server exposing local_implement
// deterministically: a server tagged LOCAL_LLM_TIER=coder wins first (the
// coder tier is the only one meant to serve implement/fix), then the
// conventional "local-router" name, then the alphabetically-first candidate.
// manager.Servers() iterates an internal map, so without this ordering the
// pick is effectively random across restarts — including landing on a CPU
// helper instance for code-writing work.
func bestImplementServer(manager *mcp.Manager) (*mcp.ConnectedServer, bool) {
	var candidates []*mcp.ConnectedServer
	for _, srv := range manager.Servers() {
		if srv != nil && srv.Status == mcp.StatusConnected && serverHasTool(srv, defaultImplementTool) {
			candidates = append(candidates, srv)
		}
	}
	if len(candidates) == 0 {
		return nil, false
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
	for _, c := range candidates {
		if strings.EqualFold(c.Config.Env["LOCAL_LLM_TIER"], coderTier) {
			return c, true
		}
	}
	for _, c := range candidates {
		if c.Name == defaultServer {
			return c, true
		}
	}
	return candidates[0], true
}

func findServer(manager *mcp.Manager, server, toolName string) (*mcp.ConnectedServer, bool) {
	for _, srv := range manager.Servers() {
		if srv == nil || srv.Name != server || srv.Status != mcp.StatusConnected {
			continue
		}
		if serverHasTool(srv, toolName) {
			return srv, true
		}
	}
	return nil, false
}

func serverHasTool(srv *mcp.ConnectedServer, name string) bool {
	_, ok := toolDef(srv, name)
	return ok
}

func toolDef(srv *mcp.ConnectedServer, name string) (mcp.ToolDef, bool) {
	for _, t := range srv.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return mcp.ToolDef{}, false
}

// toolSupportsFiles reports whether a tool's published input schema declares
// "files" and "root" properties, meaning the server reads files itself.
func toolSupportsFiles(srv *mcp.ConnectedServer, toolName string) bool {
	td, ok := toolDef(srv, toolName)
	if !ok || len(td.InputSchema) == 0 {
		return false
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(td.InputSchema, &schema); err != nil {
		return false
	}
	_, hasFiles := schema.Properties["files"]
	_, hasRoot := schema.Properties["root"]
	return hasFiles && hasRoot
}
