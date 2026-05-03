package tui

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/memdir"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/profile"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/outputstyles"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tools/askusertool"
	"github.com/icehunter/conduit/internal/tools/planmodetool"
)

// SummarizeMessages renders the last n model-visible messages as a
// plain-text transcript for /memory extract. Tool blocks are flattened so
// the sub-agent sees a readable conversation, not raw JSON. Exported
// because cmd/conduit/main.go's auto-extract path also needs it.
func SummarizeMessages(history []api.Message, n int) string {
	if len(history) == 0 {
		return ""
	}
	start := 0
	if len(history) > n {
		start = len(history) - n
	}
	var sb strings.Builder
	for _, m := range history[start:] {
		role := strings.ToUpper(m.Role)
		sb.WriteString("---\n")
		sb.WriteString(role + ":\n")
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				sb.WriteString(b.Text)
				sb.WriteString("\n")
			case "tool_use":
				sb.WriteString(fmt.Sprintf("[tool_use %s]\n", b.Name))
			case "tool_result":
				txt := b.Text
				if len(txt) > 500 {
					txt = txt[:500] + "…"
				}
				sb.WriteString("[tool_result]\n")
				sb.WriteString(txt)
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}

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
	LoadAuth func(ctx context.Context) (string, *profile.Info, error)

	// NewAPIClient constructs a fresh API client for the given bearer token.
	NewAPIClient func(bearer string) *api.Client

	// Interactive tool stubs — the TUI wires their callbacks after startup.
	EnterPlan *planmodetool.EnterPlanMode
	ExitPlan  *planmodetool.ExitPlanMode
	AskUser   *askusertool.AskUserQuestion

	// InitialOutputStyle is the style name to activate at startup (from settings).
	InitialOutputStyle string

	// PluginDirs is the list of installed plugin root directories, used to
	// load plugin-provided output styles (lowest priority — overridden by user/project).
	PluginDirs []string
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

	reg := commands.New()
	commands.RegisterBuiltins(reg)
	commands.RegisterModelCommand(reg,
		func() string { return internalmodel.Resolve() },
		func(name string) { loop.SetModel(name) },
	)
	commands.RegisterCompactCommand(reg)
	commands.RegisterPermissionsCommand(reg, opts.gate)
	commands.RegisterHooksCommand(reg, opts.hooksConfig)
	commands.RegisterMiscCommands(reg)
	commands.RegisterTerminalSetupCommand(reg)
	commands.RegisterPromptCommands(reg)
	commands.RegisterMCPCommand(reg, runOpts.MCPManager)
	commands.RegisterRTKCommands(reg)
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
	commands.RegisterBundledSkillCommands(reg)
	commands.RegisterPluginBrowserCommand(reg, loadedPlugins)
	commands.RegisterSkillsCommand(reg, loadedPlugins)

	sessionStart := time.Now()

	// Session state shared between commands and the TUI model.
	// live is a thread-safe bag updated by Model.syncLive() on every relevant
	// state change; command callbacks read from it so they see current values.
	live := &LiveState{}
	live.SetModelName(modelName) // seed with startup value

	// modelPtr is still used for methods that can only run inside the event loop
	// (TasksSummary, LastThinking, CopyLastResponse — they read m.messages).
	var modelPtr *Model
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
		GetStatus: func() string {
			tokens, cost := live.Tokens()
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
				pct = tokens * 100 / 200000
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
			sb.WriteString(fmt.Sprintf("Context: %d%% (%d tokens)\n", pct, tokens))
			if cost > 0 {
				sb.WriteString(fmt.Sprintf("Cost:    $%.4f\n", cost))
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
			tokens, cost := live.Tokens()
			// Output tokens not tracked separately in LiveState yet — return 0.
			return tokens, 0, cost
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
		RenameSession: func(title string) error {
			if runOpts.Session == nil {
				return fmt.Errorf("no active session")
			}
			return runOpts.Session.SetTitle(title)
		},
		// /tag — persist a tag to the session JSONL ("" clears).
		TagSession: func(tag string) error {
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
			if err := memdir.RunExtract(ctx, cwd, recent, loop.RunSubAgent); err != nil {
				return "", err
			}
			return "Memory extraction complete.", nil
		},
	}
	commands.RegisterSessionCommands(reg, state)

	var apiClient *api.Client = opts.apiClient

	cfg := Config{
		Version:        version,
		ModelName:      modelName,
		Loop:           loop,
		Program:        &prog,
		Commands:       reg,
		APIClient:      apiClient,
		MCPManager:     runOpts.MCPManager,
		Gate:           opts.gate,
		AuthErr:        runOpts.AuthErr,
		Profile:        runOpts.Profile,
		Session:        runOpts.Session,
		ResumedHistory: runOpts.ResumedHistory,
		Resumed:        runOpts.Resumed,
		LoadAuth:       runOpts.LoadAuth,
		NewAPIClient:   runOpts.NewAPIClient,
		Live:           live,
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
				baseBlocks := agent.BuildSystemBlocks(mem, "")
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
		runOpts.EnterPlan.AskEnter = func(ctx context.Context) bool {
			reply := make(chan permissionReply, 1)
			prog.Send(permissionAskMsg{
				toolName:  "EnterPlanMode",
				toolInput: "Enter plan mode? (read-only exploration and design phase)",
				reply:     reply,
			})
			select {
			case r := <-reply:
				return r.allow
			case <-ctx.Done():
				return false
			}
		}
		runOpts.EnterPlan.SetMode = func(m permissions.Mode) {
			prog.Send(setPermissionModeMsg{mode: m})
		}
	}

	// Wire ExitPlanMode — presents plan and asks for approval.
	if runOpts.ExitPlan != nil {
		runOpts.ExitPlan.AskApprove = func(ctx context.Context, plan string) bool {
			reply := make(chan permissionReply, 1)
			prog.Send(permissionAskMsg{
				toolName:  "ExitPlanMode",
				toolInput: "Approve this implementation plan?\n\n" + plan,
				reply:     reply,
			})
			select {
			case r := <-reply:
				return r.allow
			case <-ctx.Done():
				return false
			}
		}
		runOpts.ExitPlan.SetMode = func(m permissions.Mode) {
			prog.Send(setPermissionModeMsg{mode: m})
		}
	}

	// Wire AskUserQuestion — uses the permission prompt as a simple yes/no for now.
	// Full multi-choice UI is M-D (TUI polish milestone).
	if runOpts.AskUser != nil {
		runOpts.AskUser.Ask = func(ctx context.Context, question string, opts []askusertool.Option, multi bool) []string {
			reply := make(chan permissionReply, 1)
			// Format options inline in the prompt text.
			prompt := question
			if len(opts) > 0 {
				prompt += "\n\nOptions:"
				for i, o := range opts {
					prompt += "\n" + itoa(i+1) + ". " + o.Label
					if o.Description != "" {
						prompt += " — " + o.Description
					}
				}
			}
			prog.Send(permissionAskMsg{
				toolName:  "AskUserQuestion",
				toolInput: prompt,
				reply:     reply,
			})
			select {
			case r := <-reply:
				if r.allow {
					return []string{"yes"}
				}
				return nil
			case <-ctx.Done():
				return nil
			}
		}
	}

	// Re-enter alt-screen after SIGWINCH (iTerm2 resize) so the terminal
	// doesn't leave ghost frames in the main buffer's scrollback.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			fmt.Fprint(os.Stdout, clearScreen)
		}
	}()

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

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// logoutCredentials clears the OAuth token bundle from secure storage.
// Cross-platform: uses the same secure store the auth flow writes to,
// so it works on macOS (Keychain), Linux (libsecret/file fallback),
// and Windows (WinCred/file fallback).
func logoutCredentials() error {
	store, err := defaultSecureStorage()
	if err != nil {
		return err
	}
	return auth.Delete(store)
}

// defaultSecureStorage returns the file-backed store at the canonical
// path. Mirrors the construction in cmd/conduit/main.go's loadAuth so
// /logout writes to the same place login reads from.
func defaultSecureStorage() (secure.Storage, error) {
	path, err := secure.DefaultFilePath()
	if err != nil {
		return nil, err
	}
	return secure.NewFileStorage(path), nil
}
