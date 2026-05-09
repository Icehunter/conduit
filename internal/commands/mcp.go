package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/settings"
)

// RegisterMCPCommand registers /mcp — interactive MCP server browser.
func RegisterMCPCommand(r *Registry, manager *mcp.Manager) {
	r.Register(Command{
		Name:        "mcp",
		Description: "Manage MCP server connections",
		Handler: func(args string) Result {
			if manager == nil {
				return Result{Type: "mcp-dialog", Text: ""}
			}

			servers := manager.Servers()

			trimmed := strings.TrimSpace(args)
			head, rest := splitFirstWord(trimmed)
			lowerHead := strings.ToLower(head)

			// /mcp auth [name] — manually trigger OAuth flow for a needs-auth server.
			if lowerHead == "auth" {
				name := strings.TrimSpace(rest)
				if name == "" {
					var pending []string
					for _, s := range servers {
						if s.Status == mcp.StatusNeedsAuth {
							pending = append(pending, s.Name)
						}
					}
					if len(pending) == 0 {
						return Result{Type: "text", Text: "No MCP servers are awaiting authentication."}
					}
					return Result{Type: "text", Text: "Servers awaiting auth:\n  " + strings.Join(pending, "\n  ") + "\n\nUsage: /mcp auth <name>"}
				}
				return runMcpAuth(manager, name, servers)
			}

			// /mcp tools — plain text list of all tools across all servers.
			if lowerHead == "tools" {
				return mcpToolsResult(servers)
			}

			// /mcp add / add-json / list / get / remove — CRUD slash equivalents.
			switch lowerHead {
			case "add":
				return mcpAddSlash(manager, rest)
			case "add-json":
				return mcpAddJSONSlash(manager, rest)
			case "list":
				return mcpListSlash()
			case "get":
				return mcpGetSlash(rest)
			case "remove", "rm":
				return mcpRemoveSlash(manager, rest)
			}

			// Default: open the interactive browser panel.
			return mcpListPanel(servers)
		},
	})
}

// splitFirstWord returns (firstWord, rest) on whitespace; rest is trimmed.
func splitFirstWord(s string) (string, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	idx := strings.IndexAny(s, " \t")
	if idx < 0 {
		return s, ""
	}
	return s[:idx], strings.TrimSpace(s[idx+1:])
}

// mcpToolsResult formats /mcp tools as plain text.
func mcpToolsResult(servers []*mcp.ConnectedServer) Result {
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	var sb strings.Builder
	sb.WriteString("MCP Tools:\n\n")
	for _, srv := range servers {
		if srv.Status != mcp.StatusConnected {
			continue
		}
		fmt.Fprintf(&sb, "  %s (%d tools):\n", srv.Name, len(srv.Tools))
		for _, t := range srv.Tools {
			desc := t.Description
			if len([]rune(desc)) > 60 {
				desc = string([]rune(desc)[:59]) + "…"
			}
			fmt.Fprintf(&sb, "    • %s%s — %s\n",
				mcp.ToolNamePrefix(srv.Name), t.Name, desc)
		}
	}
	return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
}

// mcpListPanel encodes the manager's live server list for the interactive panel.
func mcpListPanel(servers []*mcp.ConnectedServer) Result {
	sort.Slice(servers, func(i, j int) bool {
		si, sj := servers[i].Config.Scope, servers[j].Config.Scope
		if si != sj {
			if si == "plugin" {
				return false
			}
			if sj == "plugin" {
				return true
			}
		}
		return servers[i].Name < servers[j].Name
	})
	lines := make([]string, 0, len(servers))
	for _, srv := range servers {
		scope := scopeLabel(srv.Config.Scope)
		source := srv.Config.Source
		status := string(srv.Status)
		if srv.Disabled {
			status = "disabled"
		}
		cmd := srv.Config.Command
		if cmd == "" {
			cmd = srv.Config.URL
		}
		args := strings.Join(srv.Config.Args, " ")
		toolCount := fmt.Sprintf("%d", len(srv.Tools))
		errStr := srv.Error
		disabled := "0"
		if srv.Disabled {
			disabled = "1"
		}
		line := strings.Join([]string{srv.Name, scope, source, status, cmd, args, toolCount, errStr, disabled}, "\t")
		lines = append(lines, line)
	}
	return Result{Type: "mcp-dialog", Text: strings.Join(lines, "\n")}
}

// runMcpAuth returns a "mcp-auth" result that the TUI handles asynchronously.
// The heavy work (browser open, callback wait, token exchange) happens in a
// tea.Cmd goroutine so it never blocks the Bubble Tea event loop.
func runMcpAuth(manager *mcp.Manager, name string, servers []*mcp.ConnectedServer) Result {
	var target *mcp.ConnectedServer
	for _, s := range servers {
		if s.Name == name {
			target = s
			break
		}
	}
	if target == nil {
		return Result{Type: "error", Text: fmt.Sprintf("MCP server %q not found.", name)}
	}
	if target.Config.URL == "" {
		return Result{Type: "error", Text: fmt.Sprintf("MCP server %q is not HTTP/SSE — OAuth only applies to remote transports.", name)}
	}
	if manager.SecureStore() == nil {
		return Result{Type: "error", Text: "MCP OAuth: no secure storage configured (this is a wiring bug — please file an issue)."}
	}
	// Text = name, Model = URL — the TUI uses both to drive the async flow.
	return Result{Type: "mcp-auth", Text: name, Model: target.Config.URL}
}

// RegisterMCPApproveCommand registers /mcp-approve, the back-end for the
// startup approval picker. Args: "<server-name> <yes|yes_all|no>". Persists
// the choice to user settings then triggers a reconnect for "yes"/"yes_all".
func RegisterMCPApproveCommand(r *Registry, manager *mcp.Manager, cwd string) {
	r.Register(Command{
		Name:        "mcp-approve",
		Description: "Internal: approve or deny a project-scope MCP server",
		Handler: func(args string) Result {
			parts := strings.SplitN(strings.TrimSpace(args), " ", 2)
			if len(parts) != 2 {
				return Result{Type: "error", Text: "Usage: /mcp-approve <name> <yes|yes_all|no>"}
			}
			name, choice := parts[0], parts[1]
			if err := settings.ApproveMcpjsonServer(name, choice, cwd); err != nil {
				return Result{Type: "error", Text: fmt.Sprintf("mcp-approve: %v", err)}
			}
			if (choice == "yes" || choice == "yes_all") && manager != nil {
				// trusted=true: the user just approved this server, so trust is granted.
				_ = manager.Reconnect(context.Background(), name, cwd, true)
			}
			verb := "Approved"
			switch choice {
			case "no":
				verb = "Denied"
			case "yes_all":
				verb = "Approved (all project servers)"
			}
			return Result{Type: "flash", Text: fmt.Sprintf("%s MCP server: %s", verb, name)}
		},
	})
}

func scopeLabel(scope string) string {
	switch scope {
	case "user", "local":
		return "User"
	case "project":
		return "Project"
	case "plugin":
		return "Built-in"
	default:
		return "User"
	}
}

// ---------------------------------------------------------------------------
// /mcp add | add-json | list | get | remove — slash-command CRUD parity
// ---------------------------------------------------------------------------

// mcpAddSlash implements `/mcp add [flags] <name> <url-or-command> [args...]`.
// Flag parsing here is a shell-style scan rather than the Go flag package
// because slash-command args arrive as a single string (no argv splitting),
// and we want to support both `--key=value` and `--key value`.
func mcpAddSlash(manager *mcp.Manager, args string) Result {
	tokens, err := splitShellArgs(args)
	if err != nil {
		return Result{Type: "error", Text: "/mcp add: " + err.Error()}
	}
	parsed, err := parseAddTokens(tokens)
	if err != nil {
		return Result{Type: "error", Text: "/mcp add: " + err.Error()}
	}
	cwd, _ := os.Getwd()
	if err := mcp.AddServer(parsed.name, parsed.cfg, parsed.scope, cwd); err != nil {
		return Result{Type: "error", Text: "/mcp add: " + err.Error()}
	}
	// Connect/reconnect live so the user doesn't need to restart.
	go func() {
		_ = manager.Reconnect(context.Background(), parsed.name, cwd, true)
	}()
	hint := ""
	if parsed.cfg.URL != "" {
		hint = fmt.Sprintf(" If it requires OAuth, run /mcp auth %s.", parsed.name)
	}
	return Result{Type: "flash", Text: fmt.Sprintf("Added MCP server %q (scope: %s).%s", parsed.name, parsed.scope, hint)}
}

// mcpAddJSONSlash implements `/mcp add-json [--scope ...] <name> <json>`.
func mcpAddJSONSlash(manager *mcp.Manager, args string) Result {
	tokens, err := splitShellArgs(args)
	if err != nil {
		return Result{Type: "error", Text: "/mcp add-json: " + err.Error()}
	}
	scope := mcp.ScopeProject
	var positional []string
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if t == "--scope" && i+1 < len(tokens) {
			scope = tokens[i+1]
			i++
			continue
		}
		if strings.HasPrefix(t, "--scope=") {
			scope = strings.TrimPrefix(t, "--scope=")
			continue
		}
		positional = append(positional, t)
	}
	if len(positional) != 2 {
		return Result{Type: "error", Text: "/mcp add-json: requires <name> and a JSON object"}
	}
	name := positional[0]
	var cfg mcp.ServerConfig
	if err := json.Unmarshal([]byte(positional[1]), &cfg); err != nil {
		return Result{Type: "error", Text: "/mcp add-json: invalid JSON: " + err.Error()}
	}
	cwd, _ := os.Getwd()
	if err := mcp.AddServer(name, cfg, scope, cwd); err != nil {
		return Result{Type: "error", Text: "/mcp add-json: " + err.Error()}
	}
	go func() {
		_ = manager.Reconnect(context.Background(), name, cwd, true)
	}()
	return Result{Type: "flash", Text: fmt.Sprintf("Added MCP server %q (scope: %s).", name, scope)}
}

// mcpListSlash returns a plain-text table of every configured server.
// (The interactive panel is reachable via bare `/mcp` without subcommand.)
func mcpListSlash() Result {
	cwd, _ := os.Getwd()
	rows, err := mcp.ListConfiguredServers(cwd)
	if err != nil {
		return Result{Type: "error", Text: "/mcp list: " + err.Error()}
	}
	if len(rows) == 0 {
		return Result{Type: "text", Text: "No MCP servers configured. Use /mcp add to add one."}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Scope != rows[j].Scope {
			return scopeOrderSlash(rows[i].Scope) < scopeOrderSlash(rows[j].Scope)
		}
		return rows[i].Name < rows[j].Name
	})
	var sb strings.Builder
	sb.WriteString("MCP servers:\n\n")
	for _, r := range rows {
		endpoint := r.Config.URL
		if endpoint == "" {
			endpoint = r.Config.Command
			if len(r.Config.Args) > 0 {
				endpoint += " " + strings.Join(r.Config.Args, " ")
			}
		}
		fmt.Fprintf(&sb, "  %-20s  %-8s  %s\n", r.Name, r.Scope, endpoint)
	}
	return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
}

// mcpGetSlash returns one server's full config as plain text.
func mcpGetSlash(args string) Result {
	name := strings.TrimSpace(args)
	if name == "" {
		return Result{Type: "error", Text: "/mcp get: requires a server name"}
	}
	cwd, _ := os.Getwd()
	rows, err := mcp.ListConfiguredServers(cwd)
	if err != nil {
		return Result{Type: "error", Text: "/mcp get: " + err.Error()}
	}
	for _, r := range rows {
		if r.Name == name {
			var sb strings.Builder
			fmt.Fprintf(&sb, "Name:      %s\n", r.Name)
			fmt.Fprintf(&sb, "Scope:     %s\n", r.Scope)
			fmt.Fprintf(&sb, "Source:    %s\n", r.Source)
			t := r.Config.Type
			if t == "" {
				if r.Config.Command != "" {
					t = "stdio"
				} else if r.Config.URL != "" {
					t = "http"
				}
			}
			fmt.Fprintf(&sb, "Transport: %s\n", t)
			if r.Config.URL != "" {
				fmt.Fprintf(&sb, "URL:       %s\n", r.Config.URL)
			}
			if r.Config.Command != "" {
				fmt.Fprintf(&sb, "Command:   %s\n", r.Config.Command)
			}
			if len(r.Config.Args) > 0 {
				fmt.Fprintf(&sb, "Args:      %s\n", strings.Join(r.Config.Args, " "))
			}
			if len(r.Config.Env) > 0 {
				sb.WriteString("Env:\n")
				for k, v := range r.Config.Env {
					fmt.Fprintf(&sb, "  %s=%s\n", k, v)
				}
			}
			if len(r.Config.Headers) > 0 {
				sb.WriteString("Headers:\n")
				for k, v := range r.Config.Headers {
					fmt.Fprintf(&sb, "  %s: %s\n", k, v)
				}
			}
			return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
		}
	}
	return Result{Type: "error", Text: fmt.Sprintf("/mcp get: server %q not found", name)}
}

// mcpRemoveSlash implements `/mcp remove [--scope ...] <name>`.
func mcpRemoveSlash(manager *mcp.Manager, args string) Result {
	tokens, err := splitShellArgs(args)
	if err != nil {
		return Result{Type: "error", Text: "/mcp remove: " + err.Error()}
	}
	scope := ""
	var positional []string
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if t == "--scope" && i+1 < len(tokens) {
			scope = tokens[i+1]
			i++
			continue
		}
		if strings.HasPrefix(t, "--scope=") {
			scope = strings.TrimPrefix(t, "--scope=")
			continue
		}
		positional = append(positional, t)
	}
	if len(positional) != 1 {
		return Result{Type: "error", Text: "/mcp remove: requires a server name"}
	}
	name := positional[0]
	cwd, _ := os.Getwd()
	from, err := mcp.RemoveServer(name, scope, cwd)
	if err != nil {
		return Result{Type: "error", Text: "/mcp remove: " + err.Error()}
	}
	if manager != nil {
		go manager.DisconnectServer(name)
	}
	return Result{Type: "flash", Text: fmt.Sprintf("Removed MCP server %q (scope: %s).", name, from)}
}

// addParseResult holds the parsed output of `/mcp add` token scanning.
type addParseResult struct {
	name  string
	scope string
	cfg   mcp.ServerConfig
}

// parseAddTokens scans tokens for flags + positionals + `--` separator and
// returns a fully-formed ServerConfig. Mirrors the CLI semantics exactly.
func parseAddTokens(tokens []string) (addParseResult, error) {
	var out addParseResult
	out.scope = mcp.ScopeProject

	// Split on standalone "--" first so a stdio command's own flags can't
	// be mistaken for our flags.
	var before []string
	var cmdAndArgs []string
	doubleDash := -1
	for i, t := range tokens {
		if t == "--" {
			doubleDash = i
			break
		}
	}
	if doubleDash >= 0 {
		before = tokens[:doubleDash]
		cmdAndArgs = tokens[doubleDash+1:]
	} else {
		before = tokens
	}

	transport := ""
	var headers []string
	var envVars []string
	var positional []string

	consumeValue := func(i *int, current string) (string, error) {
		if eq := strings.IndexByte(current, '='); eq >= 0 {
			return current[eq+1:], nil
		}
		if *i+1 >= len(before) {
			return "", fmt.Errorf("flag %q requires a value", current)
		}
		*i++
		return before[*i], nil
	}

	for i := 0; i < len(before); i++ {
		t := before[i]
		switch {
		case t == "--scope" || strings.HasPrefix(t, "--scope="):
			v, err := consumeValue(&i, t)
			if err != nil {
				return out, err
			}
			out.scope = v
		case t == "--transport" || strings.HasPrefix(t, "--transport="):
			v, err := consumeValue(&i, t)
			if err != nil {
				return out, err
			}
			transport = v
		case t == "--header" || strings.HasPrefix(t, "--header="):
			v, err := consumeValue(&i, t)
			if err != nil {
				return out, err
			}
			headers = append(headers, v)
		case t == "--env" || strings.HasPrefix(t, "--env="):
			v, err := consumeValue(&i, t)
			if err != nil {
				return out, err
			}
			envVars = append(envVars, v)
		default:
			positional = append(positional, t)
		}
	}

	if len(positional) < 1 {
		return out, fmt.Errorf("server name is required")
	}
	out.name = positional[0]
	endpoint := ""
	extraArgs := []string{}
	if len(positional) > 1 {
		endpoint = positional[1]
		extraArgs = positional[2:]
	}

	t := mcp.NormalizeTransport(transport)
	if transport == "" && len(cmdAndArgs) > 0 {
		t = "stdio"
	}
	if transport == "" && endpoint != "" && len(cmdAndArgs) == 0 {
		t = mcp.DetectTransport(endpoint)
	}

	switch t {
	case "stdio":
		if len(cmdAndArgs) > 0 {
			out.cfg.Command = cmdAndArgs[0]
			out.cfg.Args = cmdAndArgs[1:]
		} else if endpoint != "" {
			out.cfg.Command = endpoint
			out.cfg.Args = extraArgs
		} else {
			return out, fmt.Errorf("stdio: missing command (pass it after `--`)")
		}
		if len(envVars) > 0 {
			out.cfg.Env = map[string]string{}
			for _, kv := range envVars {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return out, fmt.Errorf("invalid --env %q (want KEY=value)", kv)
				}
				out.cfg.Env[k] = v
			}
		}
		if len(headers) > 0 {
			return out, fmt.Errorf("--header is only valid for http/sse")
		}
		// stdio leaves Type empty for backwards-compat.
		out.cfg.Type = ""
	case "http", "sse":
		if endpoint == "" {
			return out, fmt.Errorf("%s: URL is required", t)
		}
		if len(extraArgs) > 0 || len(cmdAndArgs) > 0 {
			return out, fmt.Errorf("%s: unexpected positional args after URL", t)
		}
		out.cfg.Type = t
		out.cfg.URL = endpoint
		if len(headers) > 0 {
			out.cfg.Headers = map[string]string{}
			for _, h := range headers {
				k, v, ok := strings.Cut(h, ":")
				if !ok || strings.TrimSpace(k) == "" {
					return out, fmt.Errorf("invalid --header %q (want \"Key: value\")", h)
				}
				out.cfg.Headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		if len(envVars) > 0 {
			return out, fmt.Errorf("--env is only valid for stdio")
		}
	default:
		return out, fmt.Errorf("unknown transport %q", t)
	}

	return out, nil
}

// splitShellArgs tokenises a single string into argv-style tokens, honouring
// single and double quotes (with no escape semantics — same approximation
// shell-words.go uses elsewhere in conduit).
func splitShellArgs(s string) ([]string, error) {
	var (
		tokens   []string
		current  strings.Builder
		inSingle bool
		inDouble bool
		hasToken bool
	)
	flush := func() {
		if hasToken {
			tokens = append(tokens, current.String())
			current.Reset()
			hasToken = false
		}
	}
	for _, r := range s {
		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
				continue
			}
			current.WriteRune(r)
			hasToken = true
		case inDouble:
			if r == '"' {
				inDouble = false
				continue
			}
			current.WriteRune(r)
			hasToken = true
		case r == '\'':
			inSingle = true
			hasToken = true
		case r == '"':
			inDouble = true
			hasToken = true
		case r == ' ' || r == '\t':
			flush()
		default:
			current.WriteRune(r)
			hasToken = true
		}
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("unterminated quoted string")
	}
	flush()
	return tokens, nil
}

func scopeOrderSlash(scope string) int {
	switch scope {
	case mcp.ScopeProject:
		return 0
	case mcp.ScopeLocal:
		return 1
	case mcp.ScopeUser:
		return 2
	}
	return 3
}
