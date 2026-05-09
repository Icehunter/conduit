package tui

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/keybindings"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/memdir"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/outputstyles"
	"github.com/icehunter/conduit/internal/pendingedits"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/planusage"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/profile"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tools/askusertool"
	"github.com/icehunter/conduit/internal/tools/automodetool"
	"github.com/icehunter/conduit/internal/tools/planmodetool"
)

// altScreenExit/clearScreen are ANSI sequences for terminal cleanup.
const (
	altScreenExit = "\x1b[?1049l\x1b[?25h" // exit alt-screen, show cursor
	clearScreen   = "\x1b[2J\x1b[H"        // erase display + cursor home
)

type runOptions struct {
	apiClient   *api.Client
	gate        *permissions.Gate
	hooksConfig *settings.HooksSettings
}

// RunOptions carries optional TUI startup parameters passed from main.
type RunOptions struct {
	// AuthErr is non-nil when no credentials were found at startup.
	AuthErr error

	// Profile is the user's account/subscription info fetched at startup.
	Profile profile.Info

	// Session is the active session for transcript persistence.
	Session *session.Session

	// ResumedHistory is the message history loaded from a previous session.
	ResumedHistory []api.Message

	// Resumed is true when --continue loaded a prior session.
	Resumed bool

	// MCPManager is the live MCP connection manager (may be nil).
	MCPManager *mcp.Manager

	// LoadAuth reloads credentials + profile after a successful /login.
	LoadAuth func(ctx context.Context) (auth.PersistedTokens, *profile.Info, error)

	// NewAPIClient constructs a fresh API client for the given persisted token.
	NewAPIClient func(auth.PersistedTokens) *api.Client
	// NewProviderAPIClient constructs a client for non-account providers.
	NewProviderAPIClient func(settings.ActiveProviderSettings) (*api.Client, error)

	// Interactive tool stubs — the TUI wires their callbacks after startup.
	EnterPlan     *planmodetool.EnterPlanMode
	ExitPlan      *planmodetool.ExitPlanMode
	AskUser       *askusertool.AskUserQuestion
	EnterAutoMode *automodetool.EnterAutoMode
	ExitAutoMode  *automodetool.ExitAutoMode

	// InitialOutputStyle is the style name to activate at startup (from settings).
	InitialOutputStyle string
	// InitialUsageStatusEnabled controls the conduit-only plan usage footer.
	InitialUsageStatusEnabled bool
	// InitialLocalMode restores the hidden /local-mode compatibility bridge.
	InitialLocalMode bool
	// InitialLocalServer is the MCP server normal chat should route to when
	// InitialLocalMode is enabled.
	InitialLocalServer string
	// InitialLocalDirectTool is the MCP tool used for normal local-mode chat.
	InitialLocalDirectTool string
	// InitialLocalImplementTool is the MCP tool used for scoped local diffs.
	InitialLocalImplementTool string
	// InitialActiveProvider is the provider shape loaded from conduit.json.
	InitialActiveProvider *settings.ActiveProviderSettings
	// InitialProviders/Roles are conduit's named provider role bindings.
	InitialProviders map[string]settings.ActiveProviderSettings
	InitialRoles     map[string]string
	// InitialCouncilProviders is the saved list of provider keys for council mode.
	InitialCouncilProviders []string
	// FetchPlanUsage returns the current Claude plan usage windows for the
	// selected provider/account. Nil disables fetching even if the footer
	// setting is enabled.
	FetchPlanUsage func(context.Context, settings.ActiveProviderSettings) (planusage.Info, error)

	// PluginDirs is the list of installed plugin root directories, used to
	// load plugin-provided output styles (lowest priority — overridden by user/project).
	PluginDirs []string

	// NeedsTrust is true when the current directory hasn't been accepted in
	// Conduit-owned trust state. The TUI shows the trust dialog before any agent turn.
	NeedsTrust bool
	// SetTrusted persists workspace trust acceptance.
	SetTrusted func() error
	// StartupWarnings are non-fatal startup failures shown as system messages.
	StartupWarnings []string

	// ClaudeMd is the concatenated CLAUDE.md + MCP server instructions passed to
	// the initial BuildSystemBlocks call. Carried here so output-style rebuilds
	// can include it instead of passing an empty string.
	ClaudeMd string
	// Skills is the skill listing from the initial BuildSystemBlocks call.
	Skills []agent.SkillEntry

	// PendingEdits is the staging table for the diff-first review gate.
	// When non-nil, end-of-turn triggers the diff review overlay if staged
	// edits are present (only in acceptEdits mode — GatedStager handles gating).
	PendingEdits *pendingedits.Table

	// DiffReview is a stub whose AskReview field Run() wires after prog starts.
	// mainrepl creates one, passes it here, and calls DiffReview.AskReview
	// from OnEndTurn when PendingEdits.Len() > 0.
	DiffReview *DiffReviewHook
}

// Run starts the full-screen TUI and blocks until the user exits.
// Variadic tail accepts: *api.Client, *permissions.Gate, *settings.HooksSettings,
// RunOptions (in any order).
func Run(version, modelName string, loop *agent.Loop, extras ...any) error {
	var prog *tea.Program

	opts := &runOptions{}
	var runOpts RunOptions
	for _, extra := range extras {
		switch v := extra.(type) {
		case *api.Client:
			opts.apiClient = v
		case *permissions.Gate:
			opts.gate = v
		case *settings.HooksSettings:
			opts.hooksConfig = v
		case RunOptions:
			runOpts = v
		}
	}

	// Populate coordinator MCP names from connected servers so the coordinator
	// system prompt knows what workers can access.
	if runOpts.MCPManager != nil {
		servers := runOpts.MCPManager.Servers()
		names := make([]string, 0, len(servers))
		for _, srv := range servers {
			names = append(names, srv.Name)
		}
		agent.CoordinatorMCPNames = names
	}

	// Session state shared between commands and the TUI model.
	// live is a thread-safe bag updated by Model.syncLive() on every relevant
	// state change; command callbacks read from it so they see current values.
	live := &LiveState{}
	live.SetModelName(modelName) // seed with startup value
	live.SetLocalMode(runOpts.InitialLocalMode, runOpts.InitialLocalServer)
	usageStatusEnabled := runOpts.InitialUsageStatusEnabled

	// modelPtr is still used for methods that can only run inside the event loop
	// (TasksSummary, LastThinking, CopyLastResponse — they read m.messages).
	var modelPtr *Model

	reg := commands.New()
	commands.RegisterBuiltins(reg)
	commands.RegisterModelCommand(reg,
		func() string {
			if enabled, server := live.LocalMode(); enabled {
				if server == "" {
					server = "local-router"
				}
				return "local:" + server
			}
			return internalmodel.Resolve()
		},
		func(name string) { loop.SetModel(name) },
		configuredAccountProviders,
		runOpts.MCPManager,
		runOpts.InitialProviders,
	)
	commands.RegisterCompactCommand(reg)
	commands.RegisterPermissionsCommand(reg, opts.gate)
	commands.RegisterHooksCommand(reg, opts.hooksConfig)
	commands.RegisterCoordinatorCommand(reg)
	commands.RegisterAccountCommand(reg)
	commands.RegisterMiscCommands(reg)
	commands.RegisterTerminalSetupCommand(reg)
	commands.RegisterPromptCommands(reg)
	commands.RegisterMCPCommand(reg, runOpts.MCPManager)
	commands.RegisterLocalCommands(reg, runOpts.MCPManager, runOpts.InitialActiveProvider, runOpts.InitialProviders)
	commands.RegisterRTKCommands(reg)
	commands.RegisterCouncilCommands(reg)
	commands.RegisterBuddyCommand(reg, func() string {
		// Use email as stable user ID for companion generation.
		return runOpts.Profile.Email
	})

	// Load plugins and register their slash commands + browser.
	cwd, _ := os.Getwd()
	commands.RegisterOutputStyleCommand(reg, cwd)
	commands.RegisterMCPApproveCommand(reg, runOpts.MCPManager, cwd)
	var loadedPlugins []*plugins.Plugin
	if ps, err := plugins.LoadAll(cwd); err == nil {
		loadedPlugins = ps
	}
	commands.RegisterPluginCommands(reg, loadedPlugins)
	commands.RegisterPluginSkillCommands(reg, loadedPlugins)
	commands.RegisterBundledSkillCommands(reg)
	commands.RegisterPluginBrowserCommand(reg, loadedPlugins)
	commands.RegisterSkillsCommand(reg, loadedPlugins)
	commands.RegisterRecordingCommand(reg)

	sessionStart := time.Now()

	state := &commands.SessionState{
		GetCost: func() string {
			if modelPtr == nil {
				return "No session data."
			}
			return modelPtr.CostSummary()
		},
		Logout: func() error {
			return logoutCredentials()
		},
		GetCwd: func() string {
			cwd, _ := os.Getwd()
			return cwd
		},
		// Rewind passes through n — the actual history mutation happens in
		// applyCommandResult when it receives the "rewind" result type.
		Rewind: func(n int) int { return n },
		GetTurnCosts: func() []float64 {
			return live.TurnCosts()
		},
		GetStatus: func() string {
			tokens := live.ContextInputTokens()
			_, _, cost := live.Tokens()
			mode := "default"
			switch live.PermissionMode() {
			case permissions.ModeAcceptEdits:
				mode = "accept-edits"
			case permissions.ModePlan:
				mode = "plan"
			case permissions.ModeBypassPermissions:
				mode = "auto"
			}
			pct := 0
			if tokens > 0 {
				window := live.ContextWindow()
				if window <= 0 {
					window = internalmodel.ContextWindowFor(live.ModelName())
				}
				pct = tokens * 100 / window
				if pct > 100 {
					pct = 100
				}
			}
			modelDisplay := live.ModelName()
			if live.FastMode() {
				modelDisplay += " ⚡"
			}
			effort := live.EffortLevel()
			if effort == "" {
				effort = "normal"
			}
			var sb strings.Builder
			sb.WriteString("Model:   " + modelDisplay + "\n")
			sb.WriteString("Mode:    " + mode + "\n")
			sb.WriteString("Effort:  " + effort + "\n")
			fmt.Fprintf(&sb, "Context: %d%% (%d tokens)\n", pct, tokens)
			if cost > 0 {
				fmt.Fprintf(&sb, "Cost:    $%.4f\n", cost)
			}
			if id := live.SessionID(); id != "" {
				sb.WriteString("Session: " + id + "\n")
			}
			if w := live.RateLimitWarning(); w != "" {
				sb.WriteString("Limits:  " + w + "\n")
			}
			return strings.TrimRight(sb.String(), "\n")
		},
		GetTasks: func() string {
			if modelPtr == nil {
				return "No active tasks."
			}
			return modelPtr.TasksSummary()
		},
		GetAgents: func() string {
			return "No active sub-agents."
		},
		GetLastThinking: func() string {
			if modelPtr == nil {
				return ""
			}
			return modelPtr.LastThinking()
		},
		GetTokens: func() (int, int, float64) {
			input, output, cost := live.Tokens()
			return input, output, cost
		},
		GetColor: func() bool { return true },
		SetColor: func(bool) {},
		CopyLast: func() string {
			if modelPtr == nil {
				return "Nothing to copy."
			}
			return modelPtr.CopyLastResponse()
		},
		// /fast — toggle between Default and Fast model.
		GetFast: func() bool { return live.FastMode() },
		SetFast: func(on bool) {
			live.SetFastMode(on)
			newModel := internalmodel.Default
			if on {
				newModel = internalmodel.Fast
			}
			loop.SetModel(newModel)
			live.SetModelName(newModel)
			// Notify the TUI to update the model name and fast badge.
			if prog != nil {
				prog.Send(setModelNameMsg{name: newModel, fast: on})
			}
		},
		GetUsageStatusEnabled: func() bool {
			return usageStatusEnabled
		},
		SetUsageStatusEnabled: func(on bool) error {
			usageStatusEnabled = on
			return settings.SaveConduitUsageStatusEnabled(on)
		},
		// /effort — set thinking budget.
		GetEffort: func() string {
			level := live.EffortLevel()
			if level == "" {
				return "normal"
			}
			return level
		},
		SetEffort: func(level string) {
			live.SetEffortLevel(level)
			budget := internalmodel.ThinkingBudgets[level]
			loop.SetThinkingBudget(budget)
		},
		// /rename — persist a title to the session JSONL.
		// Uses live.SessionFile() so it follows the active session after /resume.
		RenameSession: func(title string) error {
			if path := live.SessionFile(); path != "" {
				return session.FromFile(path).SetTitle(title)
			}
			if runOpts.Session == nil {
				return fmt.Errorf("no active session")
			}
			return runOpts.Session.SetTitle(title)
		},
		// /tag — persist a tag to the session JSONL ("" clears).
		TagSession: func(tag string) error {
			if path := live.SessionFile(); path != "" {
				return session.FromFile(path).AppendTag(tag)
			}
			if runOpts.Session == nil {
				return fmt.Errorf("no active session")
			}
			return runOpts.Session.AppendTag(tag)
		},
		// GetSessionTag — look up the latest tag for /session display.
		GetSessionTag: func() string {
			if runOpts.Session == nil {
				return ""
			}
			tag, _ := session.LoadTag(runOpts.Session.FilePath)
			return tag
		},
		// /files — deduplicated read/write lists from the session JSONL.
		GetSessionFiles: func() (reads, writes []string) {
			if runOpts.Session == nil {
				return nil, nil
			}
			entries, err := session.LoadFileAccess(runOpts.Session.FilePath)
			if err != nil {
				return nil, nil
			}
			seenR, seenW := map[string]bool{}, map[string]bool{}
			for _, e := range entries {
				switch e.Op {
				case "read":
					if !seenR[e.Path] {
						seenR[e.Path] = true
						reads = append(reads, e.Path)
					}
				case "write":
					if !seenW[e.Path] {
						seenW[e.Path] = true
						writes = append(writes, e.Path)
					}
				}
			}
			return reads, writes
		},
		// /usage — rate limit warning from LiveState.
		GetRateLimitWarning: func() string {
			return live.RateLimitWarning()
		},
		// /doctor — verify the stored bearer token is non-empty.
		CheckAuth: func() error {
			if runOpts.AuthErr != nil {
				return runOpts.AuthErr
			}
			return nil
		},
		// /session — session ID, file path, message count, start time.
		GetSessionInfo: func() (id, path string, messages int, startedAt time.Time) {
			if runOpts.Session != nil {
				id = runOpts.Session.ID
				path = runOpts.Session.FilePath
			}
			if modelPtr != nil {
				messages = len(modelPtr.messages)
			}
			startedAt = sessionStart
			return
		},
		// GetKeybindings — returns the live binding list for /keybindings display.
		GetKeybindings: func() []keybindings.Binding {
			if modelPtr == nil {
				return keybindings.Defaults()
			}
			return modelPtr.AllBindings()
		},
		// GetSessionActivity — last activity timestamp from JSONL for idle reporting.
		GetSessionActivity: func() time.Time {
			if runOpts.Session == nil {
				return time.Time{}
			}
			act, _ := session.LoadActivity(runOpts.Session.FilePath)
			return act.LastActivity
		},
		// /memory extract — fork a sub-agent over the recent conversation
		// to update the auto-memory dir. Mirrors CC's extractMemories flow.
		ExtractMemory: func() (string, error) {
			if loop == nil {
				return "", fmt.Errorf("no agent loop")
			}
			if modelPtr == nil || len(modelPtr.history) == 0 {
				return "No conversation to extract from yet.", nil
			}
			recent := SummarizeMessages(modelPtr.history, 20)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := memdir.RunExtract(ctx, cwd, recent, loop.RunBackgroundAgent); err != nil {
				return "", err
			}
			return "Memory extraction complete.", nil
		},
	}
	commands.RegisterSessionCommands(reg, state)

	apiClient := opts.apiClient

	cfg := Config{
		Version:                   version,
		ModelName:                 modelName,
		Loop:                      loop,
		Program:                   &prog,
		Commands:                  reg,
		APIClient:                 apiClient,
		MCPManager:                runOpts.MCPManager,
		Gate:                      opts.gate,
		AuthErr:                   runOpts.AuthErr,
		Profile:                   runOpts.Profile,
		Session:                   runOpts.Session,
		ResumedHistory:            runOpts.ResumedHistory,
		Resumed:                   runOpts.Resumed,
		LoadAuth:                  runOpts.LoadAuth,
		NewAPIClient:              runOpts.NewAPIClient,
		NewProviderAPIClient:      runOpts.NewProviderAPIClient,
		Live:                      live,
		NeedsTrust:                runOpts.NeedsTrust,
		SetTrusted:                runOpts.SetTrusted,
		UsageStatusEnabled:        runOpts.InitialUsageStatusEnabled,
		InitialLocalMode:          runOpts.InitialLocalMode,
		InitialLocalServer:        runOpts.InitialLocalServer,
		InitialLocalDirectTool:    runOpts.InitialLocalDirectTool,
		InitialLocalImplementTool: runOpts.InitialLocalImplementTool,
		InitialActiveProvider:     runOpts.InitialActiveProvider,
		InitialProviders:          runOpts.InitialProviders,
		InitialRoles:              runOpts.InitialRoles,
		InitialCouncilProviders:   runOpts.InitialCouncilProviders,
		StartupWarnings:           runOpts.StartupWarnings,
		ClaudeMd:                  runOpts.ClaudeMd,
		Skills:                    runOpts.Skills,
		BackgroundModel: func() string {
			if loop != nil {
				return loop.BackgroundModel()
			}
			return compact.DefaultModel
		},
		FetchPlanUsage: runOpts.FetchPlanUsage,
	}
	// Seed session ID into LiveState once it's known.
	if runOpts.Session != nil {
		live.SetSessionID(runOpts.Session.ID)
	}

	m := New(cfg)
	// Apply saved output style at startup.
	// Plugin styles are lowest priority; user/project styles override them.
	if runOpts.InitialOutputStyle != "" {
		pluginStyles := outputstyles.LoadFromPluginDirs(runOpts.PluginDirs)
		userProjStyles, _ := outputstyles.LoadAll(cwd)
		// Build merged map: plugin < user/project.
		byName := make(map[string]outputstyles.Style)
		for _, s := range pluginStyles {
			byName[s.Name] = s
		}
		for _, s := range userProjStyles {
			byName[s.Name] = s
		}
		if s, ok := byName[runOpts.InitialOutputStyle]; ok {
			m.outputStyleName = s.Name
			m.outputStylePrompt = s.Prompt
			// Push the style into the loop's system blocks. Built-in
			// "default" carries an empty Prompt — leave the loop's
			// default base blocks alone in that case. The style block
			// must be APPENDED, never prepended, because the Max
			// fingerprint requires system[0] to be the billing header.
			if s.Prompt != "" && loop != nil {
				mem := memdir.BuildPrompt(cwd)
				baseBlocks := agent.BuildSystemBlocks(mem, m.cfg.ClaudeMd, m.cfg.Skills...)
				styleBlock := api.SystemBlock{
					Type: "text",
					Text: "# Output style: " + s.Name + "\n\n" + s.Prompt,
				}
				loop.SetSystem(append(baseBlocks, styleBlock))
			}
		}
	}
	modelPtr = &m
	// AltScreen now lives on the View struct (bubbletea v2) — see Model.View.
	prog = tea.NewProgram(m)

	// Wire interactive permission prompts — the callback runs in the agent
	// goroutine, sends permissionAskMsg to Bubble Tea, then blocks on the
	// reply channel until the user responds in the TUI.
	if opts.gate != nil {
		loop.SetAskPermission(func(ctx context.Context, toolName, toolInput string) (allow, alwaysAllow bool) {
			reply := make(chan permissionReply, 1)
			prog.Send(permissionAskMsg{
				toolName:  toolName,
				toolInput: toolInput,
				reply:     reply,
			})
			select {
			case r := <-reply:
				return r.allow, r.alwaysAllow
			case <-ctx.Done():
				return false, false
			}
		})
	}

	// Wire EnterPlanMode — asks user consent via the permission prompt machinery.
	if runOpts.EnterPlan != nil {
		runOpts.EnterPlan.CurrentMode = func() permissions.Mode {
			return live.PermissionMode()
		}
		runOpts.EnterPlan.AskEnter = func(ctx context.Context) bool {
			// Honour any session/persisted "Always allow" rule for EnterPlanMode
			// so we don't re-prompt every time. EnterPlanMode has no meaningful
			// input to scope on — the rule is keyed on the bare tool name.
			if opts.gate != nil && opts.gate.Check("EnterPlanMode", "") == permissions.DecisionAllow {
				return true
			}
			reply := make(chan permissionReply, 1)
			prog.Send(permissionAskMsg{
				toolName:  "EnterPlanMode",
				toolInput: "Enter plan mode? (read-only exploration and design phase)",
				reply:     reply,
			})
			select {
			case r := <-reply:
				if r.allow && r.alwaysAllow && opts.gate != nil {
					opts.gate.AllowForSession("EnterPlanMode")
					if cwd, err := os.Getwd(); err == nil && cwd != "" {
						_ = permissions.PersistAllow("EnterPlanMode", cwd)
					}
				}
				return r.allow
			case <-ctx.Done():
				return false
			}
		}
		runOpts.EnterPlan.SetMode = func(m permissions.Mode) {
			prog.Send(setPermissionModeMsg{mode: m})
		}
	}

	// Wire ExitPlanMode — presents plan-approval picker and waits for decision.
	if runOpts.ExitPlan != nil {
		runOpts.ExitPlan.AskApprove = func(ctx context.Context, plan string) planmodetool.PlanApprovalDecision {
			reply := make(chan planmodetool.PlanApprovalDecision, 1)
			prog.Send(planApprovalAskMsg{plan: plan, reply: reply})
			select {
			case d := <-reply:
				return d
			case <-ctx.Done():
				return planmodetool.PlanApprovalDecision{Approved: false, Feedback: "context cancelled"}
			}
		}
		runOpts.ExitPlan.SetMode = func(m permissions.Mode) {
			prog.Send(setPermissionModeMsg{mode: m})
		}
		runOpts.ExitPlan.IsCouncilMode = func() bool {
			return live.PermissionMode() == permissions.ModeCouncil
		}
		runOpts.ExitPlan.StartCouncil = func(ctx context.Context, plan string) planmodetool.PlanApprovalDecision {
			ch := make(chan planmodetool.PlanApprovalDecision, 1)
			prog.Send(councilStartMsg{
				seedPlan: plan,
				reply:    ch,
			})
			select {
			case d := <-ch:
				return d
			case <-ctx.Done():
				return planmodetool.PlanApprovalDecision{Approved: false, Feedback: "cancelled"}
			}
		}
	}

	// Wire EnterAutoMode — uses the same permission-prompt machinery.
	if runOpts.EnterAutoMode != nil {
		runOpts.EnterAutoMode.CurrentMode = func() permissions.Mode {
			return live.PermissionMode()
		}
		runOpts.EnterAutoMode.AskEnter = func(ctx context.Context) bool {
			// Honour any session/persisted "Always allow" rule for EnterAutoMode
			// so we don't re-prompt every time.
			if opts.gate != nil && opts.gate.Check("EnterAutoMode", "") == permissions.DecisionAllow {
				return true
			}
			reply := make(chan permissionReply, 1)
			prog.Send(permissionAskMsg{
				toolName:  "EnterAutoMode",
				toolInput: "Enter auto mode? Tool calls will proceed without per-call prompts.",
				reply:     reply,
			})
			select {
			case r := <-reply:
				if r.allow && r.alwaysAllow && opts.gate != nil {
					opts.gate.AllowForSession("EnterAutoMode")
					if cwd, err := os.Getwd(); err == nil && cwd != "" {
						_ = permissions.PersistAllow("EnterAutoMode", cwd)
					}
				}
				return r.allow
			case <-ctx.Done():
				return false
			}
		}
		runOpts.EnterAutoMode.SetMode = func(m permissions.Mode) {
			prog.Send(setPermissionModeMsg{mode: m})
		}
	}

	// Wire ExitAutoMode — no confirmation needed; just resets the mode.
	if runOpts.ExitAutoMode != nil {
		runOpts.ExitAutoMode.SetMode = func(m permissions.Mode) {
			prog.Send(setPermissionModeMsg{mode: m})
		}
	}

	// Wire AskUserQuestion — shows the question in chat and waits for the user
	// to type an answer in the normal input box (no permission dialog).
	if runOpts.AskUser != nil {
		runOpts.AskUser.Ask = func(ctx context.Context, question string, opts []askusertool.Option, multi bool) []string {
			reply := make(chan []string, 1)
			qopts := make([]questionOption, len(opts))
			for i, o := range opts {
				qopts[i] = questionOption{Label: o.Label, Value: o.Value, Description: o.Description}
			}
			prog.Send(questionAskMsg{
				question: question,
				options:  qopts,
				multi:    multi,
				reply:    reply,
			})
			select {
			case answers := <-reply:
				return answers
			case <-ctx.Done():
				return nil
			}
		}
	}

	// Wire diff-first review gate: set DiffReview.AskReview so mainrepl's
	// OnEndTurn can call it when staged edits are ready.
	if runOpts.PendingEdits != nil && runOpts.DiffReview != nil {
		pendingTable := runOpts.PendingEdits
		runOpts.DiffReview.AskReview = func(ctx context.Context) DiffReviewResult {
			reply := make(chan DiffReviewResult, 1)
			prog.Send(diffReviewAskMsg{
				entries: pendingTable.Drain(),
				reply:   reply,
			})
			select {
			case r := <-reply:
				return r
			case <-ctx.Done():
				// Cancelled — approve whatever is left so nothing is silently lost.
				return DiffReviewResult{Approved: pendingTable.Drain()}
			}
		}
		runOpts.DiffReview.EnqueueFollowup = func(text string) {
			prog.Send(diffReviewFollowupMsg{text: text})
		}
	}

	// Re-enter alt-screen after SIGWINCH (iTerm2 resize) so the terminal
	// doesn't leave ghost frames in the main buffer's scrollback.
	winch := make(chan os.Signal, 1)
	initTUIWinch(winch)

	// bubbletea v2 already registers its own SIGINT/SIGTERM handler that
	// sends InterruptMsg to the event loop. Adding a second signal.Notify
	// for the same signals causes double-firing and can interfere with
	// program shutdown. We rely on bubbletea's handler + the Update
	// InterruptMsg case instead.

	_, err := prog.Run()

	// Guarantee alt-screen is exited even if Bubble Tea's cleanup was partial.
	fmt.Fprint(os.Stdout, altScreenExit)

	signal.Stop(winch)
	close(winch)
	return err
}
