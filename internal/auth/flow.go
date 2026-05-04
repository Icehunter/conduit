package auth

import (
	"context"
	"fmt"
	"time"
)

// LoginFlow ties PKCE generation, the localhost callback listener, and the
// token exchange into one orchestrator. It is the function `claude login`
// invokes after deciding which authorization URL to open.
type LoginFlow struct {
	Cfg     Config
	Tokens  *TokenClient
	Browser BrowserOpener // optional; stdout-only when nil
	// Display is called with both the manual auth URL and (when not nil) the
	// automatic URL. It is responsible for showing them to the user. When
	// the manual flow is desired, Display can also block on user paste.
	Display LoginDisplay
}

// BrowserOpener opens an external URL in the user's web browser.
// Pass a nil opener for headless environments — the user copy-pastes.
type BrowserOpener interface {
	Open(url string) error
}

// LoginDisplay handles user-facing presentation of the OAuth URLs.
// AutomaticURL may be empty when only the manual flow is shown.
type LoginDisplay interface {
	Show(automaticURL, manualURL string)
	// BrowserOpenFailed is called with a non-nil error when the system
	// browser launcher returned an error. Implementations should display
	// a hint that the user must open the URL manually instead.
	BrowserOpenFailed(err error)
}

// LoginOptions controls flow variants.
type LoginOptions struct {
	// LoginWithClaudeAI selects the Claude.ai (Max/Pro/Team) authorize URL
	// rather than the Console URL. Set true for subscription users.
	LoginWithClaudeAI bool
	// InferenceOnly requests the long-lived `user:inference`-only scope set.
	InferenceOnly bool
	// OrgUUID, LoginHint, LoginMethod are optional pre-fill / routing hints.
	OrgUUID     string
	LoginHint   string
	LoginMethod string
	// SkipBrowserOpen suppresses BrowserOpener.Open and only shows URLs via
	// Display. Used by the SDK control protocol where the SDK client owns
	// the user's display.
	SkipBrowserOpen bool
	// Timeout bounds how long Login will wait for the user to authorize.
	// Zero defaults to 5 minutes.
	Timeout time.Duration
}

// Login runs the full PKCE OAuth flow against the configured endpoints.
//
// Steps:
//  1. Generate verifier + challenge + state.
//  2. Spin up a localhost callback listener on an OS-assigned port.
//  3. Build manual + automatic auth URLs and present them.
//  4. Open the automatic URL in the browser (unless suppressed).
//  5. Wait for the redirect (or manual paste) and exchange the code.
//  6. Send the success redirect to close out the user's browser tab.
//
// Returns the issued Tokens. The caller is responsible for persisting them.
func (f *LoginFlow) Login(ctx context.Context, opt LoginOptions) (Tokens, error) {
	if opt.Timeout <= 0 {
		opt.Timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()

	verifier, err := GenerateVerifier()
	if err != nil {
		return Tokens{}, err
	}
	state, err := GenerateState()
	if err != nil {
		return Tokens{}, err
	}
	challenge := S256Challenge(verifier)

	listener, err := NewCallbackListener("/callback")
	if err != nil {
		return Tokens{}, err
	}
	defer func() { _ = listener.Close() }()
	port := listener.Port()
	if err := listener.Register(state); err != nil {
		return Tokens{}, err
	}

	manualURL, err := BuildAuthURL(f.Cfg, BuildAuthURLParams{
		CodeChallenge:     challenge,
		State:             state,
		Port:              port,
		IsManual:          true,
		LoginWithClaudeAI: opt.LoginWithClaudeAI,
		InferenceOnly:     opt.InferenceOnly,
		OrgUUID:           opt.OrgUUID,
		LoginHint:         opt.LoginHint,
		LoginMethod:       opt.LoginMethod,
	})
	if err != nil {
		return Tokens{}, err
	}
	autoURL, err := BuildAuthURL(f.Cfg, BuildAuthURLParams{
		CodeChallenge:     challenge,
		State:             state,
		Port:              port,
		IsManual:          false,
		LoginWithClaudeAI: opt.LoginWithClaudeAI,
		InferenceOnly:     opt.InferenceOnly,
		OrgUUID:           opt.OrgUUID,
		LoginHint:         opt.LoginHint,
		LoginMethod:       opt.LoginMethod,
	})
	if err != nil {
		return Tokens{}, err
	}

	if f.Display != nil {
		f.Display.Show(autoURL, manualURL)
	}
	if !opt.SkipBrowserOpen && f.Browser != nil {
		if err := f.Browser.Open(autoURL); err != nil {
			// Don't abort: the manual paste flow is still available.
			// Surface the failure to the user via Display so they know
			// to copy the URL manually.
			if f.Display != nil {
				f.Display.BrowserOpenFailed(err)
			}
		}
	}

	code, err := listener.Wait(ctx, state)
	if err != nil {
		return Tokens{}, fmt.Errorf("auth: waiting for authorization: %w", err)
	}

	tok, err := f.Tokens.ExchangeCodeForTokens(ctx, ExchangeParams{
		Code:         code,
		State:        state,
		CodeVerifier: verifier,
		Port:         port,
		UseManual:    false,
	})
	if err != nil {
		listener.SendErrorRedirect(f.successURL(opt))
		return Tokens{}, err
	}

	listener.SendSuccessRedirect(f.successURL(opt))
	return tok, nil
}

func (f *LoginFlow) successURL(opt LoginOptions) string {
	if opt.LoginWithClaudeAI {
		return f.Cfg.ClaudeAISuccessURL
	}
	return f.Cfg.ConsoleSuccessURL
}
