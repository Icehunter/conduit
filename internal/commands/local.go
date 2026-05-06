package commands

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/settings"
)

const defaultLocalServer = "local-router"
const defaultLocalDirectTool = "local_direct"
const defaultLocalImplementTool = "local_implement"

// LocalCall describes a direct private/local provider MCP invocation.
// It is serialized through Result.Text so the TUI can run it asynchronously.
type LocalCall struct {
	Server    string         `json:"server"`
	Tool      string         `json:"tool"`
	Arguments map[string]any `json:"arguments"`
}

// RegisterLocalCommands adds /local and /local-implement. These are
// conduit-only commands for private LAN/local model routers.
func RegisterLocalCommands(
	r *Registry,
	manager *mcp.Manager,
	provider *settings.ActiveProviderSettings,
	providers map[string]settings.ActiveProviderSettings,
) {
	r.Register(Command{
		Name:        "local",
		Description: "Send a prompt directly to a private/local model (/local [server] <prompt>)",
		Hidden:      true,
		Handler: func(args string) Result {
			toolName := providerTool(provider, "direct")
			return localCommandResult(manager, provider, providers, strings.TrimSpace(args), toolName, map[string]any{
				"mode":                    "direct",
				"include_review_reminder": false,
			})
		},
	})
	r.Register(Command{
		Name:        "local-implement",
		Description: "Ask a private/local model for a scoped unified diff",
		Hidden:      true,
		Handler: func(args string) Result {
			toolName := providerTool(provider, "implement")
			return localCommandResult(manager, provider, providers, strings.TrimSpace(args), toolName, map[string]any{
				"output_format":           "diff",
				"include_review_reminder": false,
			})
		},
	})
	r.Register(Command{
		Name:        "local-mode",
		Description: "Temporarily route normal chat input to a private/local model",
		Hidden:      true,
		Handler: func(args string) Result {
			return localModeResult(manager, provider, providers, strings.TrimSpace(args))
		},
	})
}

// NewLocalDirectCall builds the default direct-call payload used by /local and
// temporary local mode.
func NewLocalDirectCall(server, prompt string) LocalCall {
	return NewLocalDirectCallWithTool(server, defaultLocalDirectTool, prompt)
}

// NewLocalDirectCallWithTool builds a direct-call payload for local mode.
func NewLocalDirectCallWithTool(server, toolName, prompt string) LocalCall {
	if server == "" {
		server = defaultLocalServer
	}
	if toolName == "" {
		toolName = defaultLocalDirectTool
	}
	return LocalCall{
		Server: server,
		Tool:   mcp.ToolNamePrefix(server) + toolName,
		Arguments: map[string]any{
			"prompt":                  prompt,
			"mode":                    "direct",
			"include_review_reminder": false,
		},
	}
}

func localCommandResult(
	manager *mcp.Manager,
	provider *settings.ActiveProviderSettings,
	providers map[string]settings.ActiveProviderSettings,
	args, toolName string,
	extra map[string]any,
) Result {
	if args == "" {
		return Result{Type: "error", Text: "usage: /local [server] <prompt>"}
	}
	if args == "list" || args == "status" {
		return localStatusResult(manager, providers)
	}

	server, prompt := splitLocalTarget(manager, provider, providers, args)
	if strings.TrimSpace(prompt) == "" {
		return Result{Type: "error", Text: fmt.Sprintf("usage: /local %s <prompt>", server)}
	}
	if configured := configuredMCPProviderForServer(providers, server); configured != nil {
		switch {
		case toolName == defaultLocalDirectTool && configured.DirectTool != "":
			toolName = configured.DirectTool
		case toolName == defaultLocalImplementTool && configured.ImplementTool != "":
			toolName = configured.ImplementTool
		}
	}

	call := NewLocalDirectCallWithTool(server, toolName, prompt)
	if toolName != defaultLocalDirectTool {
		call.Tool = mcp.ToolNamePrefix(server) + toolName
		call.Arguments = map[string]any{"prompt": prompt}
	}
	for k, v := range extra {
		call.Arguments[k] = v
	}
	data, err := json.Marshal(call)
	if err != nil {
		return Result{Type: "error", Text: "local: " + err.Error()}
	}
	return Result{Type: "local-call", Text: string(data)}
}

func localModeResult(manager *mcp.Manager, provider *settings.ActiveProviderSettings, providers map[string]settings.ActiveProviderSettings, args string) Result {
	defaultServer := providerServer(provider)
	if args == "" || args == "toggle" {
		return Result{Type: "local-mode", Text: "toggle\t" + defaultServer}
	}
	if args == "off" || args == "false" || args == "disable" {
		return Result{Type: "local-mode", Text: "off\t"}
	}
	if args == "on" || args == "true" || args == "enable" {
		return Result{Type: "local-mode", Text: "on\t" + defaultServer}
	}
	server, rest := splitLocalTarget(manager, provider, providers, args)
	if strings.TrimSpace(rest) != "" {
		return Result{Type: "error", Text: "usage: /local-mode [on|off|server]"}
	}
	return Result{Type: "local-mode", Text: "on\t" + server}
}

func splitLocalTarget(manager *mcp.Manager, provider *settings.ActiveProviderSettings, providers map[string]settings.ActiveProviderSettings, args string) (server, prompt string) {
	server = providerServer(provider)
	fields := strings.Fields(args)
	if len(fields) == 0 {
		return server, ""
	}
	first := fields[0]
	if manager != nil {
		for _, srv := range manager.Servers() {
			if srv.Name == first {
				return first, strings.TrimSpace(strings.TrimPrefix(args, first))
			}
		}
	}
	if configuredMCPProviderForServer(providers, first) != nil {
		return first, strings.TrimSpace(strings.TrimPrefix(args, first))
	}
	if first == server || first == defaultLocalServer {
		return first, strings.TrimSpace(strings.TrimPrefix(args, first))
	}
	if len(fields) < 2 {
		return server, args
	}
	return server, args
}

func providerServer(provider *settings.ActiveProviderSettings) string {
	if provider != nil && provider.Kind == "mcp" && provider.Server != "" {
		return provider.Server
	}
	return defaultLocalServer
}

func providerTool(provider *settings.ActiveProviderSettings, mode string) string {
	if provider != nil && provider.Kind == "mcp" {
		switch mode {
		case "implement":
			if provider.ImplementTool != "" {
				return provider.ImplementTool
			}
		default:
			if provider.DirectTool != "" {
				return provider.DirectTool
			}
		}
	}
	if mode == "implement" {
		return defaultLocalImplementTool
	}
	return defaultLocalDirectTool
}

func localStatusResult(manager *mcp.Manager, providers map[string]settings.ActiveProviderSettings) Result {
	var sb strings.Builder
	sb.WriteString("Local providers:\n\n")
	targets := localTargets(manager, providers)
	for _, target := range targets {
		fmt.Fprintf(&sb, "  %s  %s", target.Server, target.Status)
		if target.Model != "" && target.Model != target.Server {
			fmt.Fprintf(&sb, "  %s", target.Model)
		}
		if target.ToolCount > 0 {
			fmt.Fprintf(&sb, "  (%d tools)", target.ToolCount)
		}
		if target.Error != "" {
			fmt.Fprintf(&sb, "  %s", target.Error)
		}
		sb.WriteByte('\n')
	}
	if len(targets) == 0 {
		if manager == nil {
			sb.WriteString("  MCP manager unavailable\n")
		}
		sb.WriteString("  (none found)\n")
	}
	return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
}

func localModelPickerItems(manager *mcp.Manager, providers map[string]settings.ActiveProviderSettings) []PickerOption {
	targets := localTargets(manager, providers)
	items := make([]PickerOption, 0, len(targets)*2)
	for _, target := range targets {
		model := target.Model
		if model == "" {
			model = target.Server
		}
		items = append(items,
			PickerOption{Label: "MCP " + target.Server, Section: true},
			PickerOption{Value: "local:" + target.Server, Label: model},
		)
	}
	if len(items) == 0 {
		return []PickerOption{
			{Label: "MCP " + defaultLocalServer, Section: true},
			{Value: "local:" + defaultLocalServer, Label: "qwen3-coder"},
		}
	}
	return items
}

func isLocalRouterServer(srv *mcp.ConnectedServer) bool {
	if srv == nil {
		return false
	}
	for _, tool := range srv.Tools {
		if tool.Name == "local_direct" || tool.Name == "local_implement" {
			return true
		}
	}
	return strings.Contains(srv.Name, "local")
}

type localTarget struct {
	Server    string
	Model     string
	Status    string
	ToolCount int
	Error     string
}

func localTargets(manager *mcp.Manager, providers map[string]settings.ActiveProviderSettings) []localTarget {
	byServer := map[string]localTarget{}
	for _, provider := range sortedMCPProviders(providers) {
		server := provider.Server
		if server == "" {
			continue
		}
		model := provider.Model
		if model == "" {
			model = server
		}
		byServer[server] = localTarget{
			Server: server,
			Model:  model,
			Status: "configured",
		}
	}
	if manager != nil {
		for _, srv := range manager.Servers() {
			if !isLocalRouterServer(srv) {
				continue
			}
			model := localModelName(manager, srv.Name)
			if model == "" {
				model = srv.Name
			}
			status := string(srv.Status)
			if status == "" {
				status = "unknown"
			}
			byServer[srv.Name] = localTarget{
				Server:    srv.Name,
				Model:     model,
				Status:    status,
				ToolCount: len(srv.Tools),
				Error:     srv.Error,
			}
		}
	}
	names := make([]string, 0, len(byServer))
	for name := range byServer {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]localTarget, 0, len(names))
	for _, name := range names {
		out = append(out, byServer[name])
	}
	return out
}

func sortedMCPProviders(providers map[string]settings.ActiveProviderSettings) []settings.ActiveProviderSettings {
	if len(providers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(providers))
	for key, provider := range providers {
		if provider.Kind == "mcp" && provider.Server != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	out := make([]settings.ActiveProviderSettings, 0, len(keys))
	for _, key := range keys {
		out = append(out, providers[key])
	}
	return out
}

func configuredMCPProviderForServer(providers map[string]settings.ActiveProviderSettings, server string) *settings.ActiveProviderSettings {
	if server == "" {
		return nil
	}
	for _, provider := range sortedMCPProviders(providers) {
		if provider.Server == server {
			cp := provider
			return &cp
		}
	}
	return nil
}

func localModelName(manager *mcp.Manager, server string) string {
	if manager != nil {
		for _, srv := range manager.Servers() {
			if srv == nil || srv.Name != server {
				continue
			}
			if model := srv.Config.Env["LOCAL_LLM_MODEL"]; model != "" {
				return model
			}
			return srv.Name
		}
	}
	if server == defaultLocalServer {
		return "qwen3-coder"
	}
	return server
}
