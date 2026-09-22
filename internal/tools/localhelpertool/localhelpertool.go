// Package localhelpertool exposes a configured local/private MCP helper
// model (summarization, extraction, classification) as a first-class agent
// tool, mirroring localimplementtool but for the non-code-writing tier.
package localhelpertool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/tool"
)

const (
	toolName          = "LocalHelper"
	defaultServer     = "local-helper"
	defaultDirectTool = "local_direct"
	helperTier        = "helper"
)

// Caller is the MCP call surface used by Tool. *mcp.Manager satisfies it.
type Caller interface {
	CallTool(ctx context.Context, qualifiedName string, input []byte) (mcp.CallResult, error)
}

// Config identifies the local helper target.
type Config struct {
	Server     string
	DirectTool string
	Model      string
	// SupportsFiles is true when the target tool's published input schema
	// declares "files" and "root" properties, meaning the MCP server reads
	// files itself instead of requiring the caller to inline their content.
	SupportsFiles bool
}

// ConfigResolver returns the current local helper target.
type ConfigResolver func() (Config, bool)

// Tool sends non-code-writing text work (summarize, extract, explain,
// classify) to a local/private helper model.
type Tool struct {
	caller  Caller
	cfg     Config
	resolve ConfigResolver
}

// New returns a LocalHelper tool for cfg.
func New(caller Caller, cfg Config) *Tool {
	if cfg.Server == "" {
		cfg.Server = defaultServer
	}
	if cfg.DirectTool == "" {
		cfg.DirectTool = defaultDirectTool
	}
	return &Tool{caller: caller, cfg: cfg}
}

// NewDynamic returns a LocalHelper tool whose target is resolved whenever
// the tool is described or executed.
func NewDynamic(caller Caller, resolve ConfigResolver) *Tool {
	return &Tool{caller: caller, resolve: resolve}
}

func (*Tool) Name() string { return toolName }

func (t *Tool) Description() string {
	cfg, ok := t.config()
	target := "the configured helper role"
	if ok {
		target = cfg.Server
		if cfg.Model != "" {
			target = cfg.Model + " on " + cfg.Server
		}
	}
	desc := "Offload a non-code-writing text job to a local/private helper model (" + target + "): " +
		"summarize, extract, explain, or classify. Cheap and fast — prefer this over doing the job " +
		"yourself when it fits one of those shapes, to save your own context budget. "
	if ok && cfg.SupportsFiles {
		desc += "This target reads files itself: pass repository-relative paths in \"files\" and it will read them " +
			"without you pre-reading them into your own context. "
	}
	desc += "\"task\" is required: summarize | extract | explain | classify. " +
		"classify requires at least two \"examples\" ({input, output} pairs) — without few-shot examples a small " +
		"model returns the same label for everything. summarize/extract accept \"max_lines\". " +
		"Do not use this for code generation or diffs — use LocalImplement for that."
	return desc
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"task": {
				"type": "string",
				"enum": ["summarize", "extract", "explain", "classify"],
				"description": "What kind of job this is. Required."
			},
			"prompt": {
				"type": "string",
				"description": "The text to summarize/extract/explain/classify, or the instruction plus inline content."
			},
			"files": {
				"type": "array",
				"description": "Repository paths to read instead of inlining content, when the target supports file reads.",
				"items": {"type": "string"}
			},
			"examples": {
				"type": "array",
				"description": "Required for task=classify: at least two {input, output} few-shot pairs.",
				"items": {
					"type": "object",
					"properties": {
						"input": {"type": "string"},
						"output": {"type": "string"}
					},
					"required": ["input", "output"]
				}
			},
			"max_lines": {
				"type": "integer",
				"description": "Caps output lines. Valid with summarize/extract."
			}
		},
		"required": ["task", "prompt"]
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool        { return true }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

type example struct {
	Input  string `json:"input"`
	Output string `json:"output"`
}

type input struct {
	Task     string    `json:"task"`
	Prompt   string    `json:"prompt"`
	Files    []string  `json:"files,omitempty"`
	Examples []example `json:"examples,omitempty"`
	MaxLines int       `json:"max_lines,omitempty"`
}

func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	if t.caller == nil {
		return tool.ErrorResult("LocalHelper unavailable: MCP manager is not configured."), nil
	}
	var in input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid input: %v", err)), nil
	}
	if strings.TrimSpace(in.Task) == "" {
		return tool.ErrorResult("task is required (summarize, extract, explain, or classify)"), nil
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return tool.ErrorResult("prompt is required"), nil
	}
	if in.Task == "classify" && len(in.Examples) < 2 {
		return tool.ErrorResult("task=classify requires at least two examples ({input, output} pairs); zero-shot returns the same label for every input"), nil
	}
	cfg, ok := t.config()
	if !ok {
		return tool.ErrorResult("LocalHelper unavailable: no connected MCP server exposes local_direct."), nil
	}

	args := map[string]any{
		"task":                    in.Task,
		"include_review_reminder": false,
	}
	if cfg.SupportsFiles && len(in.Files) > 0 {
		root, err := tool.Cwd(ctx)
		if err != nil {
			return tool.ErrorResult(fmt.Sprintf("could not resolve working directory: %v", err)), nil
		}
		args["prompt"] = in.Prompt
		args["files"] = in.Files
		args["root"] = root
	} else {
		args["prompt"] = buildPrompt(in)
	}
	if len(in.Examples) > 0 {
		examples := make([]map[string]string, 0, len(in.Examples))
		for _, e := range in.Examples {
			examples = append(examples, map[string]string{"input": e.Input, "output": e.Output})
		}
		args["examples"] = examples
	}
	if in.MaxLines > 0 {
		args["max_lines"] = in.MaxLines
	}
	payload, err := json.Marshal(args)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("invalid local helper payload: %v", err)), nil
	}

	qualified := mcp.ToolNamePrefix(cfg.Server) + cfg.DirectTool
	result, err := t.caller.CallTool(ctx, qualified, payload)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	text := flattenText(result)
	if result.IsError {
		if strings.TrimSpace(text) == "" {
			text = "local helper tool returned an error"
		}
		return tool.ErrorResult(text), nil
	}
	if strings.TrimSpace(text) == "" {
		text = "(empty local helper response)"
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
			if cfg.DirectTool == "" {
				cfg.DirectTool = defaultDirectTool
			}
			return cfg, true
		}
	}
	if t.cfg.Server == "" && t.cfg.DirectTool == "" && t.cfg.Model == "" {
		return Config{}, false
	}
	cfg := t.cfg
	if cfg.Server == "" {
		cfg.Server = defaultServer
	}
	if cfg.DirectTool == "" {
		cfg.DirectTool = defaultDirectTool
	}
	return cfg, true
}

func buildPrompt(in input) string {
	if len(in.Files) == 0 {
		return strings.TrimSpace(in.Prompt)
	}
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(in.Prompt))
	sb.WriteString("\n\nTarget files:\n")
	for _, f := range in.Files {
		if f = strings.TrimSpace(f); f != "" {
			sb.WriteString("- ")
			sb.WriteString(f)
			sb.WriteString("\n")
		}
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

// ResolveConfig chooses the local helper provider from connected MCP servers
// exposing local_direct, preferring a server tagged LOCAL_LLM_TIER=helper.
func ResolveConfig(manager *mcp.Manager) (Config, bool) {
	if manager == nil {
		return Config{}, false
	}
	srv, ok := bestHelperServer(manager)
	if !ok {
		return Config{}, false
	}
	return Config{
		Server:        srv.Name,
		DirectTool:    defaultDirectTool,
		Model:         srv.Config.Env["LOCAL_LLM_MODEL"],
		SupportsFiles: toolSupportsFiles(srv, defaultDirectTool),
	}, true
}

// bestHelperServer picks the connected server exposing local_direct
// deterministically: a server tagged LOCAL_LLM_TIER=helper wins first, then
// the conventional "local-helper" name, then the alphabetically-first
// candidate. See localimplementtool.bestImplementServer for why this needs
// to be deterministic rather than a map-order pick.
func bestHelperServer(manager *mcp.Manager) (*mcp.ConnectedServer, bool) {
	var candidates []*mcp.ConnectedServer
	for _, srv := range manager.Servers() {
		if srv != nil && srv.Status == mcp.StatusConnected && serverHasTool(srv, defaultDirectTool) {
			candidates = append(candidates, srv)
		}
	}
	if len(candidates) == 0 {
		return nil, false
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Name < candidates[j].Name })
	for _, c := range candidates {
		if strings.EqualFold(c.Config.Env["LOCAL_LLM_TIER"], helperTier) {
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
