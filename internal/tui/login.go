package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/profile"
	"github.com/icehunter/conduit/internal/secure"
)

// runLoginFlow executes the OAuth PKCE flow. The display callback is called
// with the OAuth URLs so the TUI can render them inline.
func runLoginFlow(claudeAI bool, display auth.LoginDisplay) error {
	authCfg := auth.ProdConfig
	tc := auth.NewTokenClient(authCfg, nil)
	flow := &auth.LoginFlow{
		Cfg:     authCfg,
		Tokens:  tc,
		Browser: auth.SystemBrowser{},
		Display: display,
	}

	ctx := context.Background()
	tok, err := flow.Login(ctx, auth.LoginOptions{
		LoginWithClaudeAI: claudeAI,
		Timeout:           5 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("oauth flow: %w", err)
	}

	// CreateAPIKey may return 403 for Max/Pro subscribers — that's fine,
	// the access token itself works for inference. Suppress the warning.
	apiKey, _ := tc.CreateAPIKey(ctx, tok.AccessToken)

	store := secure.NewDefault()
	persisted := auth.FromTokens(tok, time.Now())
	persisted.APIKey = apiKey

	// Prefer email from the token response (account.email_address); fall back
	// to the /api/oauth/profile endpoint (account.email) when the token omits it;
	// use a stable placeholder as last resort so login never hard-fails on email.
	email := tok.Email
	if email == "" {
		prof, _ := profile.Fetch(ctx, tok.AccessToken)
		email = prof.Email
	}
	if email == "" {
		email = "default"
	}
	if err := auth.SaveForEmail(store, persisted, email); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	return nil
}
