package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Tokens is the application-level OAuth token bundle.
//
// Field semantics mirror the reference (decoded/1220.js): RefreshToken is
// preserved across refreshes if the server omits it; ExpiresIn is the raw
// `expires_in` seconds value (the absolute expiry timestamp is computed by
// the caller using a clock interface, so we don't bake `time.Now()` into
// pure data).
type Tokens struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	TokenType    string
	Scopes       []string
	// Email is populated from account.email_address in the token response,
	// when the server includes it. Used for multi-account keychain key scoping.
	Email string
}

// String redacts tokens to keep them out of logs by accident.
func (Tokens) String() string { return "<redacted oauth tokens>" }

// ExchangeParams is the input to ExchangeCodeForTokens. UseManual selects
// MANUAL_REDIRECT_URL over http://localhost:<Port>/callback, matching
// decoded/1220.js:65 (function rm6) parameter O.
type ExchangeParams struct {
	Code         string
	State        string
	CodeVerifier string
	Port         int
	UseManual    bool
	// ExpiresIn is optional. When > 0 it's sent as `expires_in` in the
	// request body (decoded/1220.js:74). Used by inference-only flows.
	ExpiresIn int
}

// RefreshOptions matches decoded/1220.js:87 ({scopes, expiresIn, clientId}).
type RefreshOptions struct {
	// Scopes overrides the default ScopesClaudeAI. Pass nil for default.
	Scopes []string
	// ExpiresIn, when > 0, requests a specific token lifetime.
	ExpiresIn int
	// ClientID overrides the configured client_id. Used for some SDK paths
	// (decoded/1220.js:91: `K ?? Aq().CLIENT_ID`).
	ClientID string
}

// TokenClient performs the OAuth code exchange and refresh round-trips.
//
// It is a thin wrapper around an *http.Client that knows the wire shape of
// platform.claude.com/v1/oauth/token. Higher-level retry, persistence, and
// profile fetching live in callers.
type TokenClient struct {
	cfg  Config
	http *http.Client
}

// NewTokenClient returns a TokenClient. If httpClient is nil, http.DefaultClient is used.
func NewTokenClient(cfg Config, httpClient *http.Client) *TokenClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &TokenClient{cfg: cfg, http: httpClient}
}

// tokenResponse mirrors the JSON returned from /v1/oauth/token.
// The `account` field carries email_address directly in the token response,
// which is the authoritative source for multi-account key scoping.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type,omitempty"`
	Scope        string `json:"scope,omitempty"`
	Account      *struct {
		UUID         string `json:"uuid"`
		EmailAddress string `json:"email_address"`
	} `json:"account,omitempty"`
}

// ExchangeCodeForTokens trades an OAuth authorization code for tokens.
// Reference: decoded/1220.js:65-86 (function rm6).
func (c *TokenClient) ExchangeCodeForTokens(ctx context.Context, p ExchangeParams) (Tokens, error) {
	if p.Code == "" || p.State == "" || p.CodeVerifier == "" {
		return Tokens{}, fmt.Errorf("auth: ExchangeCodeForTokens: code, state, and code_verifier are required")
	}
	redirect := fmt.Sprintf("http://localhost:%d/callback", p.Port)
	if p.UseManual {
		redirect = c.cfg.ManualRedirectURL
	}

	body := map[string]any{
		"grant_type":    "authorization_code",
		"code":          p.Code,
		"redirect_uri":  redirect,
		"client_id":     c.cfg.ClientID,
		"code_verifier": p.CodeVerifier,
		"state":         p.State,
	}
	if p.ExpiresIn > 0 {
		body["expires_in"] = p.ExpiresIn
	}

	resp, err := c.postJSON(ctx, c.cfg.TokenURL, body)
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Match decoded/1220.js:79-84 messaging.
		if resp.StatusCode == http.StatusUnauthorized {
			return Tokens{}, fmt.Errorf("auth: Authentication failed: Invalid authorization code")
		}
		return Tokens{}, fmt.Errorf("auth: Token exchange failed (%d): %s", resp.StatusCode, resp.Status)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return Tokens{}, fmt.Errorf("auth: decode token response: %w", err)
	}
	return finalizeTokens(tr, "")
}

// CreateAPIKey trades an OAuth access token for a long-lived
// `sk-ant-oat01-…` API key. The real CLI does this once per session and
// uses the resulting key as the bearer for /v1/messages — using the OAuth
// access token directly causes the API to reject the request as a
// non-Claude-Code client.
//
// Reference: decoded/1220.js:175-195 (function am6),
// src/services/oauth/client.ts createAndStoreApiKey.
func (c *TokenClient) CreateAPIKey(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.APIKeyURL, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("auth: build api-key request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth: api-key request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("auth: create_api_key %d: %s", resp.StatusCode, resp.Status)
	}
	var body struct {
		RawKey string `json:"raw_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("auth: decode api-key response: %w", err)
	}
	if body.RawKey == "" {
		return "", fmt.Errorf("auth: create_api_key returned empty raw_key")
	}
	return body.RawKey, nil
}

// RefreshOAuthToken trades a refresh_token for a new access (and optionally
// new refresh) token. Reference: decoded/1220.js:87-100 (function r__).
func (c *TokenClient) RefreshOAuthToken(ctx context.Context, refreshToken string, opt RefreshOptions) (Tokens, error) {
	if refreshToken == "" {
		return Tokens{}, fmt.Errorf("auth: RefreshOAuthToken: refresh_token required")
	}

	scopes := opt.Scopes
	if len(scopes) == 0 {
		scopes = ScopesClaudeAI
	}
	clientID := opt.ClientID
	if clientID == "" {
		clientID = c.cfg.ClientID
	}

	body := map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     clientID,
		"scope":         strings.Join(scopes, " "),
	}
	if opt.ExpiresIn > 0 {
		body["expires_in"] = opt.ExpiresIn
	}

	resp, err := c.postJSON(ctx, c.cfg.TokenURL, body)
	if err != nil {
		return Tokens{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Tokens{}, fmt.Errorf("auth: Token refresh failed: %s", resp.Status)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return Tokens{}, fmt.Errorf("auth: decode refresh response: %w", err)
	}
	return finalizeTokens(tr, refreshToken)
}

// finalizeTokens validates the token response and projects it onto our
// public Tokens type. If TokenType is set and not "bearer" (case-insensitive)
// we reject — matches decoded/1390.js:38.
func finalizeTokens(tr tokenResponse, fallbackRefresh string) (Tokens, error) {
	if tr.AccessToken == "" {
		return Tokens{}, fmt.Errorf("auth: token endpoint response missing access_token")
	}
	if tr.TokenType != "" && !strings.EqualFold(tr.TokenType, "bearer") {
		return Tokens{}, fmt.Errorf("auth: unsupported token_type %q (want bearer)", tr.TokenType)
	}
	rt := tr.RefreshToken
	if rt == "" {
		rt = fallbackRefresh
	}
	email := ""
	if tr.Account != nil {
		email = tr.Account.EmailAddress
	}
	return Tokens{
		AccessToken:  tr.AccessToken,
		RefreshToken: rt,
		ExpiresIn:    tr.ExpiresIn,
		TokenType:    tr.TokenType,
		Scopes:       parseScopes(tr.Scope),
		Email:        email,
	}, nil
}

// parseScopes splits the space-separated scope string and drops empties.
// Mirrors decoded/1220.js:29 (function i__).
func parseScopes(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Fields(s) // collapses arbitrary whitespace, matches filter(Boolean) on split(" ")
	if len(parts) == 0 {
		return nil
	}
	return parts
}

// postJSON issues a JSON POST. We hand-marshal to keep the body order
// stable (Go's encoding/json sorts map keys alphabetically — fine for the
// token endpoint, but using a struct or explicit ordering would be needed
// if the server ever became order-sensitive).
func (c *TokenClient) postJSON(ctx context.Context, url string, body any) (*http.Response, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("auth: marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, fmt.Errorf("auth: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// Drain and discard any partial body.
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
		return nil, fmt.Errorf("auth: token endpoint request: %w", err)
	}
	return resp, nil
}
