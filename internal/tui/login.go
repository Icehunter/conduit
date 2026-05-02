package tui

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/icehunter/claude-go/internal/auth"
	"github.com/icehunter/claude-go/internal/secure"
)

// runLoginFlow executes the OAuth PKCE flow. It prints URLs to stderr
// (the alt-screen is still active but the terminal will handle it).
// claudeAI=true uses claude.ai (Max/Pro/Team); false uses the Anthropic Console.
func runLoginFlow(claudeAI bool, _ Config) error {
	authCfg := auth.ProdConfig
	tc := auth.NewTokenClient(authCfg, nil)
	flow := &auth.LoginFlow{
		Cfg:     authCfg,
		Tokens:  tc,
		Browser: auth.SystemBrowser{},
		Display: stderrDisplay{},
	}

	ctx := context.Background()
	tok, err := flow.Login(ctx, auth.LoginOptions{
		LoginWithClaudeAI: claudeAI,
		Timeout:           5 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("oauth flow: %w", err)
	}

	apiKey, keyErr := tc.CreateAPIKey(ctx, tok.AccessToken)
	if keyErr != nil {
		fmt.Fprintln(os.Stderr, "Warning: could not mint API key:", keyErr)
	}

	store := secure.NewDefault()
	persisted := auth.FromTokens(tok, time.Now())
	persisted.APIKey = apiKey
	if err := auth.Save(store, persisted); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	return nil
}

type stderrDisplay struct{}

func (stderrDisplay) Show(automatic, manual string) {
	fmt.Fprintln(os.Stderr, "Opening browser to sign in.")
	fmt.Fprintln(os.Stderr, "If the browser doesn't open, paste this URL:")
	fmt.Fprintln(os.Stderr, "  ", automatic)
	fmt.Fprintln(os.Stderr, "Or use the code-paste flow:")
	fmt.Fprintln(os.Stderr, "  ", manual)
}

func (stderrDisplay) BrowserOpenFailed(err error) {
	fmt.Fprintf(os.Stderr, "Couldn't open browser (%v). Paste the URL above.\n", err)
}
