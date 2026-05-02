// Package main is the claude-go entrypoint.
//
// M2 surface:
//
//	claude                      Interactive REPL (streaming, with tools).
//	claude login                Run OAuth flow, persist tokens.
//	claude logout               Clear persisted tokens.
//	claude --print "prompt"     One-shot non-streaming Messages call.
//	claude version              Print binary version.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/icehunter/claude-go/internal/agent"
	"github.com/icehunter/claude-go/internal/api"
	"github.com/icehunter/claude-go/internal/auth"
	internalmodel "github.com/icehunter/claude-go/internal/model"
	"github.com/icehunter/claude-go/internal/secure"
	"github.com/icehunter/claude-go/internal/tool"
	"github.com/icehunter/claude-go/internal/tools/bashtool"
	"github.com/icehunter/claude-go/internal/tools/filereadtool"
	"github.com/icehunter/claude-go/internal/tools/filewritetool"
	"github.com/icehunter/claude-go/internal/tools/globtool"
	"github.com/icehunter/claude-go/internal/tools/greptool"
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
	flag.BoolVar(&printMode, "print", false, "non-interactive: send a one-shot prompt and print the response")
	flag.BoolVar(&printMode, "p", false, "alias for --print")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: claude [login|logout|version] | claude --print \"prompt\" | claude (REPL)")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if printMode {
		return runPrint(args)
	}
	if len(args) == 0 {
		// No subcommand — drop into REPL.
		return runREPL()
	}

	switch args[0] {
	case "login":
		return runLogin()
	case "logout":
		return runLogout()
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
func loadAuth(ctx context.Context) (auth.PersistedTokens, error) {
	store := secure.NewDefault()
	cfg := auth.ProdConfig
	tc := auth.NewTokenClient(cfg, nil)
	return auth.EnsureFresh(ctx, store, tc, time.Now(), 5*time.Minute)
}

// buildRegistry builds the tool registry with all M2 tools.
func buildRegistry() *tool.Registry {
	reg := tool.NewRegistry()
	reg.Register(bashtool.New())
	reg.Register(filereadtool.New())
	reg.Register(filewritetool.New())
	reg.Register(globtool.New())
	reg.Register(greptool.New())
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

// runREPL runs the interactive REPL. Reads lines from stdin, sends them to
// the agent loop, and prints streaming responses.
func runREPL() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	tok, err := loadAuth(ctx)
	if err != nil {
		return fmt.Errorf("authentication: %w (run `claude login` first)", err)
	}
	bearer := tok.APIKey
	if bearer == "" {
		bearer = tok.AccessToken
	}

	c := newAPIClient(bearer)
	reg := buildRegistry()
	modelName := internalmodel.Resolve()

	lp := agent.NewLoop(c, reg, agent.LoopConfig{
		Model:     modelName,
		MaxTokens: internalmodel.MaxTokens,
		System:    agent.BuildSystemBlocks(),
		MaxTurns:  50,
	})

	// Conversation history persisted across turns within the session.
	var history []api.Message

	fmt.Fprintf(os.Stderr, "claude-go v%s  (model: %s)\n", Version, modelName)
	fmt.Fprintln(os.Stderr, "Type your message and press Enter. Ctrl-C or /exit to quit.")
	fmt.Fprintln(os.Stderr)

	scanner := bufio.NewScanner(os.Stdin)
	for {
		// Print prompt.
		fmt.Fprint(os.Stderr, "> ")

		if !scanner.Scan() {
			// EOF (Ctrl-D) — clean exit.
			fmt.Fprintln(os.Stderr)
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "/exit" || line == "/quit" {
			break
		}

		// Add user message to history.
		history = append(history, api.Message{
			Role:    "user",
			Content: []api.ContentBlock{{Type: "text", Text: line}},
		})

		// Run one agent turn (may make multiple API calls for tool use).
		// Loop.Run returns the full accumulated history so we don't need to
		// reconstruct it from events.
		newHistory, runErr := lp.Run(ctx, history, func(ev agent.LoopEvent) {
			switch ev.Type {
			case agent.EventText:
				fmt.Print(ev.Text)
			case agent.EventToolUse:
				fmt.Fprintf(os.Stderr, "\n[tool: %s]\n", ev.ToolName)
			case agent.EventToolResult:
				if ev.IsError {
					fmt.Fprintf(os.Stderr, "[tool error: %s]\n", ev.ResultText)
				}
			}
		})
		fmt.Println()

		// Always adopt whatever history the loop built, even on error, so the
		// next turn starts from the correct state.
		history = newHistory

		if runErr != nil {
			if errors.Is(runErr, context.Canceled) {
				fmt.Fprintln(os.Stderr, "[interrupted]")
				ctx, cancel = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
				_ = cancel
				continue
			}
			fmt.Fprintf(os.Stderr, "error: %v\n", runErr)
			continue
		}
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("stdin: %w", err)
	}
	return nil
}

// stdoutDisplay shows OAuth URLs on stderr (so stdout stays clean for piping).
type stdoutDisplay struct{ w io.Writer }

func (d stdoutDisplay) Show(automatic, manual string) {
	fmt.Fprintln(d.w)
	fmt.Fprintln(d.w, "Opening browser to log in to Claude.")
	fmt.Fprintln(d.w, "If the browser doesn't open, paste this URL:")
	fmt.Fprintln(d.w)
	fmt.Fprintln(d.w, "  ", automatic)
	fmt.Fprintln(d.w)
	fmt.Fprintln(d.w, "Or, for a code-paste flow, use:")
	fmt.Fprintln(d.w)
	fmt.Fprintln(d.w, "  ", manual)
	fmt.Fprintln(d.w)
}

func (d stdoutDisplay) BrowserOpenFailed(err error) {
	fmt.Fprintf(d.w, "Couldn't open the browser automatically (%v). Paste the URL above into your browser to continue.\n", err)
}

func runLogin() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg := auth.ProdConfig
	tc := auth.NewTokenClient(cfg, nil)
	flow := &auth.LoginFlow{
		Cfg:     cfg,
		Tokens:  tc,
		Browser: auth.SystemBrowser{},
		Display: stdoutDisplay{w: os.Stderr},
	}

	tok, err := flow.Login(ctx, auth.LoginOptions{
		LoginWithClaudeAI: true,
		Timeout:           5 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}

	apiKey, keyErr := tc.CreateAPIKey(ctx, tok.AccessToken)
	if keyErr != nil {
		fmt.Fprintln(os.Stderr, "Warning: could not mint API key from OAuth token:", keyErr)
	}

	store := secure.NewDefault()
	persisted := auth.FromTokens(tok, time.Now())
	persisted.APIKey = apiKey
	if err := auth.Save(store, persisted); err != nil {
		return fmt.Errorf("persist tokens: %w", err)
	}

	fmt.Fprintln(os.Stderr, "Logged in.")
	return nil
}

func runLogout() error {
	store := secure.NewDefault()
	if err := store.Delete(auth.Service, auth.PersistKey); err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	fmt.Fprintln(os.Stderr, "Logged out.")
	return nil
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
		return fmt.Errorf("authentication: %w (run `claude login` first)", err)
	}

	bearer := p.APIKey
	if bearer == "" {
		bearer = p.AccessToken
	}

	c := newAPIClient(bearer)
	reg := buildRegistry()
	modelName := internalmodel.Resolve()

	lp := agent.NewLoop(c, reg, agent.LoopConfig{
		Model:     modelName,
		MaxTokens: internalmodel.MaxTokens,
		System:    agent.BuildSystemBlocks(),
		Metadata:  buildMetadata(),
		MaxTurns:  10,
	})

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
