// Package localimplementtool exposes a configured local/private MCP
// implementation model as a first-class agent tool.
package localimplementtool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/icehunter/conduit/internal/implguard"
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
	// SupportsTrace requires the explicitly advertised boolean include_trace.
	SupportsTrace bool
	// SupportedTasks is nil for legacy tools without a task property.
	SupportedTasks []string
	// SupportsTargets is true when the target tool's input schema declares
	// "targets" (and "root"): the server applies the model's edits itself.
	// The server advertises it only when it has writeRoots configured.
	SupportsTargets bool
}

const (
	maxTargets     = 20
	appliedMarker  = "[local-router] APPLIED: wrote "
	noTargetsError = "LocalImplement requires a local-router instance that supports \"targets\" (apply mode) and has writeRoots configured; this target does not advertise it."
)

// Option customizes a Tool.
type Option func(*Tool)

// WithAuthorizer installs the per-target write check. It receives the absolute
// path of each target before the server is called; a non-nil error blocks the
// whole call.
func WithAuthorizer(fn func(ctx context.Context, absPath string) error) Option {
	return func(t *Tool) { t.authorize = fn }
}

// WithGuard sets the implement guard that records every file the server
// applies.
func WithGuard(g *implguard.Guard) Option {
	return func(t *Tool) { t.guard = g }
}

// ConfigResolver returns the current local implementation target.
type ConfigResolver func() (Config, bool)

// Tool asks a local/private model to implement a bounded change and has the
// MCP server apply it to the named target files.
type Tool struct {
	caller    Caller
	cfg       Config
	resolve   ConfigResolver
	authorize func(ctx context.Context, absPath string) error
	guard     *implguard.Guard
}

// New returns a LocalImplement tool for cfg.
func New(caller Caller, cfg Config, opts ...Option) *Tool {
	if cfg.Server == "" {
		cfg.Server = defaultServer
	}
	if cfg.ImplementTool == "" {
		cfg.ImplementTool = defaultImplementTool
	}
	t := &Tool{caller: caller, cfg: cfg}
	for _, o := range opts {
		o(t)
	}
	return t
}

// NewDynamic returns a LocalImplement tool whose target is resolved whenever
// the tool is described or executed. This lets role changes in conduit.json
// take effect without rebuilding the registry.
func NewDynamic(caller Caller, resolve ConfigResolver, opts ...Option) *Tool {
	t := &Tool{caller: caller, resolve: resolve}
	for _, o := range opts {
		o(t)
	}
	return t
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
	desc := "Offload a small, bounded implementation to the configured local/private model (" + target + "). " +
		"The model writes the change and the server applies it directly to the files listed in \"targets\" " +
		"(created if missing); the tool returns the applied diff. Review the diff. Code edits go through this tool while it is available; " +
		"until then you may still read files, run commands and use git, and you may fix the target files afterward. " +
		"If the local implementer is unreachable this tool is withdrawn and you edit files directly. "
	if ok && cfg.SupportsFiles {
		desc += "The server reads files itself: pass repository-relative paths in \"files\" instead of pre-reading them. " +
			"Only inline excerpts in \"context\" for content that doesn't live in a file. "
	} else {
		desc += "Read any required files first and include the relevant context in the prompt. "
	}
	desc += "Do not specify an output format in the prompt; the server chooses it. " +
		"Include non-goals and keep the request narrow. " +
		"Do not use it for broad architecture, ambiguous product decisions, or work that requires hidden conversation context."
	return desc
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
 "task": {"type":"string","enum":["implement","fix"],"description":"Implementation or repair draft; defaults to implement."},
			"targets": {
				"type": "array",
				"description": "Repository-relative paths of the files to create or modify (1-20). The server writes only these files.",
				"items": {"type": "string"},
				"minItems": 1,
				"maxItems": 20
			},
			"prompt": {
				"type": "string",
				"description": "Specific implementation request and acceptance criteria for the local model. Describe what the code must do, not how to format the reply: the server sets the output format, and instructions like \"output only the script\" or \"reply in a code block\" make it discard the answer."
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
		"required": ["prompt", "targets"]
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool        { return false }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

type input struct {
	Task    string   `json:"task,omitempty"`
	Prompt  string   `json:"prompt"`
	Context string   `json:"context,omitempty"`
	Files   []string `json:"files,omitempty"`
	Targets []string `json:"targets"`
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

	if in.Task == "" {
		in.Task = "implement"
	}
	if in.Task != "implement" && in.Task != "fix" {
		return tool.ErrorResult("task must be implement or fix"), nil
	}
	if cfg.SupportedTasks != nil && !slices.Contains(cfg.SupportedTasks, in.Task) {
		return tool.ErrorResult("local implement target does not support task " + in.Task), nil
	}
	if !cfg.SupportsTargets {
		return tool.ErrorResult(noTargetsError), nil
	}
	root, err := tool.Cwd(ctx)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("could not resolve working directory: %v", err)), nil
	}
	rels, err := normalizeTargets(root, in.Targets)
	if err != nil {
		return tool.ErrorResult(err.Error()), nil
	}
	abs := make([]string, len(rels))
	for i, rel := range rels {
		abs[i] = filepath.Join(root, filepath.FromSlash(rel))
		if t.authorize == nil {
			continue
		}
		if err := t.authorize(ctx, abs[i]); err != nil {
			return tool.ErrorResult(fmt.Sprintf("cannot write %s: %v", rel, err)), nil
		}
	}
	args := map[string]any{
		"task":    in.Task,
		"root":    root,
		"targets": rels,
	}
	if cfg.SupportsFiles && len(in.Files) > 0 {
		args["prompt"] = buildPrompt(input{Prompt: in.Prompt, Context: in.Context})
		args["files"] = in.Files
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
	if strings.HasPrefix(text, appliedMarker) {
		written, ok := appliedFiles(text)
		if !ok {
			return tool.TextResult(text + "\n\n[conduit] could not read the written-file list from the router reply; no files were recorded as implementer-written."), nil
		}
		for i, rel := range rels {
			if slices.Contains(written, rel) {
				t.guard.Record(abs[i])
			}
		}
	}
	return tool.TextResult(text), nil
}

// appliedHeaderRe matches the router's first line on success:
// "[local-router] APPLIED: wrote N file(s) (a, b), X added, Y removed."
var appliedHeaderRe = regexp.MustCompile(`^\[local-router\] APPLIED: wrote (\d+) file\(s\) \((.*)\), \d+ added, \d+ removed\.$`)

// appliedFiles returns the files the router reports it wrote, read from the
// header line only. The rest of the reply (diff, model text) is attacker- or
// model-influenced and never consulted.
func appliedFiles(text string) ([]string, bool) {
	header, _, _ := strings.Cut(text, "\n")
	m := appliedHeaderRe.FindStringSubmatch(header)
	if m == nil {
		return nil, false
	}
	files := strings.Split(m[2], ", ")
	if n, err := strconv.Atoi(m[1]); err != nil || n != len(files) {
		return nil, false // the list is ambiguous or does not match the count
	}
	for i, f := range files {
		files[i] = filepath.ToSlash(filepath.Clean(f))
	}
	return files, true
}

// normalizeTargets validates the target list and returns clean slash-separated
// paths relative to root. Absolute paths are accepted only inside root.
func normalizeTargets(root string, targets []string) ([]string, error) {
	if len(targets) == 0 || len(targets) > maxTargets {
		return nil, fmt.Errorf("targets must contain 1 to %d paths", maxTargets)
	}
	out := make([]string, 0, len(targets))
	seen := map[string]bool{}
	for _, raw := range targets {
		p := strings.TrimSpace(raw)
		if p == "" {
			return nil, fmt.Errorf("targets must not contain empty paths")
		}
		// The router's reply lists written files as "(a, b)"; a name that
		// contains those separators could not be told apart from a list.
		if strings.Contains(p, ", ") || strings.ContainsAny(p, "()") {
			return nil, fmt.Errorf("target %q must not contain \", \", \"(\" or \")\"", raw)
		}
		if slices.Contains(strings.Split(filepath.ToSlash(p), "/"), "..") {
			return nil, fmt.Errorf("target %q must not contain \"..\"", raw)
		}
		if filepath.IsAbs(p) {
			rel, err := filepath.Rel(root, p)
			if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
				return nil, fmt.Errorf("target %q is outside the working directory", raw)
			}
			p = rel
		}
		p = filepath.ToSlash(filepath.Clean(p))
		if p == "." {
			return nil, fmt.Errorf("target %q is not a file path", raw)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
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
			cfg.SupportsTrace = toolSupportsTrace(srv, cfg.ImplementTool)
			cfg.SupportsTargets = toolSupportsTargets(srv, cfg.ImplementTool)
			cfg.SupportedTasks = toolTasks(srv, cfg.ImplementTool)
			if cfg.SupportedTasks != nil && !slices.Contains(cfg.SupportedTasks, "implement") {
				return Config{}, false
			}
			return cfg, true
		}
		return Config{}, false
	}

	if srv, ok := bestImplementServer(manager); ok {
		return Config{
			Server:          srv.Name,
			ImplementTool:   defaultImplementTool,
			Model:           srv.Config.Env["LOCAL_LLM_MODEL"],
			SupportsFiles:   toolSupportsFiles(srv, defaultImplementTool),
			SupportsTrace:   toolSupportsTrace(srv, defaultImplementTool),
			SupportsTargets: toolSupportsTargets(srv, defaultImplementTool),
			SupportedTasks:  toolTasks(srv, defaultImplementTool),
		}, true
	}
	return Config{}, false
}

// ResolveFirst resolves the first candidate, in order, whose server is
// connected and exposes the implement tool. Both LocalImplement's registration
// and the implement guard resolve through it, so they cannot disagree about
// whether an implementer is reachable. No candidates resolves like a nil
// provider (any connected server exposing local_implement).
func ResolveFirst(manager *mcp.Manager, candidates []*settings.ActiveProviderSettings) (Config, bool) {
	if len(candidates) == 0 {
		return ResolveConfig(manager, nil)
	}
	for _, p := range candidates {
		if cfg, ok := ResolveConfig(manager, p); ok {
			return cfg, true
		}
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
			if tasks := toolTasks(srv, defaultImplementTool); tasks == nil || slices.Contains(tasks, "implement") {
				candidates = append(candidates, srv)
			}
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

// toolSupportsTargets reports whether a tool's input schema declares "targets"
// and "root", meaning the server applies edits itself.
func toolSupportsTargets(srv *mcp.ConnectedServer, name string) bool {
	td, ok := toolDef(srv, name)
	if !ok {
		return false
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(td.InputSchema, &schema) != nil {
		return false
	}
	_, hasTargets := schema.Properties["targets"]
	_, hasRoot := schema.Properties["root"]
	return hasTargets && hasRoot
}

func toolSupportsTrace(srv *mcp.ConnectedServer, name string) bool {
	td, ok := toolDef(srv, name)
	if !ok {
		return false
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(td.InputSchema, &schema) != nil {
		return false
	}
	var property struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(schema.Properties["include_trace"], &property) == nil && property.Type == "boolean"
}

// toolTasks reads the published task vocabulary. An unconstrained task accepts
// both draft operations; a malformed schema fails closed instead of selecting it.
func toolTasks(srv *mcp.ConnectedServer, name string) []string {
	td, ok := toolDef(srv, name)
	if !ok || len(td.InputSchema) == 0 {
		return nil
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(td.InputSchema, &schema) != nil {
		return []string{}
	}
	raw, ok := schema.Properties["task"]
	if !ok {
		return nil
	}
	var task struct {
		Enum []string `json:"enum"`
	}
	if json.Unmarshal(raw, &task) != nil {
		return []string{}
	}
	if task.Enum == nil {
		return []string{"implement", "fix"}
	}
	return task.Enum
}
