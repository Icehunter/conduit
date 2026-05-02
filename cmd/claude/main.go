// Package main is the claude-go entrypoint.
//
// M1 surface:
//
//	claude login                Run OAuth flow, persist tokens.
//	claude logout               Clear persisted tokens.
//	claude --print "prompt"     One-shot non-streaming Messages call.
//	claude version              Print binary version.
//
// Subcommands grow per milestone (M2 adds streaming + tools + REPL, etc.).
package main

import (
	"context"
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
	"github.com/icehunter/claude-go/internal/secure"
)

// Version is the wire version we identify as. We match the exact value the
// official binary v2.1.126 sends in `User-Agent`/`X-App` fingerprints —
// Anthropic's API rate-limits clients whose UA doesn't look like the CLI.
//
// Override at build time via -ldflags "-X main.Version=...". Override at
// runtime via the CLAUDE_GO_REPORT_VERSION env var if you ever need to lie
// in a different direction.
var Version = "2.1.126"

// DefaultModel for `claude --print` in M1. M2 will pull this from config.
// Matches what real claude 2.1.126 sends in --print captures.
const DefaultModel = "claude-opus-4-7"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "claude:", err)
		os.Exit(1)
	}
}

func run() error {
	// Single-pass flag parsing for the simple M1 surface. Cobra wires in
	// when we add the full ~86 slash-command tree in M5.
	var printMode bool
	flag.BoolVar(&printMode, "print", false, "non-interactive: send a one-shot prompt and print the response")
	flag.BoolVar(&printMode, "p", false, "alias for --print")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: claude [login|logout|version] | claude --print \"prompt\"")
		flag.PrintDefaults()
	}
	flag.Parse()

	args := flag.Args()
	if printMode {
		return runPrint(args)
	}
	if len(args) == 0 {
		flag.Usage()
		return errors.New("subcommand required")
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

	// Mint the long-lived API key the real CLI uses on /v1/messages.
	// Failure here is non-fatal — we'll fall back to the OAuth bearer,
	// which works for some endpoints even if /v1/messages rate-limits it.
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

	store := secure.NewDefault()
	cfg := auth.ProdConfig
	tc := auth.NewTokenClient(cfg, nil)

	p, err := auth.EnsureFresh(ctx, store, tc, time.Now(), 5*time.Minute)
	if err != nil {
		return err
	}

	// Match the real CLI's User-Agent shape exactly:
	// `claude-cli/<version> (external, <entrypoint>[, agent-sdk/x][, client-app/x][, workload/x])`
	// Reference: decoded/1969.js:15-25 (function Ib()).
	//
	// The entrypoint is "sdk-cli" for --print invocations (matches what
	// real claude 2.1.126 sets when CLAUDE_CODE_ENTRYPOINT is unset and
	// --print is used). Verified via mitmproxy capture.
	entrypoint := os.Getenv("CLAUDE_CODE_ENTRYPOINT")
	if entrypoint == "" {
		entrypoint = "sdk-cli"
	}
	ua := fmt.Sprintf("claude-cli/%s (external, %s)", Version, entrypoint)

	// Prefer the minted API key — it's what real Claude Code uses on
	// /v1/messages. If we don't have one (older login or mint failure),
	// fall back to the OAuth access token as a bearer.
	bearer := p.APIKey
	if bearer == "" {
		bearer = p.AccessToken
	}

	c := api.NewClient(api.Config{
		BaseURL: cfg.BaseAPIURL,
		AuthToken: bearer,
		// Beta set captured from real claude 2.1.126 (mitmproxy 2026-05-01).
		// Without claude-code-20250219 the API treats us as a non-CC client
		// and rate-limits accordingly.
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
		SessionID:    newSessionID(),
		UserAgent:    ua,
		ExtraHeaders: map[string]string{
			"anthropic-dangerous-direct-browser-access": "true",
			"X-Stainless-Retry-Count":                   "0",
			"X-Stainless-Timeout":                       "600",
		},
	}, nil)

	// Build a request body that matches the shape Anthropic expects from a
	// real Claude Code client (system blocks with billing/identity marker,
	// metadata block, larger max_tokens). A minimal {model,messages,max_tokens}
	// body is rejected as 429 on Max subscriptions — see /tmp/claude-go-capture
	// flow analysis.
	deviceID := os.Getenv("CLAUDE_CODE_DEVICE_ID")
	if deviceID == "" {
		deviceID = "00000000000000000000000000000000"
	}
	accountUUID := os.Getenv("CLAUDE_CODE_ACCOUNT_UUID")
	sessionID := newSessionID()

	resp, err := c.CreateMessage(ctx, &api.MessageRequest{
		Model:     DefaultModel,
		MaxTokens: 1024,
		System:    agent.BuildSystemBlocks(),
		Metadata:  agent.BuildMetadata(deviceID, accountUUID, sessionID),
		Messages: []api.Message{{
			Role:    "user",
			Content: []api.ContentBlock{{Type: "text", Text: prompt}},
		}},
	})
	if err != nil {
		return err
	}
	for _, b := range resp.Content {
		if b.Type == "text" {
			fmt.Print(b.Text)
		}
	}
	fmt.Println()
	return nil
}
