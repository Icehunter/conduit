package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/icehunter/conduit/internal/secure"
)

// Service is the keyring service identifier for conduit.
const Service = "com.icehunter.conduit"

// PersistKey is the base keychain item name prefix for OAuth tokens.
// Per-account entries use "oauth-tokens-<email>".
const PersistKey = "oauth-tokens"

// PersistedTokens is the stored token shape: network tokens plus a
// pre-computed absolute expiry so we don't call the server just to check.
type PersistedTokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	TokenType    string    `json:"token_type"`
	Scopes       []string  `json:"scopes"`
	APIKey       string    `json:"api_key,omitempty"`
}

func (PersistedTokens) String() string { return "<redacted oauth tokens>" }

// FromTokens converts network Tokens to PersistedTokens with an absolute expiry.
func FromTokens(tok Tokens, now time.Time) PersistedTokens {
	return PersistedTokens{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    now.Add(time.Duration(tok.ExpiresIn) * time.Second),
		TokenType:    tok.TokenType,
		Scopes:       tok.Scopes,
	}
}

// ErrNotLoggedIn is returned when no token exists for the requested account.
var ErrNotLoggedIn = errors.New("auth: not logged in")

// EnsureFresh loads the token for email and refreshes it if within skew of
// expiry. Saves the refreshed token back to the keychain on success.
func EnsureFresh(ctx context.Context, s secure.Storage, c *TokenClient, email string, now time.Time, skew time.Duration) (PersistedTokens, error) {
	p, err := LoadForEmail(s, email)
	if err != nil {
		if errors.Is(err, secure.ErrNotFound) {
			return PersistedTokens{}, ErrNotLoggedIn
		}
		return PersistedTokens{}, err
	}
	if p.RefreshToken == "" || now.Add(skew).Before(p.ExpiresAt) {
		return p, nil
	}
	tok, err := c.RefreshOAuthToken(ctx, p.RefreshToken, RefreshOptions{Scopes: p.Scopes})
	if err != nil {
		return PersistedTokens{}, fmt.Errorf("auth: refresh tokens: %w", err)
	}
	fresh := FromTokens(tok, now)
	if fresh.RefreshToken == "" {
		fresh.RefreshToken = p.RefreshToken
	}
	// Only write the keychain entry; accounts.json was already set up at login.
	if err := saveToken(s, fresh, email); err != nil {
		return PersistedTokens{}, err
	}
	return fresh, nil
}
