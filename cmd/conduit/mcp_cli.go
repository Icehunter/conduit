package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/secure"
)

// runMCPCmd dispatches `conduit mcp <subcommand>`. Each subcommand has its
// own flag set; flags must precede positionals and -- separates conduit
// flags from a stdio server's command/args (matches Claude's CLI).
func runMCPCmd(args []string) error {
	if len(args) == 0 {
		printMCPUsage(os.Stderr)
		return errors.New("mcp: missing subcommand")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "add":
		return runMCPAdd(rest)
	case "add-json":
		return runMCPAddJSON(rest)
	case "list":
		return runMCPList(rest)
	case "get":
		return runMCPGet(rest)
	case "remove", "rm":
		return runMCPRemove(rest)
	case "auth":
		return runMCPAuth(rest)
	case "help", "-h", "--help":
		printMCPUsage(os.Stdout)
		return nil
	}
	printMCPUsage(os.Stderr)
	return fmt.Errorf("mcp: unknown subcommand %q", sub)
}

func printMCPUsage(w *os.File) {
	fmt.Fprint(w, `Usage: conduit mcp <subcommand> [flags]

Subcommands:
  add <name> <url-or-command> [args...]   Add an MCP server.
  add-json <name> '<json>'                Add a server from a raw JSON object.
  list                                    List configured servers across all scopes.
  get <name>                              Show one server's configuration.
  remove <name>                           Remove a server (any scope) — or use --scope.
  auth <name>                             Authenticate with an OAuth MCP server.
                                          Opens your browser; waits for the callback.

Common flags:
  --scope project|user|local              Where to write. Default: project.
                                          - project: <cwd>/.mcp.json (shared via VCS)
                                          - user:    ~/.conduit/mcp.json (this user, all projects)
                                          - local:   ~/.conduit/conduit.json (this checkout, private)
  --transport http|sse|stdio              Transport. Default: auto-detect from arg.

Add flags:
  --header "Key: Value"                   Repeatable. HTTP/SSE only.
  --env KEY=value                         Repeatable. stdio only.

Examples:
  conduit mcp add atlassian https://mcp.atlassian.com/v1/mcp --transport http
  conduit mcp auth atlassian
  conduit mcp add airtable --env AIRTABLE_API_KEY=xxx -- npx -y airtable-mcp-server
  conduit mcp add github https://api.githubcopilot.com/mcp \
        --transport http --header "Authorization: Bearer $GH_TOKEN" --scope user
`)
}

// stringSliceFlag implements flag.Value for repeatable flags like --header / --env.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringSliceFlag) Set(v string) error { *s = append(*s, v); return nil }

// splitOnDoubleDash partitions argv at the first standalone "--" element.
// Everything before is conduit flags + positionals; everything after is the
// stdio command and its args (which may contain its own flags conduit must
// not interpret). If no "--" is present, command is empty.
func splitOnDoubleDash(args []string) (before, after []string) {
	for i, a := range args {
		if a == "--" {
			return args[:i], args[i+1:]
		}
	}
	return args, nil
}

// reorderFlagsFirst rearranges argv so all -flag / --flag (and their values)
// come before any positional arguments. This lets users put flags after the
// server name, e.g. `conduit mcp add atlassian https://… --transport http`,
// matching Claude's CLI ergonomics. We only treat names known to the supplied
// flagset as flags so we don't accidentally consume positionals that look
// flag-ish.
//
// Recognised forms:
//
//	-flag, --flag                      bool flag
//	-flag=value, --flag=value          inline value
//	-flag value, --flag value          space-separated value (only when the
//	                                   flag's *Var was registered as non-bool)
func reorderFlagsFirst(fs *flag.FlagSet, args []string) []string {
	known := map[string]*flag.Flag{}
	boolish := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		known[f.Name] = f
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			boolish[f.Name] = true
		}
	})

	var flagsOut, positionals []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		// Stop at "--" — leave it (and the rest) as a positional sentinel
		// so the caller can split on it. (We're invoked AFTER splitOnDoubleDash
		// in practice, so this branch is mostly defensive.)
		if a == "--" {
			positionals = append(positionals, args[i:]...)
			break
		}
		// Only treat dashes as a flag if it matches a registered name.
		name, _, hasEq := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if (strings.HasPrefix(a, "-") || strings.HasPrefix(a, "--")) && known[name] != nil {
			flagsOut = append(flagsOut, a)
			if !hasEq && !boolish[name] && i+1 < len(args) {
				flagsOut = append(flagsOut, args[i+1])
				i++
			}
			continue
		}
		positionals = append(positionals, a)
	}
	return append(flagsOut, positionals...)
}

func runMCPAdd(args []string) error {
	before, cmdAndArgs := splitOnDoubleDash(args)

	fs := flag.NewFlagSet("conduit mcp add", flag.ContinueOnError)
	scope := fs.String("scope", mcp.ScopeProject, "config scope: project|user|local")
	transport := fs.String("transport", "", "transport: http|sse|stdio (auto-detect by default)")
	var headers stringSliceFlag
	var envVars stringSliceFlag
	fs.Var(&headers, "header", "HTTP/SSE header, e.g. \"Authorization: Bearer xxx\" (repeatable)")
	fs.Var(&envVars, "env", "stdio env var, KEY=value (repeatable)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: conduit mcp add [flags] <name> <url-or-command> [args...]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderFlagsFirst(fs, before)); err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) < 1 {
		fs.Usage()
		return errors.New("mcp add: server name is required")
	}
	name := pos[0]
	endpoint := ""
	extraArgs := []string{}
	if len(pos) > 1 {
		endpoint = pos[1]
		extraArgs = pos[2:]
	}

	cwd, _ := os.Getwd()

	// Resolve transport: explicit flag wins, then auto-detect from endpoint
	// or the presence of "-- <command>" args.
	t := mcp.NormalizeTransport(*transport)
	if t == "" || (*transport == "" && len(cmdAndArgs) > 0) {
		t = "stdio"
	}
	if *transport == "" && endpoint != "" && len(cmdAndArgs) == 0 {
		t = mcp.DetectTransport(endpoint)
	}

	cfg := mcp.ServerConfig{Type: t}

	switch t {
	case "stdio":
		// Two valid input shapes:
		//   conduit mcp add foo -- npx -y airtable-mcp-server   (preferred)
		//   conduit mcp add foo /path/to/binary [args...]       (legacy)
		if len(cmdAndArgs) > 0 {
			cfg.Command = cmdAndArgs[0]
			cfg.Args = cmdAndArgs[1:]
		} else if endpoint != "" {
			cfg.Command = endpoint
			cfg.Args = extraArgs
		} else {
			return errors.New("mcp add (stdio): missing command — pass it after `--`")
		}
		if len(envVars) > 0 {
			cfg.Env = map[string]string{}
			for _, kv := range envVars {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return fmt.Errorf("mcp add: invalid --env %q (want KEY=value)", kv)
				}
				cfg.Env[k] = v
			}
		}
		// stdio servers leave type empty for backwards-compat with older configs.
		cfg.Type = ""
		if len(headers) > 0 {
			return errors.New("mcp add: --header is only valid for http/sse transports")
		}
	case "http", "sse":
		if endpoint == "" {
			return fmt.Errorf("mcp add (%s): URL is required", t)
		}
		cfg.URL = endpoint
		if len(extraArgs) > 0 || len(cmdAndArgs) > 0 {
			return fmt.Errorf("mcp add (%s): unexpected positional args after URL", t)
		}
		if len(headers) > 0 {
			cfg.Headers = map[string]string{}
			for _, h := range headers {
				k, v, ok := strings.Cut(h, ":")
				if !ok || strings.TrimSpace(k) == "" {
					return fmt.Errorf("mcp add: invalid --header %q (want \"Key: value\")", h)
				}
				cfg.Headers[strings.TrimSpace(k)] = strings.TrimSpace(v)
			}
		}
		if len(envVars) > 0 {
			return errors.New("mcp add: --env is only valid for stdio transport")
		}
	default:
		return fmt.Errorf("mcp add: unknown transport %q", t)
	}

	if err := mcp.AddServer(name, cfg, *scope, cwd); err != nil {
		return err
	}
	dest := writeTargetForScope(*scope, cwd)
	fmt.Printf("Added MCP server %q (scope: %s) → %s\n", name, *scope, dest)
	if t == "http" || t == "sse" {
		fmt.Printf("  Run `conduit mcp auth %s` to authenticate, or `/mcp auth %s` inside conduit.\n", name, name)
	} else {
		fmt.Printf("  Connect by launching conduit.\n")
	}
	return nil
}

func runMCPAddJSON(args []string) error {
	fs := flag.NewFlagSet("conduit mcp add-json", flag.ContinueOnError)
	scope := fs.String("scope", mcp.ScopeProject, "config scope: project|user|local")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: conduit mcp add-json [--scope ...] <name> '<json>'")
	}
	if err := fs.Parse(reorderFlagsFirst(fs, args)); err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) != 2 {
		fs.Usage()
		return errors.New("mcp add-json: requires <name> and a JSON object")
	}
	name, raw := pos[0], pos[1]
	var cfg mcp.ServerConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return fmt.Errorf("mcp add-json: invalid JSON: %w", err)
	}
	cwd, _ := os.Getwd()
	if err := mcp.AddServer(name, cfg, *scope, cwd); err != nil {
		return err
	}
	dest := writeTargetForScope(*scope, cwd)
	fmt.Printf("Added MCP server %q (scope: %s) → %s\n", name, *scope, dest)
	return nil
}

func runMCPList(args []string) error {
	fs := flag.NewFlagSet("conduit mcp list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	rows, err := mcp.ListConfiguredServers(cwd)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("No MCP servers configured.")
		fmt.Println("  Try: conduit mcp add <name> <url-or-command>")
		return nil
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Scope != rows[j].Scope {
			return scopeOrder(rows[i].Scope) < scopeOrder(rows[j].Scope)
		}
		return rows[i].Name < rows[j].Name
	})
	// Compute column widths.
	nameW, scopeW, transportW := len("NAME"), len("SCOPE"), len("TRANSPORT")
	for _, r := range rows {
		if len(r.Name) > nameW {
			nameW = len(r.Name)
		}
		if len(r.Scope) > scopeW {
			scopeW = len(r.Scope)
		}
		t := transportFor(r.Config)
		if len(t) > transportW {
			transportW = len(t)
		}
	}
	fmt.Printf("%-*s  %-*s  %-*s  %s\n", nameW, "NAME", scopeW, "SCOPE", transportW, "TRANSPORT", "ENDPOINT")
	for _, r := range rows {
		t := transportFor(r.Config)
		fmt.Printf("%-*s  %-*s  %-*s  %s\n", nameW, r.Name, scopeW, r.Scope, transportW, t, endpointFor(r.Config))
	}
	return nil
}

func runMCPGet(args []string) error {
	fs := flag.NewFlagSet("conduit mcp get", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) != 1 {
		fs.Usage()
		return errors.New("mcp get: requires a server name")
	}
	name := pos[0]
	cwd, _ := os.Getwd()
	rows, err := mcp.ListConfiguredServers(cwd)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.Name == name {
			fmt.Printf("Name:      %s\n", r.Name)
			fmt.Printf("Scope:     %s\n", r.Scope)
			fmt.Printf("Source:    %s\n", r.Source)
			fmt.Printf("Transport: %s\n", transportFor(r.Config))
			if r.Config.URL != "" {
				fmt.Printf("URL:       %s\n", r.Config.URL)
			}
			if r.Config.Command != "" {
				fmt.Printf("Command:   %s\n", r.Config.Command)
			}
			if len(r.Config.Args) > 0 {
				fmt.Printf("Args:      %s\n", strings.Join(r.Config.Args, " "))
			}
			if len(r.Config.Env) > 0 {
				fmt.Println("Env:")
				for k, v := range r.Config.Env {
					fmt.Printf("  %s=%s\n", k, v)
				}
			}
			if len(r.Config.Headers) > 0 {
				fmt.Println("Headers:")
				for k, v := range r.Config.Headers {
					fmt.Printf("  %s: %s\n", k, v)
				}
			}
			return nil
		}
	}
	return fmt.Errorf("mcp get: server %q not found", name)
}

func runMCPRemove(args []string) error {
	fs := flag.NewFlagSet("conduit mcp remove", flag.ContinueOnError)
	scope := fs.String("scope", "", "limit removal to a single scope: project|user|local")
	if err := fs.Parse(reorderFlagsFirst(fs, args)); err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) != 1 {
		fs.Usage()
		return errors.New("mcp remove: requires a server name")
	}
	name := pos[0]
	cwd, _ := os.Getwd()
	from, err := mcp.RemoveServer(name, *scope, cwd)
	if err != nil {
		return err
	}
	fmt.Printf("Removed MCP server %q (scope: %s)\n", name, from)
	return nil
}

func runMCPAuth(args []string) error {
	fs := flag.NewFlagSet("conduit mcp auth", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: conduit mcp auth <name>")
		fmt.Fprintln(fs.Output(), "  Opens your browser to complete OAuth authentication for the named MCP server.")
		fmt.Fprintln(fs.Output(), "  Tokens are saved to the keychain and used automatically on next connect.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) != 1 {
		fs.Usage()
		return errors.New("mcp auth: requires a server name")
	}
	name := pos[0]
	cwd, _ := os.Getwd()

	// Find the server URL from config files — no running manager needed.
	rows, err := mcp.ListConfiguredServers(cwd)
	if err != nil {
		return fmt.Errorf("mcp auth: list servers: %w", err)
	}
	var serverURL string
	for _, r := range rows {
		if r.Name == name {
			serverURL = r.Config.URL
			break
		}
	}
	if serverURL == "" {
		return fmt.Errorf("mcp auth: server %q not found, or it is not an HTTP/SSE server\n  (run `conduit mcp list` to see configured servers)", name)
	}

	store := secure.NewDefault()

	fmt.Printf("Opening browser for OAuth with %q…\n", name)
	fmt.Printf("  Server: %s\n", serverURL)
	fmt.Println("  Complete the flow in your browser, then return here.")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	tokens, err := mcp.PerformOAuthFlow(ctx, name, serverURL, nil, nil)
	if err != nil {
		return fmt.Errorf("mcp auth: OAuth flow failed: %w", err)
	}
	if err := mcp.SaveServerToken(store, name, tokens); err != nil {
		return fmt.Errorf("mcp auth: save tokens: %w", err)
	}
	fmt.Printf("Authenticated %q ✓  Tokens saved — launch conduit to connect.\n", name)
	return nil
}

// transportFor reports the transport label for a config — empty Type means stdio.
func transportFor(c mcp.ServerConfig) string {
	if c.Type != "" {
		return c.Type
	}
	if c.Command != "" {
		return "stdio"
	}
	if c.URL != "" {
		return "http"
	}
	return "?"
}

// endpointFor returns the user-visible target — URL for HTTP/SSE, command-line for stdio.
func endpointFor(c mcp.ServerConfig) string {
	if c.URL != "" {
		return c.URL
	}
	if c.Command != "" {
		if len(c.Args) == 0 {
			return c.Command
		}
		return c.Command + " " + strings.Join(c.Args, " ")
	}
	return ""
}

func scopeOrder(s string) int {
	switch s {
	case mcp.ScopeProject:
		return 0
	case mcp.ScopeLocal:
		return 1
	case mcp.ScopeUser:
		return 2
	}
	return 3
}

func writeTargetForScope(scope, cwd string) string {
	switch scope {
	case mcp.ScopeProject:
		return cwd + string(os.PathSeparator) + ".mcp.json"
	case mcp.ScopeUser:
		home, _ := os.UserHomeDir()
		dir := os.Getenv("CONDUIT_CONFIG_DIR")
		if dir == "" {
			dir = home + string(os.PathSeparator) + ".conduit"
		}
		return dir + string(os.PathSeparator) + "mcp.json"
	case mcp.ScopeLocal:
		home, _ := os.UserHomeDir()
		dir := os.Getenv("CONDUIT_CONFIG_DIR")
		if dir == "" {
			dir = home + string(os.PathSeparator) + ".conduit"
		}
		return dir + string(os.PathSeparator) + "conduit.json"
	}
	return "?"
}
