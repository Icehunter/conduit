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
	"github.com/icehunter/conduit/internal/bgreview"
	"github.com/icehunter/conduit/internal/buddy"
	"github.com/icehunter/conduit/internal/catalog"
	"github.com/icehunter/conduit/internal/ccr"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/globalconfig"
	"github.com/icehunter/conduit/internal/hooks"
	"github.com/icehunter/conduit/internal/instructions"
	"github.com/icehunter/conduit/internal/kernel"
	"github.com/icehunter/conduit/internal/lsp"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/memdir"
	"github.com/icehunter/conduit/internal/migrations"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/pendingedits"
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
	"github.com/icehunter/conduit/internal/tools/planmodetool"
	"github.com/icehunter/conduit/internal/tools/skillmanagetool"
	"github.com/icehunter/conduit/internal/tools/skilltool"
	"github.com/icehunter/conduit/internal/tools/syntheticoutputtool"
	"github.com/icehunter/conduit/internal/tools/worktreetool"
	"github.com/icehunter/conduit/internal/truncate"
	"github.com/icehunter/conduit/internal/ttsr"
	"github.com/icehunter/conduit/internal/tui"
	"github.com/icehunter/conduit/internal/updater"
)

// runREPL launches the full-screen Bubble Tea TUI.
// If credentials are absent or invalid the TUI still starts — it shows a
// "not logged in" welcome message and the user can /login from within.
func runREPL(continueMode bool, resumeID string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var startupWarnings []string
	warnf := func(format string, args ...any) {
		startupWarnings = append(startupWarnings, fmt.Sprintf(format, args...))
	}

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

	// Update check runs in parallel with the rest of startup. Result is
	// drained just before the TUI is built; if the check is still in
	// flight at that point we skip the notice (no blocking on slow
	// networks). The goroutine writes at most one value to a buffered
	// channel, so there's no leak if the receive is skipped.
	updateCh := make(chan string, 1)
	go func() {
		updCtx, cancelUpd := context.WithTimeout(ctx, 5*time.Second)
		defer cancelUpd()
		res, err := updater.New().Check(updCtx, AppVersion)
		if err != nil || !res.HasUpdate {
			return
		}
		updateCh <- fmt.Sprintf("conduit %s available (current %s) — %s", res.Latest, res.Current, res.UpgradeCmd)
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
	resumeSourcePath := ""

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
			resumeSourcePath = filePath
			if writeSession, err := session.ImportForWrite(cwd, filePath); err == nil {
				filePath = writeSession.FilePath
			} else {
				warnf("Could not import resumed session for writing: %v", err)
			}
			if history, err := session.LoadMessages(filePath); err == nil {
				resumedHistory = history
			} else {
				warnf("Could not load resumed session %q: %v", filepath.Base(filePath), err)
			}
		}
	} else if continueMode {
		// Load the most recent session for this directory.
		sessions, err := session.List(cwd)
		if err == nil && len(sessions) > 0 {
			most := sessions[0]
			sessionID = most.ID
			resumeSourcePath = most.FilePath
			filePath := most.FilePath
			if writeSession, err := session.ImportForWrite(cwd, most.FilePath); err == nil {
				filePath = writeSession.FilePath
			} else {
				warnf("Could not import latest session for writing: %v", err)
			}
			if history, err := session.LoadMessages(filePath); err == nil {
				resumedHistory = history
			} else {
				warnf("Could not load latest session %q: %v", filepath.Base(filePath), err)
			}
		}
	}

	var sess *session.Session
	var err error
	if resumeSourcePath != "" {
		sess, err = session.ImportForWrite(cwd, resumeSourcePath)
	} else {
		sess, err = session.New(cwd, sessionID)
	}
	if err != nil {
		// Non-fatal — session persistence failure shouldn't block the REPL.
		warnf("Session persistence is disabled for this run: %v", err)
		sess = nil
	}

	// Run one-shot settings migrations before loading. Idempotent: completed
	// IDs are recorded in settings.json so they never re-run.
	migrations.Run(settings.ClaudeDir())

	// Best-effort background cleanup for truncated outputs and CCR store.
	// Both operations are idempotent and non-fatal — we fire-and-forget.
	go func() {
		_ = truncate.Cleanup()           // best-effort; failure is non-fatal
		_ = ccr.DefaultStore().Cleanup() // best-effort; failure is non-fatal
	}()

	// Load settings (missing/invalid files are fine — defaults apply).
	s, settingsErr := settings.Load(cwd)
	if settingsErr != nil {
		warnf("Could not load settings; using defaults: %v", settingsErr)
	}
	if s == nil {
		s = &settings.Merged{DefaultMode: "default"}
	}
	usageStatusEnabled := s.UsageStatusEnabled
	if userSettings, err := settings.Load(""); err == nil && userSettings != nil {
		usageStatusEnabled = userSettings.UsageStatusEnabled
	}

	// Workspace trust check — mirrors CC's hasTrustDialogAccepted logic.
	// runPrint (-p) is non-interactive and skips the dialog; CLAUDE_CODE_SANDBOXED
	// bypasses it too (handled inside IsTrusted).
	needsTrust := false
	if trusted, trustErr := globalconfig.IsTrusted(cwd); trustErr == nil && !trusted {
		needsTrust = true
	}
	// Collect trusted ancestor paths for the permission gate so reads inside
	// trusted directories never prompt. Best-effort: ignore errors.
	trustedRoots, _ := globalconfig.TrustedAncestors(cwd)

	gate := permissions.New(cwd, trustedRoots, permissions.Mode(s.DefaultMode), s.Allow, s.Deny, s.Ask)

	importLegacySessions := func() {
		go func() {
			_, _ = session.ImportLegacyProject(cwd)
		}()
	}
	if !needsTrust {
		importLegacySessions()
	}
	go globalconfig.IncrementStartups()

	// additionalDirectories: auto-allow file operations under each directory.
	for _, dir := range s.AdditionalDirs {
		dir = filepath.Clean(dir)
		gate.AllowForSession("Read(" + dir + "/*)")
		gate.AllowForSession("Edit(" + dir + "/*)")
		gate.AllowForSession("Write(" + dir + "/*)")
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
	if err := mcpManager.ConnectAll(ctx, cwd, !needsTrust); err != nil {
		warnf("Could not connect MCP servers: %v", err)
	}

	// Create the LSP manager; servers are started on demand per file extension.
	// Apply any per-server overrides from conduit.json (cmd, args, env, disabled).
	var lspOverrides map[string]lsp.ServerOverride
	if conduitCfg, err := settings.LoadConduitConfig(); err == nil && len(conduitCfg.LSPServers) > 0 {
		lspOverrides = make(map[string]lsp.ServerOverride, len(conduitCfg.LSPServers))
		for k, v := range conduitCfg.LSPServers {
			lspOverrides[k] = lsp.ServerOverride{
				Cmd:      v.Cmd,
				Args:     v.Args,
				Env:      v.Env,
				Disabled: v.Disabled,
			}
		}
	}
	lspManager := lsp.NewManagerWithOverrides(lspOverrides)

	// Load plugins (non-fatal — missing plugins don't block startup).
	loadedPlugins, _ := plugins.LoadAll(cwd)

	// Collect plugin dirs for plugin-provided output styles.
	var pluginDirs []string
	for _, p := range loadedPlugins {
		if p.Dir != "" {
			pluginDirs = append(pluginDirs, p.Dir)
		}
	}

	// Build skill and agent listings for the system prompt.
	skillEntries := app.BuildSkillEntries(loadedPlugins)
	agentEntries := app.BuildAgentEntries(loadedPlugins)

	// Load auto-memory: ensure the directory exists and build the full memory
	// system-prompt block (type taxonomy + MEMORY.md content).
	// Mirrors loadMemoryPrompt() in src/memdir/memdir.ts.
	_ = memdir.EnsureDir(cwd)
	mem := memdir.BuildPrompt(cwd)

	// Load agent instruction files (CLAUDE.md, AGENTS.md, copilot, cursor).
	claudeMdFiles, claudeMdErr := instructions.Load(cwd)
	if claudeMdErr != nil {
		warnf("Could not load instruction files: %v", claudeMdErr)
	}
	claudeMdPrompt := instructions.BuildPrompt(claudeMdFiles)

	// Load TTSR (Token-Time Stopping Rules) from .conduit/ttsr/*.md.
	ttsrRules, ttsrLoadErr := ttsr.Load(cwd)
	if ttsrLoadErr != nil {
		warnf("Could not load TTSR rules: %v", ttsrLoadErr)
	}

	c := app.NewAPIClient(tok, Version)

	// Kernel manager: one long-lived Python/Node interpreter per (session, lang).
	// The reaper goroutine closes kernels that have been idle for 10+ minutes.
	kernelMgr := kernel.NewManager()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case t := <-ticker.C:
				kernelMgr.Reap(t)
			case <-ctx.Done():
				return
			}
		}
	}()

	// pending-edits table for the diff-first review gate. The GatedStager
	// wraps it with a mode check so staging only activates in acceptEdits mode.
	// gate is defined below; the stager captures a pointer to it via the Gate field.
	pendingTable := pendingedits.NewTable()
	diffReviewHook := &tui.DiffReviewHook{}

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
		Stager:        &pendingedits.GatedStager{Table: pendingTable, Gate: gate},
		SessionEnv:    sessionEnv,
		KernelManager: kernelMgr,
		SessionID:     sessionID,
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

	// Inject prior session memory only when transcript history could not be
	// loaded. If we already resumed full history, re-injecting summary.md adds
	// duplicate context cost on every turn.
	priorSummary := ""
	if continueMode && sess != nil && projectDir != "" && len(resumedHistory) == 0 {
		priorSummary, _ = sessionmem.Load(sessionmem.Path(projectDir, sess.ID))
		priorSummary = truncateResumeSummary(priorSummary, 6000)
	}

	// Build system blocks; append prior session summary on resume so the
	// new turn picks up where the previous one left off. Append (not
	// prepend) keeps the Max wire fingerprint intact.
	systemBlocks := agent.BuildSystemBlocks(mem, claudeMdPrompt+mcpInstructionsBuf.String(), projectDir, agentEntries, skillEntries...)
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

	// Merge plugin hooks with user/project hooks before filtering for trust.
	mergedHooks := plugins.MergeHooksFrom(loadedPlugins, &s.Hooks)

	var lp *agent.Loop
	lp = agent.NewLoop(c, reg, agent.LoopConfig{
		Model:             modelName,
		MaxTokens:         internalmodel.MaxTokens,
		System:            systemBlocks,
		MaxTurns:          agent.DefaultMainMaxTurns,
		Gate:              gate,
		Hooks:             settings.FilterUntrustedHooks(mergedHooks, cwd, !needsTrust),
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
		RoleResolver: func(role string) (string, *api.Client, bool) {
			latest, err := settings.Load(cwd)
			if err != nil {
				return "", nil, false
			}
			provider, ok := latest.ProviderForRole(role)
			if !ok || provider == nil || provider.Model == "" {
				return "", nil, false
			}
			client, err := app.NewProviderAPIClient(*provider, secure.NewDefault(), Version)
			if err != nil {
				return provider.Model, nil, true
			}
			return provider.Model, client, true
		},
		OnFileAccess: func(op, path string) {
			if sess != nil {
				_ = sess.AppendFileAccess(op, path)
			}
		},
		OnSubAgentUsage: func(subModel string, usage api.Usage) {
			if sess != nil {
				costUSD := api.CostUSDForModel(subModel, usage)
				_ = sess.AppendCost(usage.InputTokens, usage.OutputTokens, costUSD)
			}
		},
		OnEndTurn: func(history []api.Message) {
			snapshot := make([]api.Message, len(history))
			copy(snapshot, history)

			// Diff-first review gate: if staged edits are pending, open the
			// overlay and block until the user approves or reverts each file.
			// This runs synchronously before memory extraction so the agent
			// doesn't start a new turn while the user is reviewing.
			if pendingTable.Len() > 0 && diffReviewHook.AskReview != nil {
				ctx2, cancel2 := context.WithTimeout(bgCtx, 10*time.Minute)
				result := diffReviewHook.AskReview(ctx2)
				cancel2()
				var flushResults []pendingedits.FlushResult
				// Flush approved entries to disk.
				if len(result.Approved) > 0 {
					flushResults = pendingedits.Flush(result.Approved)
					for i, fr := range flushResults {
						if fr.Applied {
							if sess != nil {
								_ = sess.AppendFileAccess("write", fr.Path)
								_ = sess.AppendPendingEditResult(fr.Path, result.Approved[i].Op.String(), "approved", result.Approved[i].ToolName)
							}
						} else if fr.Err != nil && sess != nil {
							_ = sess.AppendPendingEditResult(fr.Path, result.Approved[i].Op.String(), "flush-failed", result.Approved[i].ToolName)
						}
					}
				}
				// Record reverted/requested entries in the JSONL.
				for _, e := range result.Requested {
					if sess != nil {
						_ = sess.AppendPendingEditResult(e.Path, e.Op.String(), "requested", e.ToolName)
					}
				}
				// If hunks were rejected or approved edits failed to flush, inject
				// feedback as the agent's next user turn so it can address it.
				if followup := diffReviewFollowupText(result, flushResults); followup != "" && diffReviewHook.EnqueueFollowup != nil {
					diffReviewHook.EnqueueFollowup(followup)
				}
			}

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
		OnToolBatchComplete: func(pendingEdits int) bool {
			// In acceptEditsLive mode, pause for mid-turn hunk review when
			// the staged-edit count hits the configured threshold.
			if gate.Mode() != permissions.ModeAcceptEditsLive {
				return false
			}
			n := pendingTable.Len()
			threshold := settings.DiffReviewMidTurnThreshold()
			if n < threshold {
				return false
			}
			if diffReviewHook.AskReview == nil {
				return false
			}
			ctx2, cancel2 := context.WithTimeout(bgCtx, 10*time.Minute)
			result := diffReviewHook.AskReview(ctx2)
			cancel2()
			var flushResults []pendingedits.FlushResult
			if len(result.Approved) > 0 {
				flushResults = pendingedits.Flush(result.Approved)
				for i, fr := range flushResults {
					if fr.Applied {
						if sess != nil {
							_ = sess.AppendFileAccess("write", fr.Path)
							_ = sess.AppendPendingEditResult(fr.Path, result.Approved[i].Op.String(), "approved", result.Approved[i].ToolName)
						}
					} else if fr.Err != nil && sess != nil {
						_ = sess.AppendPendingEditResult(fr.Path, result.Approved[i].Op.String(), "flush-failed", result.Approved[i].ToolName)
					}
				}
			}
			for _, e := range result.Requested {
				if sess != nil {
					_ = sess.AppendPendingEditResult(e.Path, e.Op.String(), "requested", e.ToolName)
				}
			}
			if followup := diffReviewFollowupText(result, flushResults); followup != "" && diffReviewHook.EnqueueFollowup != nil {
				diffReviewHook.EnqueueFollowup(followup)
			}
			// Pause only when there is a synthetic follow-up to submit. An
			// all-approved review can continue in the same loop now that the
			// staged edits are flushed to disk.
			return diffReviewShouldPause(result, flushResults)
		},
		IsOAuthSubscription: auth.InferAccountKind(tok) == auth.AccountKindClaudeAI,
	})

	// Install TTSR rules loaded from .conduit/ttsr/*.md.
	if len(ttsrRules) > 0 {
		lp.SetTTSRRules(ttsrRules)
	}

	// Wire provider failover when chains are configured in conduit.json.
	// The chain is re-resolved on each failover so hotloaded config changes
	// take effect between providers.
	if len(s.ProviderChains) > 0 {
		secureStore := secure.NewDefault()
		lp.SetProviderChain(
			settings.RoleDefault,
			func(role string) []settings.ActiveProviderSettings {
				// Re-load merged settings so that a conduit.json edit during a
				// session picks up updated chains without restarting.
				if latest, err := settings.Load(cwd); err == nil && latest != nil {
					if chain, ok := latest.ProviderChainForRole(role); ok {
						return chain
					}
				}
				chain, _ := s.ProviderChainForRole(role)
				return chain
			},
			func(p settings.ActiveProviderSettings) (*api.Client, error) {
				return app.NewProviderAPIClient(p, secureStore, Version)
			},
		)
	}

	// Register AgentTool and SkillTool now that the loop exists.
	agentRegistry := plugins.NewAgentRegistry(loadedPlugins)
	reg.Register(agenttool.New(
		// Plain Task calls (no subagent_type) use RunSubAgentTyped so they
		// appear in the sub-agent drill-in panel. RunBackgroundAgent marks
		// entries as Background:true which hides them from the panel.
		func(ctx context.Context, prompt string) (string, error) {
			r, err := lp.RunSubAgentTyped(ctx, prompt, agent.SubAgentSpec{
				Mode: permissions.ModeBypassPermissions,
			})
			return r.Text, err
		},
		agentRegistry,
		func(ctx context.Context, prompt, systemPrompt, model, role string, tools []string) (string, error) {
			r, err := lp.RunSubAgentTyped(ctx, prompt, agent.SubAgentSpec{
				SystemPrompt: systemPrompt,
				Model:        model,
				Role:         role,
				Tools:        tools,
			})
			return r.Text, err
		},
	))
	skillLoader := plugins.NewSkillLoader(loadedPlugins, cwd)
	reg.Register(skilltool.New(
		skillLoader,
		lp.RunBackgroundAgent,
		func(ctx context.Context, prompt string, tools []string) (string, error) {
			r, err := lp.RunSubAgentTyped(ctx, prompt, agent.SubAgentSpec{Tools: tools})
			return r.Text, err
		},
	))

	// Wire periodic background memory and skill reviews. The reviewer fires
	// after every Nth end_turn (memory: 5, skill: 7) and runs a restricted
	// sub-agent. Skill reviews inject the SkillManage tool via ExtraTools so
	// the main registry is not polluted. Memory reviews use the standard tools
	// (Read/Write/Glob/Grep) already in the registry.
	lp.SetBackgroundReviewer(bgreview.NewSplit(
		bgreview.Config{}, // defaults: memory every 5 turns, skill every 7 turns
		cwd,
		// Memory review runner — uses the standard background model and registry.
		func(ctx context.Context, prompt string, tools []string) (string, error) {
			r, err := lp.RunSubAgentTyped(ctx, prompt, agent.SubAgentSpec{
				Tools:      tools,
				Background: true,
				Mode:       permissions.ModeBypassPermissions,
			})
			return r.Text, err
		},
		// Skill review runner — augments child registry with SkillManage.
		func(ctx context.Context, prompt string, tools []string) (string, error) {
			r, err := lp.RunSubAgentTyped(ctx, prompt, agent.SubAgentSpec{
				Tools:      tools,
				ExtraTools: []tool.Tool{skillmanagetool.New(cwd, skillmanagetool.WithAgentProvenance())},
				Background: true,
				Mode:       permissions.ModeBypassPermissions,
			})
			return r.Text, err
		},
	))

	// Wire a session-scoped async group so async hooks are cancellable and
	// drainable at shutdown instead of leaking as untracked goroutines.
	hooks.DefaultAsyncGroup = hooks.NewAsyncGroup(ctx)

	// Drain the update-check result. Non-blocking: if the goroutine is
	// still running we proceed without a notice (it'll surface next launch
	// from cache).
	select {
	case msg := <-updateCh:
		startupWarnings = append(startupWarnings, msg)
	default:
	}

	// Load the model capability catalog from disk (best-effort; never blocks startup).
	initialCatalog := catalog.Load(settings.ConduitDir())

	tuiErr := tui.Run(AppVersion, modelName, lp, c, gate, settings.FilterUntrustedHooks(mergedHooks, cwd, !needsTrust), tui.RunOptions{
		AuthErr:                   authErr,
		Profile:                   prof,
		Session:                   sess,
		ResumedHistory:            resumedHistory,
		Resumed:                   continueMode && len(resumedHistory) > 0,
		MCPManager:                mcpManager,
		EnterPlan:                 rOpts.EnterPlan,
		ExitPlan:                  rOpts.ExitPlan,
		AskUser:                   rOpts.AskUser,
		ClaudeMd:                  claudeMdPrompt + mcpInstructionsBuf.String(),
		Skills:                    skillEntries,
		Agents:                    agentEntries,
		InitialOutputStyle:        s.OutputStyle,
		InitialUsageStatusEnabled: usageStatusEnabled,
		InitialLocalMode:          initialLocalMode,
		InitialLocalServer:        initialLocalServer,
		InitialLocalDirectTool:    initialLocalDirectTool,
		InitialLocalImplementTool: initialLocalImplementTool,
		InitialProviders:          s.Providers,
		InitialRoles:              s.Roles,
		InitialCouncilProviders:   s.CouncilProviders,
		StartupWarnings:           startupWarnings,
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
		NewProviderAPIClient: func(provider settings.ActiveProviderSettings) (*api.Client, error) {
			return app.NewProviderAPIClient(provider, secure.NewDefault(), Version)
		},
		NeedsTrust: needsTrust,
		SetTrusted: func() error {
			if err := globalconfig.SetTrusted(cwd); err != nil {
				return err
			}
			importLegacySessions()
			return nil
		},
		PendingEdits:   pendingTable,
		DiffReview:     diffReviewHook,
		SteerMessage:   lp.InjectSteerMessage,
		InitialCatalog: initialCatalog,
		LSPManager:     lspManager,
	})

	// Dispose kernel processes for this session before exiting.
	// Best-effort: failure is non-fatal; the OS will clean up.
	kernelMgr.DisposeSession(sessionID)

	// Fast exit: skip all cleanup drains regardless of how the TUI exited.
	// Terminal state was already restored by Bubble Tea; session data was
	// written synchronously during OnEndTurn; the OS kills LSP/MCP
	// subprocesses. The drain stack can block up to 25s — not acceptable.
	if tuiErr != nil {
		os.Exit(1)
	}
	os.Exit(0)

	// Drain async hooks: cancel their context and wait up to 5s for them to
	// finish before the process tears down further state.
	hooks.DefaultAsyncGroup.Shutdown(5 * time.Second)

	// Close subprocess-backed integrations before returning to the shell so
	// lingering MCP/LSP children cannot keep the conduit command alive.
	// Each manager has its own 5s timeout; wrap in an overall 10s timeout
	// as a safety net to guarantee the user returns to their shell.
	managersDone := make(chan struct{})
	go func() {
		lspManager.Close()
		mcpManager.Close()
		close(managersDone)
	}()
	select {
	case <-managersDone:
	case <-time.After(10 * time.Second):
		// Safety timeout: managers didn't close in time, continue anyway.
	}

	// Auto-dream: after a normal session ends, check whether memory consolidation
	// should fire. Skip this optional network work on interrupt/cancel paths so
	// Ctrl+C returns to the shell promptly.
	if sess != nil && ctx.Err() == nil && tuiErr == nil {
		sessionDir := sess.ProjectDir
		if memdir.ShouldDream(cwd, sessionDir) {
			dreamCtx, dreamCancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = memdir.RunDream(dreamCtx, cwd, sessionDir, lp.RunBackgroundAgent)
			dreamCancel()
		}

		// Curator: weekly deep-clean pass — consolidates memory and prunes/merges
		// skills. Fires after 7 days or 10 accumulated sessions, whichever comes
		// first. Runs in the background so it does not block the shell return.
		if memdir.ShouldRunCurator(cwd, sessionDir) {
			curatorCtx, curatorCancel := context.WithTimeout(context.Background(), 5*time.Minute)
			go func() {
				defer curatorCancel()
				// Use RunSubAgentTyped with ExtraTools so the curator agent has
				// access to SkillManage in addition to the standard tool set.
				runner := func(ctx context.Context, prompt string) (string, error) {
					r, err := lp.RunSubAgentTyped(ctx, prompt, agent.SubAgentSpec{
						ExtraTools: []tool.Tool{skillmanagetool.New(cwd)},
						Background: true,
						Mode:       permissions.ModeBypassPermissions,
					})
					return r.Text, err
				}
				_ = memdir.RunCurator(curatorCtx, cwd, sessionDir, runner)
			}()
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

func diffReviewShouldPause(result tui.DiffReviewResult, flushResults []pendingedits.FlushResult) bool {
	return diffReviewFollowupText(result, flushResults) != ""
}

func diffReviewFollowupText(result tui.DiffReviewResult, flushResults []pendingedits.FlushResult) string {
	var parts []string
	if failures := diffReviewFlushFailures(flushResults); len(failures) > 0 {
		parts = append(parts, "<diff_apply_errors>\n"+strings.Join(failures, "\n")+"\n</diff_apply_errors>")
	}
	if strings.TrimSpace(result.FollowupMessage) != "" {
		parts = append(parts, result.FollowupMessage)
	}
	return strings.Join(parts, "\n\n")
}

func diffReviewFlushFailures(results []pendingedits.FlushResult) []string {
	failures := make([]string, 0)
	for _, result := range results {
		if result.Err != nil {
			failures = append(failures, fmt.Sprintf("- %s: %v", result.Path, result.Err))
		}
	}
	return failures
}

func truncateResumeSummary(summary string, maxBytes int) string {
	summary = strings.TrimSpace(summary)
	if summary == "" || maxBytes <= 0 || len(summary) <= maxBytes {
		return summary
	}
	cut := strings.LastIndex(summary[:maxBytes], "\n")
	if cut <= 0 {
		cut = maxBytes
	}
	return summary[:cut] + "\n\n(Truncated for context efficiency. Open session-memory/summary.md for the full text.)"
}
