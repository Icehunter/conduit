// Package codex implements the ChatGPT/Codex product-account provider.
package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/catalog"
	"github.com/icehunter/conduit/internal/providerauth"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

const (
	ProviderID   = "chatgpt-codex"
	DisplayName  = "ChatGPT / Codex"
	ClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	Issuer       = "https://auth.openai.com"
	CodexBaseURL = "https://chatgpt.com/backend-api/codex"
	OAuthPort    = 1455
	callbackPath = "/auth/callback"
)

var (
	httpClient  = &http.Client{Timeout: 30 * time.Second}
	issuerURL   = func() string { return Issuer }
	browserOpen = func(rawURL string) error { return auth.SystemBrowser{}.Open(rawURL) }
)

type TokenResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

type Claims struct {
	ChatGPTAccountID string `json:"chatgpt_account_id"`
	Email            string `json:"email"`
	Auth             struct {
		ChatGPTAccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
	Organizations []struct {
		ID string `json:"id"`
	} `json:"organizations"`
}

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
	return []providerauth.Method{{Kind: providerauth.MethodOAuth, Label: "Connect ChatGPT / Codex", Hint: "Open browser to authorize with OpenAI"}}
}

func (a *Authorizer) Validate(context.Context, string) error { return nil }

func (a *Authorizer) Authorize(ctx context.Context, kind string, _ map[string]string) (string, error) {
	if kind != providerauth.MethodOAuth {
		return "", fmt.Errorf("codex: unsupported auth method %q", kind)
	}
	cred, err := a.AuthorizeBrowser(ctx)
	if err != nil {
		return "", err
	}
	return cred.Metadata["account_id"], nil
}

func (a *Authorizer) AuthorizeBrowser(ctx context.Context) (*providerauth.ProviderCredential, error) {
	listener, err := auth.NewCallbackListenerOnAddr(fmt.Sprintf("127.0.0.1:%d", OAuthPort), callbackPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = listener.Close() }()

	verifier, err := auth.GenerateVerifier()
	if err != nil {
		return nil, err
	}
	state, err := auth.GenerateState()
	if err != nil {
		return nil, err
	}
	redirectURI := fmt.Sprintf("http://localhost:%d%s", OAuthPort, callbackPath)
	if err := listener.Register(state); err != nil {
		return nil, err
	}
	authURL := BuildAuthorizeURL(redirectURI, auth.S256Challenge(verifier), state)
	if err := browserOpen(authURL); err != nil {
		return nil, fmt.Errorf("codex: open browser failed: %w; visit %s", err, authURL)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	code, err := listener.Wait(waitCtx, state)
	if err != nil {
		return nil, err
	}
	tokens, err := ExchangeCode(ctx, code, redirectURI, verifier)
	if err != nil {
		listener.SendErrorRedirect("")
		return nil, err
	}
	cred := CredentialFromTokens(tokens)
	if err := a.saveCredential(cred); err != nil {
		listener.SendErrorRedirect("")
		return nil, err
	}
	listener.SendSuccessRedirect("")
	return cred, nil
}

func BuildAuthorizeURL(redirectURI, challenge, state string) string {
	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", ClientID)
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", "openid profile email offline_access")
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("id_token_add_organizations", "true")
	params.Set("codex_cli_simplified_flow", "true")
	params.Set("state", state)
	params.Set("originator", "opencode")
	return strings.TrimRight(issuerURL(), "/") + "/oauth/authorize?" + params.Encode()
}

func ExchangeCode(ctx context.Context, code, redirectURI, verifier string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", ClientID)
	form.Set("code_verifier", verifier)
	return tokenRequest(ctx, form)
}

func RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", ClientID)
	return tokenRequest(ctx, form)
}

func tokenRequest(ctx context.Context, form url.Values) (*TokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(issuerURL(), "/")+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex: token request: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	var out TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("codex: decode token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := out.ErrorDesc
		if msg == "" {
			msg = out.Error
		}
		if msg == "" {
			msg = resp.Status
		}
		return nil, fmt.Errorf("codex: token request failed: %s", msg)
	}
	if out.AccessToken == "" || out.RefreshToken == "" {
		return nil, errors.New("codex: token response missing access or refresh token")
	}
	return &out, nil
}

func CredentialFromTokens(tokens *TokenResponse) *providerauth.ProviderCredential {
	expiry := time.Now().Add(time.Hour)
	if tokens != nil && tokens.ExpiresIn > 0 {
		expiry = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	}
	if tokens == nil {
		tokens = &TokenResponse{}
	}
	metadata := map[string]string{}
	if accountID := ExtractAccountID(tokens); accountID != "" {
		metadata["account_id"] = accountID
	}
	if email := ExtractEmail(tokens); email != "" {
		metadata["email"] = email
	}
	return &providerauth.ProviderCredential{
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		Expiry:       expiry,
		Metadata:     metadata,
	}
}

func ParseClaims(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("codex: invalid jwt shape")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(parts[1])
	}
	if err != nil {
		return nil, err
	}
	var claims Claims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, err
	}
	return &claims, nil
}

func ExtractAccountID(tokens *TokenResponse) string {
	if tokens == nil {
		return ""
	}
	for _, token := range []string{tokens.IDToken, tokens.AccessToken} {
		if token == "" {
			continue
		}
		claims, err := ParseClaims(token)
		if err != nil {
			continue
		}
		if claims.ChatGPTAccountID != "" {
			return claims.ChatGPTAccountID
		}
		if claims.Auth.ChatGPTAccountID != "" {
			return claims.Auth.ChatGPTAccountID
		}
		if len(claims.Organizations) > 0 && claims.Organizations[0].ID != "" {
			return claims.Organizations[0].ID
		}
	}
	return ""
}

func ExtractEmail(tokens *TokenResponse) string {
	if tokens == nil {
		return ""
	}
	for _, token := range []string{tokens.IDToken, tokens.AccessToken} {
		if token == "" {
			continue
		}
		claims, err := ParseClaims(token)
		if err == nil && claims.Email != "" {
			return claims.Email
		}
	}
	return ""
}

func (a *Authorizer) EnsureFresh(ctx context.Context) (*providerauth.ProviderCredential, error) {
	cred, err := settings.LoadStructuredProviderCredential(a.store, a.credentialName)
	if err != nil {
		return nil, fmt.Errorf("codex credential: %w", err)
	}
	if cred.AccessToken != "" && time.Until(cred.Expiry) > 5*time.Minute {
		return cred, nil
	}
	if cred.RefreshToken == "" {
		return nil, errors.New("codex: missing refresh token")
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

func Headers(accountID, sessionID, version string) map[string]string {
	headers := map[string]string{
		"originator": "conduit",
		"User-Agent": fmt.Sprintf("conduit/%s (%s; %s)", version, runtime.GOOS, runtime.GOARCH),
	}
	if sessionID != "" {
		headers["session_id"] = sessionID
	}
	if accountID != "" {
		headers["ChatGPT-Account-Id"] = accountID
	}
	return headers
}

func FallbackModels() []catalog.ModelInfo {
	now := time.Now()
	return []catalog.ModelInfo{
		{ID: "gpt-5.5", Name: "GPT-5.5", Provider: ProviderID, ContextWindow: 400000, ToolUse: true, Vision: true, Thinking: true, FetchedAt: now},
		{ID: "gpt-5.4", Name: "GPT-5.4", Provider: ProviderID, ContextWindow: 272000, ToolUse: true, Vision: true, Thinking: true, FetchedAt: now},
		{ID: "gpt-5.4-mini", Name: "GPT-5.4 Mini", Provider: ProviderID, ContextWindow: 272000, ToolUse: true, Vision: true, Thinking: true, FetchedAt: now},
		{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex", Provider: ProviderID, ContextWindow: 272000, ToolUse: true, Vision: true, Thinking: true, FetchedAt: now},
		{ID: "gpt-5.3-codex-spark", Name: "GPT-5.3 Codex Spark", Provider: ProviderID, ContextWindow: 272000, ToolUse: true, Vision: true, Thinking: true, FetchedAt: now},
		{ID: "gpt-5.2", Name: "GPT-5.2", Provider: ProviderID, ContextWindow: 272000, ToolUse: true, Vision: true, Thinking: true, FetchedAt: now},
	}
}

func FallbackModelIDs() []string {
	models := FallbackModels()
	out := make([]string, 0, len(models))
	for _, model := range models {
		out = append(out, model.ID)
	}
	return out
}

func ContextWindowForModel(model string, current int) int {
	for _, info := range FallbackModels() {
		if strings.EqualFold(info.ID, model) {
			return info.ContextWindow
		}
	}
	if current > 0 {
		return current
	}
	return 272000
}
