// Package main is the conduit entrypoint.
//
// Surface:
//
//	claude                      Full-screen Bubble Tea TUI.
//	claude --print "prompt"     One-shot streaming response.
//	claude version              Print binary version.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/claudemd"
	"github.com/icehunter/conduit/internal/memdir"
	"github.com/icehunter/conduit/internal/mcp"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/profile"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/tools/bashtool"
	"github.com/icehunter/conduit/internal/tools/fileedittool"
	"github.com/icehunter/conduit/internal/tools/filereadtool"
	"github.com/icehunter/conduit/internal/tools/filewritetool"
	"github.com/icehunter/conduit/internal/tools/globtool"
	"github.com/icehunter/conduit/internal/tools/greptool"
	"github.com/icehunter/conduit/internal/tools/mcptool"
	"github.com/icehunter/conduit/internal/tools/notebookedittool"
	"github.com/icehunter/conduit/internal/tools/repltool"
	"github.com/icehunter/conduit/internal/tools/sleeptool"
	"github.com/icehunter/conduit/internal/tools/tasktool"
	"github.com/icehunter/conduit/internal/tools/todowritetool"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/tools/agenttool"
	"github.com/icehunter/conduit/internal/tools/skilltool"
	"github.com/icehunter/conduit/internal/tools/toolsearchtool"
	"github.com/icehunter/conduit/internal/tools/webfetchtool"
	"github.com/icehunter/conduit/internal/tools/websearchtool"
	"github.com/icehunter/conduit/internal/tui"
)

// Version is the wire version we identify as. We match the exact value the
// official binary v2.1.126 sends in `User-Agent`/`X-App` fingerprints —
// Anthropic's API rate-limits clients whose UA doesn't look like the CLI.
//
// Override at build time via -ldflags "-X main.Version=...". Override at
// runtime via the CLAUDE_GO_REPORT_VERSION env var if you ever need to lie
// in a different direction.
var Version = "2.1.126"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "claude:", err)
		os.Exit(1)
	}
}

func run() error {
	var printMode bool
	var continueMode bool
	flag.BoolVar(&printMode, "print", false, "non-interactive: send a one-shot prompt and print the response")
	flag.BoolVar(&printMode, "p", false, "alias for --print")
	flag.BoolVar(&continueMode, "continue", false, "resume the most recent conversation for the current directory")
	flag.BoolVar(&continueMode, "c", false, "alias for --continue")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: claude [version] | claude --print \"prompt\" | claude [--continue] (REPL)")
		fmt.Fprintln(os.Stderr, "       Login and logout are managed via /login and /logout inside the REPL.")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if printMode {
		return runPrint(args)
	}
	if len(args) == 0 {
		return runREPL(continueMode)
	}

	switch args[0] {
	case "version":
		fmt.Println(Version)
		return nil
	default:
		flag.Usage()
		return fmt.Errorf("unknown subcommand: %s", args[0])
	}
}

// newAPIClient builds a configured API client using the persisted token.
func newAPIClient(bearer string) *api.Client {
	entrypoint := os.Getenv("CLAUDE_CODE_ENTRYPOINT")
	if entrypoint == "" {
		entrypoint = "sdk-cli"
	}
	ua := fmt.Sprintf("claude-cli/%s (external, %s)", Version, entrypoint)
	return api.NewClient(api.Config{
		BaseURL:   auth.ProdConfig.BaseAPIURL,
		AuthToken: bearer,
		BetaHeaders: []string{
			"claude-code-20250219",
			"oauth-2025-04-20",
			"interleaved-thinking-2025-05-14",
			"context-management-2025-06-27",
			"prompt-caching-scope-2026-01-05",
			"advisor-tool-2026-03-01",
			"advanced-tool-use-2025-11-20",
			"effort-2025-11-24",
			"cache-diagnosis-2026-04-07",
		},
		SessionID: newSessionID(),
		UserAgent: ua,
		ExtraHeaders: map[string]string{
			"anthropic-dangerous-direct-browser-access": "true",
			"X-Stainless-Retry-Count":                   "0",
			"X-Stainless-Timeout":                       "600",
		},
	}, nil)
}

// loadAuth loads and refreshes tokens from the credential store.
// Returns an empty PersistedTokens and non-nil error when no credentials exist.
func loadAuth(ctx context.Context) (auth.PersistedTokens, error) {
	store := secure.NewDefault()
	cfg := auth.ProdConfig
	tc := auth.NewTokenClient(cfg, nil)
	return auth.EnsureFresh(ctx, store, tc, time.Now(), 5*time.Minute)
}

// buildSkillEntries converts loaded plugin commands into SkillEntry values for
// the system prompt skill listing.
func buildSkillEntries(ps []*plugins.Plugin) []agent.SkillEntry {
	var entries []agent.SkillEntry
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

// buildRegistry builds the tool registry, including MCP server tools.
func buildRegistry(client *api.Client, mcpManager *mcp.Manager) *tool.Registry {
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
	// Register MCP server tools (if any servers are configured).
	if mcpManager != nil {
		mcptool.RegisterAll(reg, mcpManager)
	}
	return reg
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
func runREPL(continueMode bool) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Try auth — failure is not fatal here. The TUI handles the no-auth state.
	tok, authErr := loadAuth(ctx)
	bearer := tok.APIKey
	if bearer == "" {
		bearer = tok.AccessToken
	}

	// Fetch profile info in the background; non-fatal if unavailable.
	var prof profile.Info
	if authErr == nil && tok.AccessToken != "" {
		prof, _ = profile.Fetch(ctx, tok.AccessToken)
	}

	// Session persistence — create or resume.
	cwd, _ := os.Getwd()
	sessionID := newSessionID()
	var resumedHistory []api.Message

	if continueMode {
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

	// Load settings (missing/invalid files are fine — defaults apply).
	s, _ := settings.Load(cwd)
	if s == nil {
		s = &settings.Merged{DefaultMode: "default"}
	}

	gate := permissions.New(permissions.Mode(s.DefaultMode), s.Allow, s.Deny, s.Ask)

	// Connect MCP servers in the background; non-fatal if config missing or servers fail.
	mcpManager := mcp.NewManager()
	_ = mcpManager.ConnectAll(ctx, cwd)

	// Load plugins (non-fatal — missing plugins don't block startup).
	loadedPlugins, _ := plugins.LoadAll(cwd)

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

	c := newAPIClient(bearer)
	reg := buildRegistry(c, mcpManager)
	modelName := internalmodel.Resolve()

	lp := agent.NewLoop(c, reg, agent.LoopConfig{
		Model:     modelName,
		MaxTokens: internalmodel.MaxTokens,
		System:    agent.BuildSystemBlocks(mem, claudeMdPrompt, skillEntries...),
		MaxTurns:  50,
		Gate:      gate,
		Hooks:     &s.Hooks,
		SessionID: sessionID,
	})

	// Register AgentTool and SkillTool now that the loop exists.
	reg.Register(agenttool.New(lp.RunSubAgent))
	skillLoader := plugins.NewSkillLoader(loadedPlugins)
	reg.Register(skilltool.New(skillLoader, lp.RunSubAgent))

	tuiErr := tui.Run(Version, modelName, lp, c, gate, &s.Hooks, tui.RunOptions{
		AuthErr:         authErr,
		Profile:         prof,
		Session:         sess,
		ResumedHistory:  resumedHistory,
		Resumed:         continueMode && len(resumedHistory) > 0,
		MCPManager:      mcpManager,
		LoadAuth: func(ctx context.Context) (string, *profile.Info, error) {
			tok, err := loadAuth(ctx)
			if err != nil {
				return "", nil, err
			}
			bearer := tok.APIKey
			if bearer == "" {
				bearer = tok.AccessToken
			}
			p, _ := profile.Fetch(ctx, tok.AccessToken)
			return bearer, &p, nil
		},
		NewAPIClient: func(bearer string) *api.Client {
			return newAPIClient(bearer)
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
			_ = memdir.RunDream(dreamCtx, cwd, sessionDir, lp.RunSubAgent)
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

	bearer := p.APIKey
	if bearer == "" {
		bearer = p.AccessToken
	}

	cwd, _ := os.Getwd()
	loadedPlugins, _ := plugins.LoadAll(cwd)
	skillEntries := buildSkillEntries(loadedPlugins)
	_ = memdir.EnsureDir(cwd)
	mem := memdir.BuildPrompt(cwd)
	claudeMdFiles, _ := claudemd.Load(cwd)
	claudeMdPrompt := claudemd.BuildPrompt(claudeMdFiles)
	c := newAPIClient(bearer)
	reg := buildRegistry(c, nil)
	modelName := internalmodel.Resolve()

	lp := agent.NewLoop(c, reg, agent.LoopConfig{
		Model:     modelName,
		MaxTokens: internalmodel.MaxTokens,
		System:    agent.BuildSystemBlocks(mem, claudeMdPrompt, skillEntries...),
		Metadata:  buildMetadata(),
		MaxTurns:  10,
	})
	reg.Register(agenttool.New(lp.RunSubAgent))
	reg.Register(skilltool.New(plugins.NewSkillLoader(loadedPlugins), lp.RunSubAgent))

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

// keep json import used (for ContentBlock marshaling in history tracking)
var _ = json.Marshal
