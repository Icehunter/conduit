// Package copilot implements the GitHub Copilot provider auth and runtime.
package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/catalog"
	"github.com/icehunter/conduit/internal/providerauth"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

const (
	ProviderID    = "github-copilot"
	DisplayName   = "GitHub Copilot"
	ClientID      = "Iv1.b507a08c87ecfe98"
	ChatBaseURL   = "https://api.githubcopilot.com"
	deviceAuthURL = "https://github.com/login/device/code"
	tokenPollURL  = "https://github.com/login/oauth/access_token"
	tokenAPIURL   = "https://api.github.com/copilot_internal/v2/token"
	modelsURL     = "https://api.githubcopilot.com/models"
)

var (
	ErrNotAvailable = errors.New("github copilot not available for this account")

	httpClient        = &http.Client{Timeout: 30 * time.Second}
	deviceAuthURLFunc = func() string { return deviceAuthURL }
	tokenPollURLFunc  = func() string { return tokenPollURL }
	tokenAPIURLFunc   = func() string { return tokenAPIURL }
	modelsURLFunc     = func() string { return modelsURL }
)

type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type TokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int    `json:"expires_in"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
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
	return []providerauth.Method{{Kind: providerauth.MethodOAuth, Label: "Connect GitHub Copilot", Hint: "Click to authorize via GitHub"}}
}

func Headers() map[string]string {
	return map[string]string{
		"Accept":                 "application/json",
		"Copilot-Integration-Id": "vscode-chat",
		"Editor-Version":         "vscode/1.105.1",
		"Editor-Plugin-Version":  "copilot-chat/0.32.4",
		"OpenAI-Intent":          "conversation-edits",
		"User-Agent":             "GitHubCopilotChat/0.32.4",
		"X-GitHub-Api-Version":   "2023-07-07",
	}
}

func ChatHeaders() map[string]string {
	return map[string]string{
		"Copilot-Integration-Id": "vscode-chat",
		"Editor-Version":         "vscode/1.105.1",
		"Editor-Plugin-Version":  "copilot-chat/0.32.4",
		"OpenAI-Intent":          "conversation-edits",
		"X-GitHub-Api-Version":   "2023-07-07",
		"x-initiator":            "user",
	}
}

func MessagesHeaders() map[string]string {
	return map[string]string{
		"Copilot-Integration-Id": "vscode-chat",
		"Editor-Version":         "vscode/1.105.1",
		"Editor-Plugin-Version":  "copilot-chat/0.32.4",
		"OpenAI-Intent":          "conversation-edits",
		"X-GitHub-Api-Version":   "2023-07-07",
		"x-initiator":            "user",
	}
}

func ModelDiscoveryHeaders() map[string]string {
	return map[string]string{
		"Accept":                 "application/json",
		"Copilot-Integration-Id": "vscode-chat",
		"Editor-Version":         "vscode/1.105.1",
		"Editor-Plugin-Version":  "copilot-chat/0.32.4",
		"OpenAI-Intent":          "conversation-edits",
		"User-Agent":             "GitHubCopilotChat/0.32.4",
		"X-GitHub-Api-Version":   "2023-07-07",
	}
}

func UsesMessagesAPI(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(model, "claude-")
}

func ShouldUseResponsesAPI(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	match := strings.TrimPrefix(model, "gpt-")
	if match == model || match == "" {
		return false
	}
	first := match
	if idx := strings.IndexAny(first, ".-"); idx >= 0 {
		first = first[:idx]
	}
	var major int
	if _, err := fmt.Sscanf(first, "%d", &major); err != nil {
		return false
	}
	return major >= 5 && !strings.HasPrefix(model, "gpt-5-mini")
}

func FallbackModels() []catalog.ModelInfo {
	now := time.Now()
	return []catalog.ModelInfo{
		{ID: "gpt-5.1-codex", Name: "GPT-5.1-Codex", Provider: ProviderID, ContextWindow: 400000, ToolUse: true, Vision: true, Thinking: true, FetchedAt: now},
		{ID: "gpt-5.1-codex-mini", Name: "GPT-5.1-Codex Mini", Provider: ProviderID, ContextWindow: 400000, ToolUse: true, Vision: true, Thinking: true, FetchedAt: now},
		{ID: "gpt-5", Name: "GPT-5", Provider: ProviderID, ContextWindow: 128000, ToolUse: true, Vision: true, Thinking: true, FetchedAt: now},
		{ID: "gpt-5-mini", Name: "GPT-5 Mini", Provider: ProviderID, ContextWindow: 128000, ToolUse: true, Vision: true, Thinking: true, FetchedAt: now},
		{ID: "gpt-4.1", Name: "GPT-4.1", Provider: ProviderID, ContextWindow: 128000, ToolUse: true, Vision: true, FetchedAt: now},
		{ID: "gpt-4o", Name: "GPT-4o", Provider: ProviderID, ContextWindow: 128000, ToolUse: true, Vision: true, FetchedAt: now},
		{ID: "claude-sonnet-4.5", Name: "Claude Sonnet 4.5", Provider: ProviderID, ContextWindow: 128000, ToolUse: true, Vision: true, Thinking: true, FetchedAt: now},
		{ID: "claude-haiku-4.5", Name: "Claude Haiku 4.5", Provider: ProviderID, ContextWindow: 136000, ToolUse: true, Vision: true, Thinking: true, FetchedAt: now},
	}
}

func ContextWindowForModel(model string, current int) int {
	model = strings.ToLower(strings.TrimSpace(model))
	limit := 0
	for _, info := range FallbackModels() {
		if strings.EqualFold(info.ID, model) {
			limit = info.ContextWindow
			break
		}
	}
	if limit == 0 {
		switch {
		case strings.Contains(model, "haiku"):
			limit = 136000
		case strings.Contains(model, "claude"):
			limit = 128000
		case strings.Contains(model, "gpt-5-mini"):
			limit = 128000
		}
	}
	if current <= 0 {
		return limit
	}
	if limit > 0 && current > limit {
		return limit
	}
	return current
}

func (a *Authorizer) postForm(ctx context.Context, u string, data url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", Headers()["User-Agent"])
	return httpClient.Do(req)
}

func (a *Authorizer) InitiateDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	data := url.Values{}
	data.Set("client_id", ClientID)
	data.Set("scope", "read:user")

	resp, err := a.postForm(ctx, deviceAuthURLFunc(), data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, fmt.Errorf("copilot: device code request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var res DeviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	if res.DeviceCode == "" || res.UserCode == "" || res.VerificationURI == "" {
		return nil, fmt.Errorf("copilot: device code response missing required fields")
	}
	if res.Interval <= 0 {
		res.Interval = 5
	}
	return &res, nil
}

func (a *Authorizer) PollToken(ctx context.Context, deviceCode string, interval int) (*TokenResponse, error) {
	if strings.TrimSpace(deviceCode) == "" {
		return nil, fmt.Errorf("copilot: device code is required")
	}
	if interval <= 0 {
		interval = 5
	}
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			res, err := a.tryPollToken(ctx, deviceCode)
			if errors.Is(err, errAuthorizationPending) {
				continue
			}
			if errors.Is(err, errSlowDown) {
				interval += 5
				ticker.Reset(time.Duration(interval) * time.Second)
				continue
			}
			if err != nil {
				return nil, err
			}
			return res, nil
		}
	}
}

var (
	errAuthorizationPending = errors.New("authorization pending")
	errSlowDown             = errors.New("slow down")
)

func (a *Authorizer) tryPollToken(ctx context.Context, deviceCode string) (*TokenResponse, error) {
	data := url.Values{}
	data.Set("client_id", ClientID)
	data.Set("device_code", deviceCode)
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	resp, err := a.postForm(ctx, tokenPollURLFunc(), data)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var res TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	switch res.Error {
	case "":
	case "authorization_pending":
		return nil, errAuthorizationPending
	case "slow_down":
		return nil, errSlowDown
	case "expired_token":
		return nil, fmt.Errorf("copilot: authorization timed out")
	case "access_denied":
		return nil, fmt.Errorf("copilot: authorization was cancelled")
	default:
		if res.ErrorDescription != "" {
			return nil, fmt.Errorf("copilot: oauth error %s: %s", res.Error, res.ErrorDescription)
		}
		return nil, fmt.Errorf("copilot: oauth error %s", res.Error)
	}
	if res.AccessToken == "" {
		return nil, errAuthorizationPending
	}
	return a.exchangeGitHubToken(ctx, res.AccessToken)
}

func (a *Authorizer) exchangeGitHubToken(ctx context.Context, githubToken string) (*TokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenAPIURLFunc(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+githubToken)
	for k, v := range Headers() {
		req.Header.Set(k, v)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, ErrNotAvailable
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("copilot: token exchange failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var result struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("copilot: decode token exchange: %w", err)
	}
	if result.Token == "" {
		return nil, fmt.Errorf("copilot: token exchange returned no token")
	}
	expiry := time.Time{}
	expiresIn := 0
	if result.ExpiresAt > 0 {
		expiry = time.Unix(result.ExpiresAt, 0)
		expiresIn = int(time.Until(expiry).Seconds())
	}
	res := &TokenResponse{
		AccessToken:  result.Token,
		RefreshToken: githubToken,
		ExpiresIn:    expiresIn,
		TokenType:    "bearer",
	}
	cred := &providerauth.ProviderCredential{
		AccessToken:  res.AccessToken,
		RefreshToken: res.RefreshToken,
		Expiry:       expiry,
		Metadata: map[string]string{
			"provider": ProviderID,
			"method":   providerauth.MethodOAuth,
		},
	}
	if cred.Expiry.IsZero() && res.ExpiresIn > 0 {
		cred.Expiry = time.Now().Add(time.Duration(res.ExpiresIn) * time.Second)
	}
	if err := settings.SaveStructuredProviderCredential(a.store, a.credentialName, cred); err != nil {
		return nil, err
	}
	return res, nil
}

func (a *Authorizer) FetchModels(ctx context.Context) ([]catalog.ModelInfo, error) {
	tryFetch := func() (*http.Response, error) {
		cred, err := a.EnsureFresh(ctx)
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURLFunc(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+cred.AccessToken)
		for k, v := range ModelDiscoveryHeaders() {
			req.Header.Set(k, v)
		}
		return httpClient.Do(req)
	}

	resp, err := tryFetch()
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		if err := a.Refresh(ctx); err != nil {
			return nil, err
		}
		resp, err = tryFetch()
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, fmt.Errorf("copilot: model discovery failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	models, err := decodeModels(resp.Body)
	if err != nil {
		return nil, err
	}
	return models, nil
}

func (a *Authorizer) Refresh(ctx context.Context) error {
	cred, err := settings.LoadStructuredProviderCredential(a.store, a.credentialName)
	if err != nil {
		return err
	}
	if cred.RefreshToken == "" {
		return fmt.Errorf("copilot: refresh token missing; reconnect GitHub Copilot")
	}
	if _, err := a.exchangeGitHubToken(ctx, cred.RefreshToken); err != nil {
		if errors.Is(err, ErrNotAvailable) {
			_ = settings.DeleteProviderCredential(a.store, a.credentialName)
		}
		return err
	}
	return nil
}

func (a *Authorizer) EnsureFresh(ctx context.Context) (*providerauth.ProviderCredential, error) {
	cred, err := settings.LoadStructuredProviderCredential(a.store, a.credentialName)
	if err != nil {
		return nil, err
	}
	if cred.AccessToken == "" {
		return nil, providerauth.ErrMissingCredential
	}
	if !cred.Expiry.IsZero() && time.Until(cred.Expiry) < time.Minute {
		if err := a.Refresh(ctx); err != nil {
			return nil, err
		}
		return settings.LoadStructuredProviderCredential(a.store, a.credentialName)
	}
	return cred, nil
}

func (a *Authorizer) Authorize(ctx context.Context, kind string, params map[string]string) (string, error) {
	if kind != providerauth.MethodOAuth {
		return "", fmt.Errorf("copilot: unsupported auth kind %q", kind)
	}
	deviceCode := strings.TrimSpace(params["device_code"])
	if deviceCode == "" {
		return "", fmt.Errorf("copilot: device_code is required")
	}
	interval := 5
	if raw := strings.TrimSpace(params["interval"]); raw != "" {
		var parsed int
		if _, err := fmt.Sscanf(raw, "%d", &parsed); err == nil && parsed > 0 {
			interval = parsed
		}
	}
	res, err := a.PollToken(ctx, deviceCode, interval)
	if err != nil {
		return "", err
	}
	return res.AccessToken, nil
}

func (a *Authorizer) Validate(ctx context.Context, credential string) error {
	if credential == "" {
		return providerauth.ErrMissingCredential
	}
	return nil
}

func (a *Authorizer) GetAccount(ctx context.Context) (*providerauth.ProviderAccount, error) {
	cred, err := settings.LoadStructuredProviderCredential(a.store, a.credentialName)
	if err != nil {
		return nil, err
	}
	if cred.AccessToken == "" {
		return nil, providerauth.ErrMissingCredential
	}
	return &providerauth.ProviderAccount{
		ID:          ProviderID,
		ProviderID:  ProviderID,
		DisplayName: DisplayName,
		Method:      providerauth.MethodOAuth,
		AddedAt:     time.Now(),
		Active:      true,
	}, nil
}

func decodeModels(r io.Reader) ([]catalog.ModelInfo, error) {
	var raw struct {
		Models []copilotModel `json:"models"`
		Data   []copilotModel `json:"data"`
	}
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("copilot: decode models: %w", err)
	}
	models := raw.Models
	if len(models) == 0 {
		models = raw.Data
	}
	out := make([]catalog.ModelInfo, 0, len(models))
	now := time.Now()
	for _, model := range models {
		id := strings.TrimSpace(model.ID)
		if id == "" || model.Disabled() {
			continue
		}
		name := strings.TrimSpace(model.Name)
		if name == "" {
			name = id
		}
		out = append(out, catalog.ModelInfo{
			ID:            id,
			Name:          name,
			Provider:      ProviderID,
			ContextWindow: model.ContextWindow(),
			ToolUse:       model.ToolUse(),
			Vision:        model.Vision(),
			Thinking:      model.Thinking(),
			FetchedAt:     now,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("copilot: no usable models returned")
	}
	return out, nil
}

type copilotModel struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	ModelPickerEnabled *bool    `json:"model_picker_enabled"`
	ContextWindowFlat  int      `json:"context_window"`
	DisabledFlat       bool     `json:"disabled"`
	SupportsVisionFlat bool     `json:"supports_vision"`
	SupportsThinkFlat  bool     `json:"supports_thinking"`
	SupportedEndpoints []string `json:"supported_endpoints"`
	Policy             struct {
		State string `json:"state"`
	} `json:"policy"`
	Capabilities struct {
		Limits struct {
			MaxContextWindowTokens int `json:"max_context_window_tokens"`
			MaxPromptTokens        int `json:"max_prompt_tokens"`
			Vision                 struct {
				SupportedMediaTypes []string `json:"supported_media_types"`
			} `json:"vision"`
		} `json:"limits"`
		Supports struct {
			AdaptiveThinking  bool     `json:"adaptive_thinking"`
			ReasoningEffort   []string `json:"reasoning_effort"`
			MaxThinkingBudget *int     `json:"max_thinking_budget"`
			MinThinkingBudget *int     `json:"min_thinking_budget"`
			ToolCalls         bool     `json:"tool_calls"`
			Vision            *bool    `json:"vision"`
		} `json:"supports"`
	} `json:"capabilities"`
}

func (m copilotModel) Disabled() bool {
	if m.ModelPickerEnabled != nil && !*m.ModelPickerEnabled {
		return true
	}
	return m.DisabledFlat || strings.EqualFold(m.Policy.State, "disabled")
}

func (m copilotModel) ContextWindow() int {
	if m.Capabilities.Limits.MaxPromptTokens > 0 {
		return m.Capabilities.Limits.MaxPromptTokens
	}
	if m.Capabilities.Limits.MaxContextWindowTokens > 0 {
		return m.Capabilities.Limits.MaxContextWindowTokens
	}
	return m.ContextWindowFlat
}

func (m copilotModel) ToolUse() bool {
	if m.Capabilities.Supports.ToolCalls {
		return true
	}
	return true
}

func (m copilotModel) Vision() bool {
	if m.Capabilities.Supports.Vision != nil {
		return *m.Capabilities.Supports.Vision
	}
	return m.SupportsVisionFlat || len(m.Capabilities.Limits.Vision.SupportedMediaTypes) > 0
}

func (m copilotModel) Thinking() bool {
	return m.SupportsThinkFlat ||
		m.Capabilities.Supports.AdaptiveThinking ||
		len(m.Capabilities.Supports.ReasoningEffort) > 0 ||
		m.Capabilities.Supports.MaxThinkingBudget != nil ||
		m.Capabilities.Supports.MinThinkingBudget != nil
}
