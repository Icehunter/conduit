package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/app"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/buddy"
	"github.com/icehunter/conduit/internal/claudemd"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/globalconfig"
	"github.com/icehunter/conduit/internal/hooks"
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
	"github.com/icehunter/conduit/internal/tools/agenttool"
	"github.com/icehunter/conduit/internal/tools/askusertool"
	"github.com/icehunter/conduit/internal/tools/planmodetool"
	"github.com/icehunter/conduit/internal/tools/skilltool"
	"github.com/icehunter/conduit/internal/tools/syntheticoutputtool"
	"github.com/icehunter/conduit/internal/tools/worktreetool"
	"github.com/icehunter/conduit/internal/tui"
)

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
	tok, authErr := app.LoadAuth(ctx)

	// Fetch profile info in the background; non-fatal if unavailable.
	var prof profile.Info
	if authErr == nil && tok.AccessToken != "" {
		prof, _ = profile.Fetch(ctx, tok.AccessToken)
		app.FillProfileAccountFallback(&prof)
		app.SaveProfileAccountMetadata(prof, auth.InferAccountKind(tok))
	}
	app.RefreshClaudeAccountProfiles(ctx)

	// Session persistence — create or resume.
	cwd, _ := os.Getwd()
	sessionID := app.NewSessionID()
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

	// SessionEnv is stored in RegistryOpts and passed to BashTool.New() rather
	// than a package-level global, so initialization order doesn't matter.
	var sessionEnv map[string]string
	if len(s.Env) > 0 {
		sessionEnv = s.Env
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
	skillEntries := app.BuildSkillEntries(loadedPlugins)

	// Load auto-memory: ensure the directory exists and build the full memory
	// system-prompt block (type taxonomy + MEMORY.md content).
	// Mirrors loadMemoryPrompt() in src/memdir/memdir.ts.
	_ = memdir.EnsureDir(cwd)
	mem := memdir.BuildPrompt(cwd)

	// Load CLAUDE.md instruction files (project + user + local).
	claudeMdFiles, _ := claudemd.Load(cwd)
	claudeMdPrompt := claudemd.BuildPrompt(claudeMdFiles)

	c := app.NewAPIClient(tok, Version)

	// Build interactive-tool stubs with nil callbacks; the TUI wires the real
	// callbacks after prog.Start() via the same send-to-channel pattern used
	// by SetAskPermission. Nil callbacks produce graceful error results.
	rOpts := &app.RegistryOpts{
		EnterPlan: &planmodetool.EnterPlanMode{},
		ExitPlan:  &planmodetool.ExitPlanMode{},
		AskUser:   &askusertool.AskUserQuestion{},
		Synthetic: &syntheticoutputtool.SyntheticOutput{},
		EnterWorktree: &worktreetool.EnterWorktree{GetCwd: func() string {
			d, _ := os.Getwd()
			return d
		}},
		ExitWorktree: &worktreetool.ExitWorktree{GetCwd: func() string {
			d, _ := os.Getwd()
			return d
		}, OriginalCwd: cwd},
		SessionEnv: sessionEnv,
	}

	reg := app.BuildRegistry(c, mcpManager, lspManager, rOpts, func() *settings.ActiveProviderSettings {
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
	var mcpInstructionsBuf strings.Builder
	for srvName, instr := range mcpManager.ServerInstructions() {
		mcpInstructionsBuf.WriteString("## ")
		mcpInstructionsBuf.WriteString(srvName)
		mcpInstructionsBuf.WriteString("\n")
		mcpInstructionsBuf.WriteString(instr)
		mcpInstructionsBuf.WriteString("\n\n")
	}
	// Buddy companion intro: when a companion is configured, tell the
	// model about it so the model defers to the buddy when the user
	// addresses it by name. Mirrors src/buddy/prompt.ts.
	if intro := buddy.IntroPrompt(); intro != "" {
		mcpInstructionsBuf.WriteString(intro)
		mcpInstructionsBuf.WriteString("\n")
	}

	// bgCtx / bgWg bound background memory goroutines to the session lifetime.
	// On shutdown, bgCancel signals them to stop; bgWg lets us drain them
	// with a grace window rather than killing them immediately.
	bgCtx, bgCancel := context.WithCancel(context.Background())
	defer bgCancel()
	var bgWg sync.WaitGroup

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
	systemBlocks := agent.BuildSystemBlocks(mem, claudeMdPrompt+mcpInstructionsBuf.String(), skillEntries...)
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
		Hooks:             settings.FilterUntrustedHooks(&s.Hooks, cwd, !needsTrust),
		SessionID:         sessionID,
		Cwd:               cwd,
		AutoCompact:       true,
		MicroCompact:      true,
		LastAssistantTime: lastAssistant,
		ThinkingBudget:    thinkingBudget(),
		NotifyOnComplete:  true,
		BackgroundModel: func() string {
			return app.ClaudeRoleModel(cwd, settings.RoleBackground, compact.DefaultModel)
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
				bgWg.Go(func() {
					defer func() {
						extractMu.Lock()
						extractInflight = false
						extractMu.Unlock()
					}()
					ctx, cancel := context.WithTimeout(bgCtx, 5*time.Minute)
					defer cancel()
					recent := tui.SummarizeMessages(snapshot, 20)
					_ = memdir.RunExtract(ctx, cwd, recent, lp.RunBackgroundAgent)
				})
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
				bgWg.Go(func() {
					defer func() {
						sessionMemMu.Lock()
						sessionMemInflight = false
						sessionMemMu.Unlock()
					}()
					path, err := sessionmem.EnsureFile(projectDir, sess.ID)
					if err != nil {
						return
					}
					ctx, cancel := context.WithTimeout(bgCtx, 5*time.Minute)
					defer cancel()
					recent := tui.SummarizeMessages(snapshot, 30)
					_ = sessionmem.RunUpdate(ctx, path, recent, lp.RunBackgroundAgent)
				})
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

	// Wire a session-scoped async group so async hooks are cancellable and
	// drainable at shutdown instead of leaking as untracked goroutines.
	hooks.DefaultAsyncGroup = hooks.NewAsyncGroup(ctx)

	tuiErr := tui.Run(AppVersion, modelName, lp, c, gate, settings.FilterUntrustedHooks(&s.Hooks, cwd, !needsTrust), tui.RunOptions{
		AuthErr:                   authErr,
		Profile:                   prof,
		Session:                   sess,
		ResumedHistory:            resumedHistory,
		Resumed:                   continueMode && len(resumedHistory) > 0,
		MCPManager:                mcpManager,
		EnterPlan:                 rOpts.EnterPlan,
		ExitPlan:                  rOpts.ExitPlan,
		AskUser:                   rOpts.AskUser,
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
			tok, err := app.LoadAuth(ctx)
			if err != nil {
				return auth.PersistedTokens{}, nil, err
			}
			p, _ := profile.Fetch(ctx, tok.AccessToken)
			app.FillProfileAccountFallback(&p)
			app.SaveProfileAccountMetadata(p, auth.InferAccountKind(tok))
			app.RefreshClaudeAccountProfiles(ctx)
			return tok, &p, nil
		},
		NewAPIClient: func(tok auth.PersistedTokens) *api.Client {
			return app.NewAPIClient(tok, Version)
		},
		NeedsTrust: needsTrust,
		SetTrusted: func() error {
			return globalconfig.SetTrusted(cwd)
		},
	})

	// Drain async hooks: cancel their context and wait up to 5s for them to
	// finish before the process tears down further state.
	hooks.DefaultAsyncGroup.Shutdown(5 * time.Second)

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

	// Drain in-flight background memory goroutines. Cancel bgCtx first so
	// they abort any pending sub-agent API calls, then wait up to 10s.
	bgCancel()
	drainDone := make(chan struct{})
	go func() { bgWg.Wait(); close(drainDone) }()
	select {
	case <-drainDone:
	case <-time.After(10 * time.Second):
	}

	return tuiErr
}
