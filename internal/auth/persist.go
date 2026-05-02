package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/icehunter/claude-go/internal/secure"
)

// Service is the keyring service identifier under which we stash tokens.
const Service = "com.anthropic.claude-code"

// PersistKey is the keyring item name for the OAuth token bundle.
const PersistKey = "oauth-tokens"

// PersistedTokens is the on-disk shape: the network Tokens plus a clock-stamped
// expiry so we don't need to call back to the server to know when to refresh.
type PersistedTokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	TokenType    string    `json:"token_type"`
	Scopes       []string  `json:"scopes"`
	// APIKey is an `sk-ant-oat01-…` key minted from the OAuth access token
	// via /api/oauth/claude_cli/create_api_key. Real Claude Code uses this
	// (not the OAuth bearer) on /v1/messages — the API rate-limits OAuth-
	// bearer-only requests as if they were unidentified clients.
	APIKey string `json:"api_key,omitempty"`
}

// String redacts the bundle so accidental log output doesn't leak.
func (PersistedTokens) String() string { return "<redacted oauth tokens>" }

// FromTokens projects network Tokens onto a PersistedTokens with an absolute
// expiry stamped using `now`.
func FromTokens(tok Tokens, now time.Time) PersistedTokens {
	return PersistedTokens{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    now.Add(time.Duration(tok.ExpiresIn) * time.Second),
		TokenType:    tok.TokenType,
		Scopes:       tok.Scopes,
	}
}

// Save writes the token bundle into secure storage.
func Save(s secure.Storage, p PersistedTokens) error {
	buf, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("auth: marshal persisted tokens: %w", err)
	}
	return s.Set(Service, PersistKey, buf)
}

// Load reads the token bundle from secure storage. Returns
// secure.ErrNotFound when no bundle is present.
func Load(s secure.Storage) (PersistedTokens, error) {
	raw, err := s.Get(Service, PersistKey)
	if err != nil {
		return PersistedTokens{}, err
	}
	var p PersistedTokens
	if err := json.Unmarshal(raw, &p); err != nil {
		return PersistedTokens{}, fmt.Errorf("auth: decode persisted tokens: %w", err)
	}
	return p, nil
}

// EnsureFresh returns a (possibly refreshed) access token. It refreshes when
// the existing token is within `skew` of expiry. Persists the new bundle on
// success.
//
// Returns ErrNotLoggedIn when no bundle is present.
func EnsureFresh(ctx context.Context, s secure.Storage, c *TokenClient, now time.Time, skew time.Duration) (PersistedTokens, error) {
	p, err := Load(s)
	if err != nil {
		if errors.Is(err, secure.ErrNotFound) {
			return PersistedTokens{}, ErrNotLoggedIn
		}
		return PersistedTokens{}, err
	}
	if p.RefreshToken == "" {
		return p, nil // no refresh capability — trust until 401
	}
	if now.Add(skew).Before(p.ExpiresAt) {
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
	if err := Save(s, fresh); err != nil {
		return PersistedTokens{}, err
	}
	return fresh, nil
}

// ErrNotLoggedIn is returned by EnsureFresh when no token bundle exists.
var ErrNotLoggedIn = errors.New("auth: not logged in (run `claude login` first)")
