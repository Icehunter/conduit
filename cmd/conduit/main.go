// Package main is the conduit entrypoint.
//
// Surface:
//
//	conduit                      Full-screen Bubble Tea TUI.
//	conduit --print "prompt"     One-shot streaming response.
//	conduit version              Print binary version.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"net/http"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/buddy"
	"github.com/icehunter/conduit/internal/claudemd"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/globalconfig"
	"github.com/icehunter/conduit/internal/lsp"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/memdir"
	"github.com/icehunter/conduit/internal/migrations"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/planusage"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/profile"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/sessionmem"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/theme"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/tools/agenttool"
	"github.com/icehunter/conduit/internal/tools/askusertool"
	"github.com/icehunter/conduit/internal/tools/bashtool"
	"github.com/icehunter/conduit/internal/tools/configtool"
	"github.com/icehunter/conduit/internal/tools/fileedittool"
	"github.com/icehunter/conduit/internal/tools/filereadtool"
	"github.com/icehunter/conduit/internal/tools/filewritetool"
	"github.com/icehunter/conduit/internal/tools/globtool"
	"github.com/icehunter/conduit/internal/tools/greptool"
	"github.com/icehunter/conduit/internal/tools/localimplementtool"
	lsptool "github.com/icehunter/conduit/internal/tools/lsp"
	"github.com/icehunter/conduit/internal/tools/mcpauthtool"
	"github.com/icehunter/conduit/internal/tools/mcpresourcetool"
	"github.com/icehunter/conduit/internal/tools/mcptool"
	"github.com/icehunter/conduit/internal/tools/notebookedittool"
	"github.com/icehunter/conduit/internal/tools/planmodetool"
	"github.com/icehunter/conduit/internal/tools/repltool"
	"github.com/icehunter/conduit/internal/tools/skilltool"
	"github.com/icehunter/conduit/internal/tools/sleeptool"
	"github.com/icehunter/conduit/internal/tools/syntheticoutputtool"
	"github.com/icehunter/conduit/internal/tools/tasktool"
	"github.com/icehunter/conduit/internal/tools/todowritetool"
	"github.com/icehunter/conduit/internal/tools/toolsearchtool"
	"github.com/icehunter/conduit/internal/tools/webfetchtool"
	"github.com/icehunter/conduit/internal/tools/websearchtool"
	"github.com/icehunter/conduit/internal/tools/worktreeTool"
	"github.com/icehunter/conduit/internal/tui"
)

// AppVersion is the conduit release version shown to users.
// Populated at build time via -ldflags "-X main.AppVersion=$(VERSION)".
var AppVersion = "1.0.0"

// Version is the CC wire version sent in User-Agent/X-App headers.
var Version = "2.1.126"

// GitCommit and BuildTime are stamped at build time.
var GitCommit = "unknown"
var BuildTime = "unknown"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "conduit:", err)
		os.Exit(1)
	}
}

func run() error {
	var printMode bool
	var continueMode bool
	var resumeID string
	flag.BoolVar(&printMode, "print", false, "non-interactive: send a one-shot prompt and print the response")
	flag.BoolVar(&printMode, "p", false, "alias for --print")
	flag.BoolVar(&continueMode, "continue", false, "resume the most recent conversation for the current directory")
	flag.BoolVar(&continueMode, "c", false, "alias for --continue")
	flag.StringVar(&resumeID, "resume", "", "resume a specific session (session UUID or path to .jsonl file)")
	flag.StringVar(&resumeID, "r", "", "alias for --resume")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: conduit [version] | conduit --print \"prompt\" | conduit [--continue|--resume <id>] (REPL)")
		fmt.Fprintln(os.Stderr, "       Login and logout are managed via /login and /logout inside the REPL.")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if printMode {
		return runPrint(args)
	}
	if len(args) == 0 {
		return runREPL(continueMode, resumeID)
	}

	switch args[0] {
	case "version":
		fmt.Printf("conduit %s (cc-wire/%s, commit %s, built %s)\n", AppVersion, Version, GitCommit, BuildTime)
		return nil
	default:
		flag.Usage()
		return fmt.Errorf("unknown subcommand: %s", args[0])
	}
}

// newAPIClient builds a configured API client using the persisted account
// credentials. Claude.ai uses OAuth bearer auth; Anthropic Console uses the
// minted API key when available.
func newAPIClient(tok auth.PersistedTokens) *api.Client {
	entrypoint := os.Getenv("CLAUDE_CODE_ENTRYPOINT")
	if entrypoint == "" {
		entrypoint = "sdk-cli"
	}
	ua := fmt.Sprintf("claude-cli/%s (external, %s)", Version, entrypoint)
	authToken := tok.AccessToken
	apiKey := ""
	if auth.InferAccountKind(tok) == auth.AccountKindAnthropicConsole && tok.APIKey != "" {
		authToken = ""
		apiKey = tok.APIKey
	}
	betaHeaders := []string{
		"claude-code-20250219",
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"advisor-tool-2026-03-01",
		"advanced-tool-use-2025-11-20",
		"effort-2025-11-24",
		"cache-diagnosis-2026-04-07",
	}
	if apiKey != "" {
		betaHeaders = removeString(betaHeaders, "oauth-2025-04-20")
	}
	cfg := api.Config{
		BaseURL:     auth.ProdConfig.BaseAPIURL,
		AuthToken:   authToken,
		APIKey:      apiKey,
		BetaHeaders: betaHeaders,
		SessionID:   newSessionID(),
		UserAgent:   ua,
		ExtraHeaders: map[string]string{
			"anthropic-dangerous-direct-browser-access": "true",
			"X-Stainless-Retry-Count":                   "0",
			"X-Stainless-Timeout":                       "600",
		},
	}
	// Use a proxy-aware transport when HTTPS_PROXY / HTTP_PROXY env vars are set.
	return api.NewClientWithProxy(cfg)
}

func removeString(values []string, target string) []string {
	out := values[:0]
	for _, value := range values {
		if value != target {
			out = append(out, value)
		}
	}
	return out
}

func fillProfileAccountFallback(p *profile.Info) {
	if p == nil || p.Email != "" {
		return
	}
	active := auth.ActiveEmail()
	if active == "" {
		return
	}
	store, err := auth.ListAccounts()
	if err != nil {
		return
	}
	if entry, ok := store.Accounts[active]; ok {
		if p.DisplayName == "" {
			p.DisplayName = entry.DisplayName
		}
		p.Email = entry.Email
		if p.OrganizationName == "" {
			p.OrganizationName = entry.OrganizationName
		}
		if p.SubscriptionType == "" {
			p.SubscriptionType = entry.SubscriptionType
		}
		return
	}
	for _, entry := range store.Accounts {
		if entry.Email != "" && active == entry.Email {
			p.Email = entry.Email
			return
		}
	}
}

func saveProfileAccountMetadata(p profile.Info, kind string) {
	if p.Email == "" {
		return
	}
	_ = auth.SaveAccountProfile(p.Email, kind, p.DisplayName, p.OrganizationName, p.SubscriptionType)
}

func refreshClaudeAccountProfiles(ctx context.Context) {
	store, err := auth.ListAccounts()
	if err != nil {
		return
	}
	secureStore := secure.NewDefault()
	tc := auth.NewTokenClient(auth.ProdConfig, nil)
	for id, entry := range store.Accounts {
		if entry.Kind != auth.AccountKindClaudeAI || entry.Email == "" {
			continue
		}
		tok, err := auth.EnsureFresh(ctx, secureStore, tc, id, time.Now(), 5*time.Minute)
		if err != nil || tok.AccessToken == "" {
			continue
		}
		p, _ := profile.Fetch(ctx, tok.AccessToken)
		if p.Email == "" {
			p.Email = entry.Email
		}
		saveProfileAccountMetadata(p, auth.AccountKindClaudeAI)
	}
}

// loadAuth loads and refreshes tokens for the active account.
func loadAuth(ctx context.Context) (auth.PersistedTokens, error) {
	store := secure.NewDefault()
	cfg := auth.ProdConfig
	tc := auth.NewTokenClient(cfg, nil)
	return auth.EnsureFresh(ctx, store, tc, auth.ActiveEmail(), time.Now(), 5*time.Minute)
}

// buildSkillEntries converts loaded plugin commands + bundled skills into
// SkillEntry values for the system prompt skill listing.
func buildSkillEntries(ps []*plugins.Plugin) []agent.SkillEntry {
	var entries []agent.SkillEntry
	// Bundled built-in skills first.
	loader := plugins.NewSkillLoader(ps)
	for _, cmd := range loader.BundledCommands() {
		entries = append(entries, agent.SkillEntry{
			Name:        "/" + cmd.QualifiedName,
			Description: cmd.Description,
		})
	}
	// Plugin commands.
	for _, p := range ps {
		for _, cmd := range p.Commands {
			entries = append(entries, agent.SkillEntry{
				Name:        cmd.QualifiedName,
				Description: cmd.Description,
			})
		}
	}
	return entries
}

// registryOpts holds optional callbacks wired after the TUI program starts.
// These are nil in --print mode (no interactive terminal).
type registryOpts struct {
	enterPlan     *planmodetool.EnterPlanMode
	exitPlan      *planmodetool.ExitPlanMode
	askUser       *askusertool.AskUserQuestion
	synthetic     *syntheticoutputtool.SyntheticOutput
	enterWorktree *worktreeTool.EnterWorktree
	exitWorktree  *worktreeTool.ExitWorktree
}

// buildRegistry builds the tool registry, including MCP server tools.
func buildRegistry(client *api.Client, mcpManager *mcp.Manager, lspManager *lsp.Manager, rOpts *registryOpts, implementProvider func() *settings.ActiveProviderSettings) *tool.Registry {
	reg := tool.NewRegistry()
	reg.Register(bashtool.New())
	reg.Register(fileedittool.New())
	reg.Register(filereadtool.New())
	reg.Register(filewritetool.New())
	reg.Register(globtool.New())
	reg.Register(greptool.New())
	reg.Register(notebookedittool.New())
	reg.Register(repltool.New())
	reg.Register(sleeptool.New())
	reg.Register(tasktool.NewCreate())
	reg.Register(tasktool.NewGet())
	reg.Register(tasktool.NewList())
	reg.Register(tasktool.NewUpdate())
	reg.Register(tasktool.NewOutput())
	reg.Register(tasktool.NewStop())
	reg.Register(todowritetool.New())
	reg.Register(toolsearchtool.New(reg))
	reg.Register(webfetchtool.New())
	reg.Register(websearchtool.New(client))
	reg.Register(lsptool.New(lspManager))
	reg.Register(&configtool.ConfigTool{})
	reg.Register(&mcpresourcetool.ListMcpResources{Manager: mcpManager})
	reg.Register(&mcpresourcetool.ReadMcpResource{Manager: mcpManager})
	if _, ok := localimplementtool.ResolveConfig(mcpManager, resolveImplementProvider(implementProvider)); ok {
		reg.Register(localimplementtool.NewDynamic(mcpManager, func() (localimplementtool.Config, bool) {
			return localimplementtool.ResolveConfig(mcpManager, resolveImplementProvider(implementProvider))
		}))
	}
	// Interactive tools — callbacks are wired by the TUI after prog.Start().
	if rOpts != nil && rOpts.enterWorktree != nil {
		reg.Register(rOpts.enterWorktree)
		reg.Register(rOpts.exitWorktree)
	}
	if rOpts != nil {
		reg.Register(rOpts.enterPlan)
		reg.Register(rOpts.exitPlan)
		reg.Register(rOpts.askUser)
		reg.Register(rOpts.synthetic)
	}
	// Register MCP server tools (if any servers are configured).
	if mcpManager != nil {
		mcptool.RegisterAll(reg, mcpManager)
		// For each HTTP/SSE server in the StatusNeedsAuth state, register
		// the per-server pseudo-tool so the model can trigger OAuth
		// itself (mirrors src/tools/McpAuthTool/createMcpAuthTool).
		urls := make(map[string]string)
		for _, srv := range mcpManager.Servers() {
			if srv.Status == mcp.StatusNeedsAuth && srv.Config.URL != "" {
				urls[srv.Name] = srv.Config.URL
			}
		}
		mcpauthtool.RegisterPending(reg, mcpManager, urls)
	}
	return reg
}

func resolveImplementProvider(fn func() *settings.ActiveProviderSettings) *settings.ActiveProviderSettings {
	if fn == nil {
		return nil
	}
	return fn()
}

func claudeRoleModel(cwd, role, fallback string) string {
	latest, err := settings.Load(cwd)
	if err != nil {
		return fallback
	}
	provider, ok := latest.ProviderForRole(role)
	if !ok || provider == nil || provider.Kind == "mcp" || provider.Model == "" {
		return fallback
	}
	return provider.Model
}

// buildMetadata returns the API metadata block.
func buildMetadata() map[string]any {
	deviceID := os.Getenv("CLAUDE_CODE_DEVICE_ID")
	if deviceID == "" {
		deviceID = "00000000000000000000000000000000"
	}
	accountUUID := os.Getenv("CLAUDE_CODE_ACCOUNT_UUID")
	sessionID := newSessionID()
	return agent.BuildMetadata(deviceID, accountUUID, sessionID)
}

// runREPL launches the full-screen Bubble Tea TUI.
// If credentials are absent or invalid the TUI still starts — it shows a
// "not logged in" welcome message and the user can /login from within.
func runREPL(continueMode bool, resumeID string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Warm the TCP+TLS connection to the API in the background to overlap
	// with the rest of startup (mirrors utils/apiPreconnect.ts). Skipped
	// when a proxy is configured because the request would warm the wrong
	// pool — the real request goes through a different transport. Honors
	// ANTHROPIC_BASE_URL for staging/local overrides.
	go func() {
		if os.Getenv("HTTPS_PROXY") != "" || os.Getenv("https_proxy") != "" ||
			os.Getenv("HTTP_PROXY") != "" || os.Getenv("http_proxy") != "" {
			return
		}
		baseURL := os.Getenv("ANTHROPIC_BASE_URL")
		if baseURL == "" {
			baseURL = "https://api.anthropic.com"
		}
		preCtx, cancelPre := context.WithTimeout(ctx, 10*time.Second)
		defer cancelPre()
		req, err := http.NewRequestWithContext(preCtx, http.MethodHead, baseURL, nil)
		if err == nil {
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				_ = resp.Body.Close()
			}
		}
	}()

	// Try auth — failure is not fatal here. The TUI handles the no-auth state.
	tok, authErr := loadAuth(ctx)

	// Fetch profile info in the background; non-fatal if unavailable.
	var prof profile.Info
	if authErr == nil && tok.AccessToken != "" {
		prof, _ = profile.Fetch(ctx, tok.AccessToken)
		fillProfileAccountFallback(&prof)
		saveProfileAccountMetadata(prof, auth.InferAccountKind(tok))
	}
	refreshClaudeAccountProfiles(ctx)

	// Session persistence — create or resume.
	cwd, _ := os.Getwd()
	sessionID := newSessionID()
	var resumedHistory []api.Message

	if resumeID != "" {
		// --resume <uuid> or --resume <path.jsonl>
		var filePath string
		if strings.HasSuffix(strings.ToLower(resumeID), ".jsonl") {
			filePath = resumeID
			// Derive session ID from filename (strip path + .jsonl).
			base := filepath.Base(resumeID)
			sessionID = strings.TrimSuffix(base, ".jsonl")
		} else {
			// Treat as session UUID — look it up in the session list.
			sessions, err := session.List(cwd)
			if err == nil {
				for _, s := range sessions {
					if s.ID == resumeID {
						filePath = s.FilePath
						sessionID = s.ID
						break
					}
				}
			}
		}
		if filePath != "" {
			resumedHistory, _ = session.LoadMessages(filePath)
		}
	} else if continueMode {
		// Load the most recent session for this directory.
		sessions, err := session.List(cwd)
		if err == nil && len(sessions) > 0 {
			most := sessions[0]
			sessionID = most.ID
			resumedHistory, _ = session.LoadMessages(most.FilePath)
		}
	}

	sess, err := session.New(cwd, sessionID)
	if err != nil {
		// Non-fatal — session persistence failure shouldn't block the REPL.
		sess = nil
	}

	// Run one-shot settings migrations before loading. Idempotent: completed
	// IDs are recorded in settings.json so they never re-run.
	migrations.Run(settings.ClaudeDir())

	// Load settings (missing/invalid files are fine — defaults apply).
	s, _ := settings.Load(cwd)
	if s == nil {
		s = &settings.Merged{DefaultMode: "default"}
	}
	usageStatusEnabled := s.UsageStatusEnabled
	if userSettings, err := settings.Load(""); err == nil && userSettings != nil {
		usageStatusEnabled = userSettings.UsageStatusEnabled
	}

	gate := permissions.New(permissions.Mode(s.DefaultMode), s.Allow, s.Deny, s.Ask)

	// Workspace trust check — mirrors CC's hasTrustDialogAccepted logic.
	// runPrint (-p) is non-interactive and skips the dialog; CLAUDE_CODE_SANDBOXED
	// bypasses it too (handled inside IsTrusted).
	needsTrust := false
	if trusted, trustErr := globalconfig.IsTrusted(cwd); trustErr == nil && !trusted {
		needsTrust = true
	}
	go globalconfig.IncrementStartups()

	// additionalDirectories: auto-allow file operations under each directory.
	for _, dir := range s.AdditionalDirs {
		dir = filepath.Clean(dir)
		gate.AllowForSession("Read(" + dir + "/*)")
		gate.AllowForSession("Edit(" + dir + "/*)")
		gate.AllowForSession("Write(" + dir + "/*)")
	}

	// Auto-allow conduit's own per-project storage tree without prompting.
	// The auto-extract memory sub-agent writes to <home>/.claude/projects/
	// <sanitized-cwd>/memory/, the session-memory sub-agent writes to
	// <home>/.claude/projects/<sanitized-cwd>/<sessionID>/session-memory/
	// summary.md, and dream consolidation reads/writes the same memory
	// dir. Without these allows, every conduit-internal write triggered
	// the user permission prompt — annoying and meaningless because the
	// model never picked the path itself; conduit picked it.
	if home, err := os.UserHomeDir(); err == nil {
		conduitDataDir := filepath.Join(home, ".claude", "projects")
		gate.AllowForSession("Read(" + conduitDataDir + "/**)")
		gate.AllowForSession("Edit(" + conduitDataDir + "/**)")
		gate.AllowForSession("Write(" + conduitDataDir + "/**)")
	}

	// Apply theme from settings.json. Style packages init at import time
	// from default Dark, then re-derive via theme.OnChange when we Set here.
	// Unknown theme names are silently ignored (current palette stays Dark)
	// so user preferences for themes we don't have aren't lost — settings
	// file keeps the original value.
	// Load custom themes from settings.json BEFORE applying the active
	// theme name, so a user-defined name wins over built-ins.
	if len(s.Themes) > 0 {
		palettes := make(map[string]theme.Palette, len(s.Themes))
		for name, fields := range s.Themes {
			get := func(key string) string { return fields[key] }
			palettes[name] = theme.Palette{
				Name:            name,
				Primary:         get("primary"),
				Secondary:       get("secondary"),
				Tertiary:        get("tertiary"),
				Accent:          get("accent"),
				Success:         get("success"),
				Danger:          get("danger"),
				Warning:         get("warning"),
				Info:            get("info"),
				Background:      get("background"),
				ModalBg:         get("modalbg"),
				CodeBg:          get("codebg"),
				Border:          get("border"),
				BorderActive:    get("borderactive"),
				ModeAcceptEdits: get("modeacceptedits"),
				ModePlan:        get("modeplan"),
				ModeAuto:        get("modeauto"),
			}
		}
		theme.SetUserThemes(palettes)
	}
	if s.Theme != "" {
		_ = theme.Set(s.Theme)
	}
	if len(s.ThemeOverrides) > 0 {
		theme.SetOverrides(s.ThemeOverrides)
	}

	initialLocalMode := false
	initialLocalServer := ""
	initialLocalDirectTool := ""
	initialLocalImplementTool := ""
	defaultProvider, _ := s.ProviderForRole(settings.RoleDefault)
	implementProvider, _ := s.ProviderForRole(settings.RoleImplement)
	if defaultProvider != nil && defaultProvider.Kind == "mcp" {
		initialLocalMode = true
		initialLocalServer = defaultProvider.Server
		initialLocalDirectTool = defaultProvider.DirectTool
		initialLocalImplementTool = defaultProvider.ImplementTool
	}
	if initialLocalMode && initialLocalServer == "" {
		initialLocalServer = "local-router"
	}
	if initialLocalMode && initialLocalDirectTool == "" {
		initialLocalDirectTool = "local_direct"
	}
	if initialLocalMode && initialLocalImplementTool == "" {
		initialLocalImplementTool = "local_implement"
	}

	// Apply the default provider's Claude/API-shaped model first. MCP-backed
	// providers are restored through conduit's local routing path instead.
	switch {
	case defaultProvider != nil && defaultProvider.Kind != "mcp" && defaultProvider.Model != "":
		internalmodel.SetDefault(defaultProvider.Model)
	case s.Model != "" && !strings.HasPrefix(s.Model, "local:"):
		internalmodel.SetDefault(s.Model)
	}

	// Inject session env vars into the bash tool so every subprocess inherits them.
	if len(s.Env) > 0 {
		bashtool.SessionEnv = s.Env
	}

	// Connect MCP servers in the background; non-fatal if config missing or servers fail.
	mcpManager := mcp.NewManager()
	// Wire the platform keychain so MCP OAuth tokens persist securely.
	mcpManager.SetSecureStore(secure.NewDefault())
	_ = mcpManager.ConnectAll(ctx, cwd)

	// Create the LSP manager; servers are started on demand per file extension.
	lspManager := lsp.NewManager()

	// Load plugins (non-fatal — missing plugins don't block startup).
	loadedPlugins, _ := plugins.LoadAll(cwd)

	// Collect plugin dirs for plugin-provided output styles.
	var pluginDirs []string
	for _, p := range loadedPlugins {
		if p.Dir != "" {
			pluginDirs = append(pluginDirs, p.Dir)
		}
	}

	// Build skill listing for the system prompt.
	skillEntries := buildSkillEntries(loadedPlugins)

	// Load auto-memory: ensure the directory exists and build the full memory
	// system-prompt block (type taxonomy + MEMORY.md content).
	// Mirrors loadMemoryPrompt() in src/memdir/memdir.ts.
	_ = memdir.EnsureDir(cwd)
	mem := memdir.BuildPrompt(cwd)

	// Load CLAUDE.md instruction files (project + user + local).
	claudeMdFiles, _ := claudemd.Load(cwd)
	claudeMdPrompt := claudemd.BuildPrompt(claudeMdFiles)

	c := newAPIClient(tok)

	// Build interactive-tool stubs with nil callbacks; the TUI wires the real
	// callbacks after prog.Start() via the same send-to-channel pattern used
	// by SetAskPermission. Nil callbacks produce graceful error results.
	rOpts := &registryOpts{
		enterPlan: &planmodetool.EnterPlanMode{},
		exitPlan:  &planmodetool.ExitPlanMode{},
		askUser:   &askusertool.AskUserQuestion{},
		synthetic: &syntheticoutputtool.SyntheticOutput{},
		enterWorktree: &worktreeTool.EnterWorktree{GetCwd: func() string {
			d, _ := os.Getwd()
			return d
		}},
		exitWorktree: &worktreeTool.ExitWorktree{GetCwd: func() string {
			d, _ := os.Getwd()
			return d
		}, OriginalCwd: cwd},
	}

	reg := buildRegistry(c, mcpManager, lspManager, rOpts, func() *settings.ActiveProviderSettings {
		latest, err := settings.Load(cwd)
		if err != nil {
			return implementProvider
		}
		provider, ok := latest.ProviderForRole(settings.RoleImplement)
		if !ok {
			return implementProvider
		}
		return provider
	})
	modelName := internalmodel.Resolve()

	// Build MCP server instructions block from connected servers that returned
	// instructions in their initialize response. Injected as an additional
	// system block — mirrors MCP instructions delta in Claude Code.
	var mcpInstructionsPrompt string
	for srvName, instr := range mcpManager.ServerInstructions() {
		mcpInstructionsPrompt += "## " + srvName + "\n" + instr + "\n\n"
	}
	// Buddy companion intro: when a companion is configured, tell the
	// model about it so the model defers to the buddy when the user
	// addresses it by name. Mirrors src/buddy/prompt.ts.
	if intro := buddy.IntroPrompt(); intro != "" {
		mcpInstructionsPrompt += intro + "\n"
	}

	// extractInflight single-flights post-Stop memory extraction so a fast
	// chain of end_turns doesn't queue multiple sub-agent runs. Mirrors CC's
	// `inProgress` guard in extractMemories.ts.
	var (
		extractMu       sync.Mutex
		extractInflight bool
		// Session-memory throttle: fire updateSessionMemory every Nth end_turn
		// so the sub-agent doesn't run on every reply. Mirrors CC's
		// toolCallsBetweenUpdates default.
		sessionMemTurnCount int
		sessionMemMu        sync.Mutex
		sessionMemInflight  bool
	)
	// projectDir for session memory layout — mirrors session.ProjectDir.
	homeDir, _ := os.UserHomeDir()
	projectDir := ""
	if homeDir != "" && sess != nil {
		// session.ProjectDir is what session.New computed already.
		projectDir = sess.ProjectDir
	}

	// Inject prior session memory into the system blocks on resume.
	priorSummary := ""
	if continueMode && sess != nil && projectDir != "" {
		priorSummary, _ = sessionmem.Load(sessionmem.Path(projectDir, sess.ID))
	}

	// Build system blocks; append prior session summary on resume so the
	// new turn picks up where the previous one left off. Append (not
	// prepend) keeps the Max wire fingerprint intact.
	systemBlocks := agent.BuildSystemBlocks(mem, claudeMdPrompt+mcpInstructionsPrompt, skillEntries...)
	if strings.TrimSpace(priorSummary) != "" {
		systemBlocks = append(systemBlocks, api.SystemBlock{
			Type: "text",
			Text: "# Previous session summary (resumed)\n\n" + priorSummary,
		})
	}

	// Seed lastAssistantTime from the resumed session's JSONL so the very
	// first request after a long-idle resume can trigger time-based
	// microcompact without waiting for an additional assistant turn.
	var lastAssistant time.Time
	if continueMode && sess != nil {
		if act, err := session.LoadActivity(sess.FilePath); err == nil {
			lastAssistant = act.LastActivity
		}
	}

	var lp *agent.Loop
	lp = agent.NewLoop(c, reg, agent.LoopConfig{
		Model:             modelName,
		MaxTokens:         internalmodel.MaxTokens,
		System:            systemBlocks,
		MaxTurns:          50,
		Gate:              gate,
		Hooks:             &s.Hooks,
		SessionID:         sessionID,
		Cwd:               cwd,
		AutoCompact:       true,
		MicroCompact:      true,
		LastAssistantTime: lastAssistant,
		ThinkingBudget:    thinkingBudget(),
		NotifyOnComplete:  true,
		BackgroundModel: func() string {
			return claudeRoleModel(cwd, settings.RoleBackground, compact.DefaultModel)
		},
		OnFileAccess: func(op, path string) {
			if sess != nil {
				_ = sess.AppendFileAccess(op, path)
			}
		},
		OnEndTurn: func(history []api.Message) {
			snapshot := make([]api.Message, len(history))
			copy(snapshot, history)

			// Memory extraction (every Stop, single-flighted). Mirrors
			// src/services/extractMemories/extractMemories.ts inProgress guard.
			extractMu.Lock()
			extractWasIdle := !extractInflight
			if extractWasIdle {
				extractInflight = true
			}
			extractMu.Unlock()
			if extractWasIdle {
				go func() {
					defer func() {
						extractMu.Lock()
						extractInflight = false
						extractMu.Unlock()
					}()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					recent := tui.SummarizeMessages(snapshot, 20)
					_ = memdir.RunExtract(ctx, cwd, recent, lp.RunBackgroundAgent)
				}()
			}

			// Session-memory update (throttled to every UpdateEveryNTurns
			// end_turns, single-flighted). Mirrors CC's SessionMemory
			// sub-agent which runs less often than per-Stop extraction.
			if sess == nil || projectDir == "" {
				return
			}
			sessionMemMu.Lock()
			sessionMemTurnCount++
			shouldUpdate := !sessionMemInflight && sessionMemTurnCount%sessionmem.UpdateEveryNTurns == 0
			if shouldUpdate {
				sessionMemInflight = true
			}
			sessionMemMu.Unlock()
			if shouldUpdate {
				go func() {
					defer func() {
						sessionMemMu.Lock()
						sessionMemInflight = false
						sessionMemMu.Unlock()
					}()
					path, err := sessionmem.EnsureFile(projectDir, sess.ID)
					if err != nil {
						return
					}
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
					defer cancel()
					recent := tui.SummarizeMessages(snapshot, 30)
					_ = sessionmem.RunUpdate(ctx, path, recent, lp.RunBackgroundAgent)
				}()
			}
		},
		OnCompact: func(summary string) {
			if sess != nil && summary != "" {
				_ = sess.SetSummary(summary)
			}
		},
	})

	// Register AgentTool and SkillTool now that the loop exists.
	reg.Register(agenttool.New(lp.RunBackgroundAgent))
	skillLoader := plugins.NewSkillLoader(loadedPlugins)
	reg.Register(skilltool.New(skillLoader, lp.RunBackgroundAgent))

	tuiErr := tui.Run(AppVersion, modelName, lp, c, gate, &s.Hooks, tui.RunOptions{
		AuthErr:                   authErr,
		Profile:                   prof,
		Session:                   sess,
		ResumedHistory:            resumedHistory,
		Resumed:                   continueMode && len(resumedHistory) > 0,
		MCPManager:                mcpManager,
		EnterPlan:                 rOpts.enterPlan,
		ExitPlan:                  rOpts.exitPlan,
		AskUser:                   rOpts.askUser,
		InitialOutputStyle:        s.OutputStyle,
		InitialUsageStatusEnabled: usageStatusEnabled,
		InitialLocalMode:          initialLocalMode,
		InitialLocalServer:        initialLocalServer,
		InitialLocalDirectTool:    initialLocalDirectTool,
		InitialLocalImplementTool: initialLocalImplementTool,
		InitialActiveProvider:     defaultProvider,
		InitialProviders:          s.Providers,
		InitialRoles:              s.Roles,
		PluginDirs:                pluginDirs,
		FetchPlanUsage: func(ctx context.Context, provider settings.ActiveProviderSettings) (planusage.Info, error) {
			if provider.Kind != "claude-subscription" || provider.Account == "" {
				return planusage.Info{}, fmt.Errorf("plan usage unsupported for provider %q", provider.Kind)
			}
			store := secure.NewDefault()
			cfg := auth.ProdConfig
			tc := auth.NewTokenClient(cfg, nil)
			tok, err := auth.EnsureFresh(ctx, store, tc, auth.AccountID(auth.AccountKindClaudeAI, provider.Account), time.Now(), 5*time.Minute)
			if err != nil {
				return planusage.Info{}, err
			}
			return planusage.Fetch(ctx, tok.AccessToken)
		},
		LoadAuth: func(ctx context.Context) (auth.PersistedTokens, *profile.Info, error) {
			tok, err := loadAuth(ctx)
			if err != nil {
				return auth.PersistedTokens{}, nil, err
			}
			p, _ := profile.Fetch(ctx, tok.AccessToken)
			fillProfileAccountFallback(&p)
			saveProfileAccountMetadata(p, auth.InferAccountKind(tok))
			refreshClaudeAccountProfiles(ctx)
			return tok, &p, nil
		},
		NewAPIClient: func(tok auth.PersistedTokens) *api.Client {
			return newAPIClient(tok)
		},
		NeedsTrust: needsTrust,
		SetTrusted: func() error {
			return globalconfig.SetTrusted(cwd)
		},
	})

	// Auto-dream: after the session ends, check whether memory consolidation
	// should fire. Mirrors autoDream.ts gate: 24h elapsed + 5 sessions.
	// Runs synchronously (after TUI exits) so the terminal is restored before
	// any sub-agent output. Non-fatal — failure doesn't affect the session.
	if sess != nil {
		sessionDir := sess.ProjectDir
		if memdir.ShouldDream(cwd, sessionDir) {
			dreamCtx, dreamCancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer dreamCancel()
			_ = memdir.RunDream(dreamCtx, cwd, sessionDir, lp.RunBackgroundAgent)
		}
	}

	return tuiErr
}

func runPrint(args []string) error {
	if len(args) == 0 {
		return errors.New("--print requires a prompt argument")
	}
	prompt := strings.Join(args, " ")

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	p, err := loadAuth(ctx)
	if err != nil {
		return fmt.Errorf("authentication: %w (use /login inside the REPL to sign in)", err)
	}

	cwd, _ := os.Getwd()
	loadedPlugins, _ := plugins.LoadAll(cwd)
	skillEntries := buildSkillEntries(loadedPlugins)
	_ = memdir.EnsureDir(cwd)
	mem := memdir.BuildPrompt(cwd)
	claudeMdFiles, _ := claudemd.Load(cwd)
	claudeMdPrompt := claudemd.BuildPrompt(claudeMdFiles)
	c := newAPIClient(p)
	reg := buildRegistry(c, nil, lsp.NewManager(), nil, nil)
	modelName := internalmodel.Resolve()

	lp := agent.NewLoop(c, reg, agent.LoopConfig{
		Model:           modelName,
		MaxTokens:       internalmodel.MaxTokens,
		System:          agent.BuildSystemBlocks(mem, claudeMdPrompt, skillEntries...),
		Metadata:        buildMetadata(),
		MaxTurns:        10,
		BackgroundModel: func() string { return compact.DefaultModel },
	})
	reg.Register(agenttool.New(lp.RunBackgroundAgent))
	reg.Register(skilltool.New(plugins.NewSkillLoader(loadedPlugins), lp.RunBackgroundAgent))

	_, err = lp.Run(ctx, []api.Message{{
		Role:    "user",
		Content: []api.ContentBlock{{Type: "text", Text: prompt}},
	}}, func(ev agent.LoopEvent) {
		if ev.Type == agent.EventText {
			fmt.Print(ev.Text)
		}
	})
	if err != nil {
		fmt.Println()
	}
	return err
}

// thinkingBudget returns the token budget for extended thinking from
// CLAUDE_THINKING_BUDGET env var. 0 means thinking disabled.
func thinkingBudget() int {
	if v := os.Getenv("CLAUDE_THINKING_BUDGET"); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// keep json import used (for ContentBlock marshaling in history tracking)
var _ = json.Marshal
