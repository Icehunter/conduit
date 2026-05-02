package tui

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/mcp"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/profile"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
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
	LoadAuth func(ctx context.Context) (string, *profile.Info, error)

	// NewAPIClient constructs a fresh API client for the given bearer token.
	NewAPIClient func(bearer string) *api.Client
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
	commands.RegisterPromptCommands(reg)
	commands.RegisterMCPCommand(reg, runOpts.MCPManager)

	// Load plugins and register their slash commands + browser.
	cwd, _ := os.Getwd()
	var loadedPlugins []*plugins.Plugin
	if ps, err := plugins.LoadAll(cwd); err == nil {
		loadedPlugins = ps
	}
	commands.RegisterPluginCommands(reg, loadedPlugins)
	commands.RegisterPluginBrowserCommand(reg, loadedPlugins)
	commands.RegisterSkillsCommand(reg, loadedPlugins)

	// Session state shared between commands and the TUI model.
	// The model pointer is set after New() — use a pointer-to-pointer so
	// closures always see the live model.
	var modelPtr *Model
	state := &commands.SessionState{
		GetCost: func() string {
			if modelPtr == nil {
				return "No session data."
			}
			return modelPtr.CostSummary()
		},
		Logout: func() error {
			// Perform logout by deleting the credentials from the keychain.
			return logoutCredentials()
		},
		GetCwd: func() string {
			cwd, _ := os.Getwd()
			return cwd
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
	}

	m := New(cfg)
	modelPtr = &m
	prog = tea.NewProgram(
		m,
		tea.WithAltScreen(),
	)

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

	// Re-enter alt-screen after SIGWINCH (iTerm2 resize) so the terminal
	// doesn't leave ghost frames in the main buffer's scrollback.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			fmt.Fprint(os.Stdout, clearScreen)
		}
	}()

	// Clean exit on interrupt/term.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigs
		prog.Kill()
	}()

	_, err := prog.Run()

	// Guarantee alt-screen is exited even if Bubble Tea's cleanup was partial.
	fmt.Fprint(os.Stdout, altScreenExit)

	signal.Stop(winch)
	signal.Stop(sigs)
	close(winch)
	return err
}

// logoutCredentials deletes the stored credentials from the keychain.
func logoutCredentials() error {
	home, _ := os.UserHomeDir()
	credPath := home + "/Library/Application Support/claude-code/credentials.json"
	return os.Remove(credPath)
}
