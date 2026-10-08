// Package xai implements the Grok (xAI) subscription-account provider. It
// signs in with the same public OAuth client the Grok CLI uses (PKCE browser
// flow or RFC 8628 device flow) and presents the resulting access token as a
// Bearer credential to the OpenAI-compatible API at api.x.ai.
package xai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/credlock"
	"github.com/icehunter/conduit/internal/providerauth"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

const (
	// ProviderID is the default credential alias. Aliases for further
	// accounts keep this prefix, which is how the app and TUI recognise a
	// Grok account. It deliberately avoids an "xai-" prefix: that is the
	// shape of an API key and the provider form rejects it as a secret.
	ProviderID  = "grok-xai"
	DisplayName = "Grok / xAI"

	// ClientID is the public OAuth client xAI registers for its own Grok CLI.
	// xAI offers no self-service client registration, so agent CLIs share it.
	ClientID = "b1a00492-073a-47ea-816f-4c329264a828"
	Issuer   = "https://auth.x.ai"

	// BaseURL is the API the account's access token authorizes.
	BaseURL = "https://api.x.ai/v1"

	// Scope is the set registered for the shared client; the server expects
	// all of it.
	Scope = "openid profile email offline_access grok-cli:access " +
		"api:access conversations:read conversations:write " +
		"workspaces:read workspaces:write"

	// referrer identifies this client on the consent screen.
	referrer = "conduit"
)

var (
	httpClient = &http.Client{Timeout: 30 * time.Second}
	issuerURL  = func() string { return Issuer }
	apiBaseURL = func() string { return BaseURL }
)

// TokenResponse is the token endpoint payload.
type TokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// Claims are the identity claims read from the ID token.
type Claims struct {
	Subject string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
}

// tokenError is a non-2xx token endpoint answer; Code is the OAuth error.
type tokenError struct {
	Status int
	Code   string
	Desc   string
}

func (e *tokenError) Error() string {
	msg := e.Desc
	if msg == "" {
		msg = e.Code
	}
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	return fmt.Sprintf("xai: token request failed: %s", msg)
}

// IsInvalidGrant reports whether err is the token endpoint rejecting a refresh
// token, which means the account must sign in again.
func IsInvalidGrant(err error) bool {
	var te *tokenError
	return errors.As(err, &te) && te.Code == "invalid_grant"
}

func authorizeEndpoint() string { return strings.TrimRight(issuerURL(), "/") + "/oauth2/authorize" }
func tokenEndpoint() string     { return strings.TrimRight(issuerURL(), "/") + "/oauth2/token" }

// BuildAuthorizeURL is the browser authorization URL for the loopback redirect.
func BuildAuthorizeURL(redirectURI, challenge, state, nonce string) string {
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", ClientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", Scope)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", state)
	params.Set("nonce", nonce)
	params.Set("referrer", referrer)
	return authorizeEndpoint() + "?" + params.Encode()
}

// ExchangeCode trades an authorization code for tokens.
func ExchangeCode(ctx context.Context, code, redirectURI, verifier string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", ClientID)
	form.Set("code_verifier", verifier)
	return tokenRequest(ctx, form)
}

// RefreshToken renews an access token. The server does not always rotate the
// refresh token, so the previous one is kept when the answer omits it.
func RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", ClientID)
	tokens, err := tokenRequest(ctx, form)
	if err != nil {
		return nil, err
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = refreshToken
	}
	return tokens, nil
}

func tokenRequest(ctx context.Context, form url.Values) (*TokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xai: token request: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("xai: read token response: %w", err)
	}
	var out TokenResponse
	decodeErr := json.Unmarshal(body, &out)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &tokenError{Status: resp.StatusCode, Code: out.Error, Desc: out.ErrorDesc}
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("xai: decode token response: %w", decodeErr)
	}
	if out.AccessToken == "" {
		return nil, errors.New("xai: token response missing access token")
	}
	return &out, nil
}

// CredentialFromTokens converts a token response to the stored credential.
func CredentialFromTokens(tokens *TokenResponse) *providerauth.ProviderCredential {
	if tokens == nil {
		tokens = &TokenResponse{}
	}
	expiry := time.Now().Add(time.Hour)
	if tokens.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	}
	metadata := map[string]string{}
	if claims, err := ParseClaims(tokens.IDToken); err == nil {
		if claims.Subject != "" {
			metadata["account_id"] = claims.Subject
		}
		if claims.Email != "" {
			metadata["email"] = claims.Email
		}
	}
	return &providerauth.ProviderCredential{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		Expiry:       expiry,
		Metadata:     metadata,
	}
}

// ParseClaims decodes the payload of a JWT without verifying it; the claims
// only label the account.
func ParseClaims(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("xai: invalid jwt shape")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var claims Claims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, err
	}
	return &claims, nil
}

// Authorizer signs a Grok account in and keeps its credential fresh.
type Authorizer struct {
	store          secure.Storage
	credentialName string
}

func NewAuthorizer(store secure.Storage) *Authorizer {
	return NewAuthorizerForCredential(store, ProviderID)
}

func NewAuthorizerForCredential(store secure.Storage, credentialName string) *Authorizer {
	credentialName = strings.TrimSpace(credentialName)
	if credentialName == "" {
		credentialName = ProviderID
	}
	return &Authorizer{store: store, credentialName: credentialName}
}

func (a *Authorizer) ProviderID() string { return ProviderID }

func (a *Authorizer) Methods() []providerauth.Method {
	return []providerauth.Method{{Kind: providerauth.MethodOAuth, Label: "Connect Grok / xAI", Hint: "Open browser to sign in with your Grok account"}}
}

func (a *Authorizer) Validate(context.Context, string) error { return nil }

func (a *Authorizer) Authorize(ctx context.Context, kind string, _ map[string]string) (string, error) {
	if kind != providerauth.MethodOAuth {
		return "", fmt.Errorf("xai: unsupported auth method %q", kind)
	}
	flow, err := StartBrowserFlow()
	if err != nil {
		return "", err
	}
	defer flow.Close()
	if err := browserOpen(flow.URL()); err != nil {
		return "", fmt.Errorf("xai: open browser failed: %w; visit %s", err, flow.URL())
	}
	cred, err := a.FinishBrowserFlow(ctx, flow)
	if err != nil {
		return "", err
	}
	return cred.Metadata["account_id"], nil
}

// FinishBrowserFlow waits for the redirect, exchanges the code and stores the
// credential.
func (a *Authorizer) FinishBrowserFlow(ctx context.Context, flow *BrowserFlow) (*providerauth.ProviderCredential, error) {
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	tokens, err := flow.Wait(waitCtx)
	if err != nil {
		return nil, err
	}
	return a.storeTokens(tokens)
}

// AuthorizeDevice runs the device-code flow. show is called once with the code
// and page the user must visit.
func (a *Authorizer) AuthorizeDevice(ctx context.Context, show func(*DeviceCode)) (*providerauth.ProviderCredential, error) {
	dc, err := RequestDeviceCode(ctx)
	if err != nil {
		return nil, err
	}
	if show != nil {
		show(dc)
	}
	tokens, err := PollForToken(ctx, dc.DeviceCode, dc.ExpiresIn, dc.Interval)
	if err != nil {
		return nil, err
	}
	return a.storeTokens(tokens)
}

func (a *Authorizer) storeTokens(tokens *TokenResponse) (*providerauth.ProviderCredential, error) {
	cred := CredentialFromTokens(tokens)
	if err := a.saveCredential(cred); err != nil {
		return nil, err
	}
	return cred, nil
}

// EnsureFresh returns the stored credential, refreshing it when it expires
// within five minutes.
func (a *Authorizer) EnsureFresh(ctx context.Context) (*providerauth.ProviderCredential, error) {
	return a.ensureFresh(ctx, false)
}

// ForceRefresh renews the credential regardless of its recorded expiry, for a
// 401 on a token that looked valid. If another process already refreshed it
// while this one waited for the lock, that newer credential is adopted.
func (a *Authorizer) ForceRefresh(ctx context.Context) (*providerauth.ProviderCredential, error) {
	return a.ensureFresh(ctx, true)
}

func (a *Authorizer) ensureFresh(ctx context.Context, force bool) (*providerauth.ProviderCredential, error) {
	cred, err := settings.LoadStructuredProviderCredential(a.store, a.credentialName)
	stale := cred // compared by access token below, before cred changes
	if err != nil {
		return nil, fmt.Errorf("xai credential: %w", err)
	}
	if !force && cred.AccessToken != "" && time.Until(cred.Expiry) > 5*time.Minute {
		return cred, nil
	}
	if cred.RefreshToken == "" {
		return nil, errors.New("xai: missing refresh token")
	}

	// Serialize across processes: a rotated refresh token is rejected on
	// reuse, so two conduit instances would sign each other out. A lock
	// failure is not fatal.
	release, _ := credlock.Acquire(ctx, "xai:"+a.credentialName)
	defer release()

	// Re-read after waiting: the lock holder has just written a new credential.
	if latest, lerr := settings.LoadStructuredProviderCredential(a.store, a.credentialName); lerr == nil && latest.RefreshToken != "" {
		rotated := stale == nil || latest.AccessToken != stale.AccessToken
		if latest.AccessToken != "" && time.Until(latest.Expiry) > 5*time.Minute && (!force || rotated) {
			return latest, nil
		}
		cred = latest
	}

	tokens, err := RefreshToken(ctx, cred.RefreshToken)
	if err != nil {
		return nil, err
	}
	next := CredentialFromTokens(tokens)
	if next.Metadata == nil {
		next.Metadata = map[string]string{}
	}
	for k, v := range cred.Metadata {
		if next.Metadata[k] == "" {
			next.Metadata[k] = v
		}
	}
	if err := a.saveCredential(next); err != nil {
		return nil, err
	}
	return next, nil
}

func (a *Authorizer) Refresh(ctx context.Context) error {
	_, err := a.EnsureFresh(ctx)
	return err
}

func (a *Authorizer) GetAccount(ctx context.Context) (*providerauth.ProviderAccount, error) {
	cred, err := a.EnsureFresh(ctx)
	if err != nil {
		return nil, err
	}
	accountID := cred.Metadata["account_id"]
	if accountID == "" {
		accountID = a.credentialName
	}
	display := cred.Metadata["email"]
	if display == "" {
		display = accountID
	}
	return &providerauth.ProviderAccount{
		ID:          accountID,
		ProviderID:  ProviderID,
		DisplayName: display,
		Method:      providerauth.MethodOAuth,
		Active:      true,
	}, nil
}

func (a *Authorizer) saveCredential(cred *providerauth.ProviderCredential) error {
	return settings.SaveStructuredProviderCredential(a.store, a.credentialName, cred)
}

var browserOpen = func(rawURL string) error { return auth.SystemBrowser{}.Open(rawURL) }

// OpenBrowser opens rawURL in the system browser.
func OpenBrowser(rawURL string) error { return browserOpen(rawURL) }
