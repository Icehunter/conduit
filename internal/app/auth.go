package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/catalog"
	"github.com/icehunter/conduit/internal/profile"
	"github.com/icehunter/conduit/internal/provider/codex"
	"github.com/icehunter/conduit/internal/provider/copilot"
	"github.com/icehunter/conduit/internal/provider/xai"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

// NewAPIClient builds a configured API client using persisted account
// credentials. Claude.ai uses OAuth bearer auth; Anthropic Console uses the
// minted API key when available.
func NewAPIClient(tok auth.PersistedTokens, wireVersion string) *api.Client {
	return NewAPIClientForAccount(tok, wireVersion, "")
}

// NewAPIClientForAccount is NewAPIClient for tokens loaded from a specific
// account. account names the credential to renew when the API rejects the
// access token; empty means whichever account is active at that moment.
func NewAPIClientForAccount(tok auth.PersistedTokens, wireVersion, account string) *api.Client {
	entrypoint := os.Getenv("CLAUDE_CODE_ENTRYPOINT")
	if entrypoint == "" {
		entrypoint = "sdk-cli"
	}
	ua := fmt.Sprintf("claude-cli/%s (external, %s)", wireVersion, entrypoint)
	authToken := tok.AccessToken
	apiKey := ""
	if auth.InferAccountKind(tok) == auth.AccountKindAnthropicConsole && tok.APIKey != "" {
		authToken = ""
		apiKey = tok.APIKey
	}
	betaHeaders := []string{
		"claude-code-20250219",
		"oauth-2025-04-20",
		"interleaved-thinking-2025-05-14",
		"thinking-token-count-2026-05-13",
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"advisor-tool-2026-03-01",
		"advanced-tool-use-2025-11-20",
		"effort-2025-11-24",
		"extended-cache-ttl-2025-04-11",
		"cache-diagnosis-2026-04-07",
	}
	if apiKey != "" {
		betaHeaders = removeString(betaHeaders, "oauth-2025-04-20")
	}
	cfg := api.Config{
		BaseURL:     auth.ProdConfig.BaseAPIURL,
		AuthToken:   authToken,
		APIKey:      apiKey,
		BetaHeaders: betaHeaders,
		SessionID:   NewSessionID(),
		UserAgent:   ua,
		ExtraHeaders: map[string]string{
			"anthropic-dangerous-direct-browser-access": "true",
			"X-Stainless-Retry-Count":                   "0",
			"X-Stainless-Timeout":                       "600",
		},
	}
	if apiKey != "" {
		// Console accounts authenticate with a minted API key, which has no refresh token.
		return api.NewClientWithProxy(cfg)
	}

	// The access token is fixed when the client is built, so a session that
	// outlives it would fail every turn. Refresh on 401 and swap the bearer in
	// place; the failed request is retried once by the client.
	var client *api.Client
	rejected := tok.AccessToken
	cfg.OnAuth401 = func(ctx context.Context) error {
		latest, err := refreshRejectedClaudeToken(ctx, account, rejected)
		if err != nil {
			return err
		}
		rejected = latest.AccessToken
		client.SetAuthToken(latest.AccessToken)
		return nil
	}
	// Use a proxy-aware transport when HTTPS_PROXY / HTTP_PROXY env vars are set.
	client = api.NewClientWithProxy(cfg)
	return client
}

// refreshRejectedClaudeToken renews the active Claude account's credential
// after the API rejected the access token rejected. An empty account means
// the active one.
func refreshRejectedClaudeToken(ctx context.Context, account, rejected string) (auth.PersistedTokens, error) {
	if account == "" {
		account = auth.ActiveEmail()
	}
	tc := auth.NewTokenClient(auth.ProdConfig, nil)
	return auth.RefreshRejected(ctx, secure.NewDefault(), tc, account, rejected, time.Now(), 5*time.Minute)
}

// modelVisionCatalog is the catalog consulted for image support, loaded once.
var modelVisionCatalog = sync.OnceValue(func() *catalog.Catalog { return catalog.Load(settings.ConduitDir()) })

// modelVision reports whether the catalog says model takes image input; known
// is false for a model the catalog does not list (local models, new releases).
func modelVision(model string) (vision, known bool) {
	info, ok := modelVisionCatalog().Lookup(model)
	return info.Vision, ok
}

// providerHeaderTimeout maps the provider setting (seconds, <=0 unset) to
// api.Config.ResponseHeaderTimeout: unset disables the limit.
func providerHeaderTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return -1
	}
	return time.Duration(seconds) * time.Second
}

func removeString(values []string, target string) []string {
	out := values[:0]
	for _, value := range values {
		if value != target {
			out = append(out, value)
		}
	}
	return out
}

func NewProviderAPIClient(provider settings.ActiveProviderSettings, store secure.Storage, wireVersion string) (*api.Client, error) {
	switch provider.Kind {
	case settings.ProviderKindOpenAICompatible:
		entrypoint := os.Getenv("CLAUDE_CODE_ENTRYPOINT")
		if entrypoint == "" {
			entrypoint = "sdk-cli"
		}
		baseURL := strings.TrimRight(provider.BaseURL, "/")
		if baseURL == "" {
			return nil, fmt.Errorf("provider %q missing baseURL", settings.ProviderKey(provider))
		}
		if strings.HasPrefix(provider.Credential, codex.ProviderID) || strings.Contains(baseURL, "chatgpt.com/backend-api/codex") {
			auth := codex.NewAuthorizerForCredential(store, provider.Credential)
			cred, err := auth.EnsureFresh(context.Background())
			if err != nil {
				return nil, fmt.Errorf("chatgpt/codex credential: %w", err)
			}
			accountID := ""
			if cred.Metadata != nil {
				accountID = cred.Metadata["account_id"]
			}
			var client *api.Client
			client = api.NewClientWithProxy(codexConfig(codexConfigParams{
				accessToken: cred.AccessToken,
				accountID:   accountID,
				wireVersion: wireVersion,
				userAgent:   fmt.Sprintf("conduit/%s (external, %s)", wireVersion, entrypoint),
				onAuth401: func(ctx context.Context) error {
					latest, err := auth.ForceRefresh(ctx)
					if err != nil {
						return err
					}
					client.SetAuthToken(latest.AccessToken)
					return nil
				},
			}))
			return client, nil
		}
		if strings.HasPrefix(provider.Credential, copilot.ProviderID) || strings.Contains(baseURL, "api.githubcopilot.com") {
			auth := copilot.NewAuthorizerForCredential(store, provider.Credential)
			cred, err := auth.EnsureFresh(context.Background())
			if err != nil {
				return nil, fmt.Errorf("github copilot credential: %w", err)
			}
			var client *api.Client
			// Business and Enterprise seats name their own API host at token
			// exchange; /models says which wire each model takes.
			baseURL = copilot.ResolveBaseURL(baseURL, cred)
			cfg := copilotConfig(provider.Model, baseURL, fmt.Sprintf("conduit/%s (external, %s)", wireVersion, entrypoint), copilot.ModelEndpoints(cred, provider.Model))
			cfg.AuthToken = cred.AccessToken
			cfg.ModelVision = modelVision
			cfg.OnAuth401 = func(ctx context.Context) error {
				if err := auth.Refresh(ctx); err != nil {
					return err
				}
				latest, err := auth.EnsureFresh(ctx)
				if err != nil {
					return err
				}
				client.SetAuthToken(latest.AccessToken)
				return nil
			}
			client = api.NewClientWithProxy(cfg)
			return client, nil
		}
		if isXAIOAuth(provider, baseURL, store) {
			auth := xai.NewAuthorizerForCredential(store, provider.Credential)
			cred, err := auth.EnsureFresh(context.Background())
			if err != nil {
				return nil, fmt.Errorf("grok/xai credential: %w", err)
			}
			var client *api.Client
			client = api.NewClientWithProxy(xaiConfig(cred.AccessToken, fmt.Sprintf("conduit/%s (external, %s)", wireVersion, entrypoint), func(ctx context.Context) error {
				latest, err := auth.ForceRefresh(ctx)
				if err != nil {
					return err
				}
				client.SetAuthToken(latest.AccessToken)
				return nil
			}))
			return client, nil
		}
		key, err := settings.LoadProviderCredential(store, provider.Credential)
		if err != nil {
			return nil, fmt.Errorf("provider credential %q: %w", provider.Credential, err)
		}
		return api.NewClientWithProxy(api.Config{
			ProviderKind:    api.ProviderKindOpenAICompatible,
			BaseURL:         baseURL,
			APIKey:          key,
			UserAgent:       fmt.Sprintf("conduit/%s (external, %s)", wireVersion, entrypoint),
			ExtraBody:       provider.ExtraBody,
			ReasoningReplay: provider.ReasoningReplay,
			ToolCallFormat:  provider.ToolCallFormat,
			ModelVision:     modelVision,
			// Unset means no limit: a generic server may prefill for minutes.
			ResponseHeaderTimeout: providerHeaderTimeout(provider.ResponseHeaderTimeout),
		}), nil
	default:
		return nil, fmt.Errorf("provider kind %q does not use a provider API client", provider.Kind)
	}
}

// isXAIOAuth reports whether the provider is a Grok account signed in with
// OAuth: a grok-xai credential alias, or the xAI API host with a structured
// (OAuth) credential rather than a plain API key.
func isXAIOAuth(provider settings.ActiveProviderSettings, baseURL string, store secure.Storage) bool {
	if strings.HasPrefix(provider.Credential, xai.ProviderID) {
		return true
	}
	if !strings.Contains(strings.ToLower(baseURL), "api.x.ai") {
		return false
	}
	_, err := settings.LoadStructuredProviderCredential(store, provider.Credential)
	return err == nil
}

// xaiConfig is the client config for the xAI chat-completions API with a Grok
// account's OAuth access token as the Bearer credential.
func xaiConfig(accessToken, userAgent string, onAuth401 func(context.Context) error) api.Config {
	return api.Config{
		ProviderKind: api.ProviderKindOpenAICompatible,
		BaseURL:      xai.BaseURL,
		AuthToken:    accessToken,
		UserAgent:    userAgent,
		ModelVision:  modelVision,
		OnAuth401:    onAuth401,
	}
}

type codexConfigParams struct {
	accessToken, accountID, wireVersion, userAgent string
	onAuth401                                      func(context.Context) error
}

// codexConfig is the client config for the ChatGPT/Codex Responses backend.
// Each request carries its conduit session id as session_id and
// prompt_cache_key (the client's own stable id when the request has none),
// asks for encrypted reasoning because store is false, and replays it.
func codexConfig(p codexConfigParams) api.Config {
	storeResponses := false
	clientSession := NewSessionID()
	return api.Config{
		ProviderKind:                        api.ProviderKindOpenAIResponses,
		BaseURL:                             codex.CodexBaseURL,
		AuthToken:                           p.accessToken,
		UserAgent:                           p.userAgent,
		SessionID:                           clientSession,
		ExtraHeaders:                        codex.Headers(p.accountID, "", p.wireVersion),
		RequestHeaders:                      codex.RequestHeaders(clientSession),
		OpenAIResponsesSystemAsInstructions: true,
		OpenAIResponsesOmitMaxOutputTokens:  true,
		OpenAIResponsesStore:                &storeResponses,
		OpenAIResponsesPromptCacheKey:       true,
		OpenAIResponsesReplayReasoning:      true,
		ModelVision:                         modelVision,
		OnAuth401:                           p.onAuth401,
	}
}

// copilotConfig is the client config for a Copilot model, without
// credentials. The route follows the model's supported_endpoints from /models
// (endpoints, empty when unknown, then the model-name heuristics):
// /responses, /v1/messages (at the account's Business/Enterprise origin when baseURL is
// one, else ChatBaseURL), or /chat/completions. Every route sends
// copilot.RequestHeaders: x-initiator, X-Interaction-Id and
// Copilot-Vision-Request are per request.
func copilotConfig(model, baseURL, userAgent string, endpoints []string) api.Config {
	switch copilot.RouteForModel(model, endpoints) {
	case copilot.RouteResponses:
		return api.Config{
			ProviderKind:   api.ProviderKindOpenAIResponses,
			BaseURL:        baseURL,
			UserAgent:      userAgent,
			ExtraHeaders:   copilot.ChatHeaders(),
			RequestHeaders: copilot.RequestHeaders,
		}
	case copilot.RouteMessages:
		return api.Config{
			BaseURL:                copilot.MessagesBaseURL(baseURL),
			UserAgent:              userAgent,
			BetaHeaders:            []string{"interleaved-thinking-2025-05-14"},
			ExtraHeaders:           copilot.MessagesHeaders(),
			RequestHeaders:         copilot.RequestHeaders,
			StripCacheControlScope: true,
		}
	default:
		return api.Config{
			ProviderKind:   api.ProviderKindOpenAICompatible,
			BaseURL:        baseURL,
			UserAgent:      userAgent,
			ExtraHeaders:   copilot.ChatHeaders(),
			RequestHeaders: copilot.RequestHeaders,
		}
	}
}

func FillProfileAccountFallback(p *profile.Info) {
	if p == nil || p.Email != "" {
		return
	}
	active := auth.ActiveEmail()
	if active == "" {
		return
	}
	store, err := auth.ListAccounts()
	if err != nil {
		return
	}
	if entry, ok := store.Accounts[active]; ok {
		if p.DisplayName == "" {
			p.DisplayName = entry.DisplayName
		}
		p.Email = entry.Email
		if p.OrganizationName == "" {
			p.OrganizationName = entry.OrganizationName
		}
		if p.SubscriptionType == "" {
			p.SubscriptionType = entry.SubscriptionType
		}
		return
	}
	for _, entry := range store.Accounts {
		if entry.Email != "" && active == entry.Email {
			p.Email = entry.Email
			return
		}
	}
}

func SaveProfileAccountMetadata(p profile.Info, kind string) {
	if p.Email == "" {
		return
	}
	_ = auth.SaveAccountProfile(p.Email, kind, p.DisplayName, p.OrganizationName, p.SubscriptionType)
}

func RefreshClaudeAccountProfiles(ctx context.Context) {
	store, err := auth.ListAccounts()
	if err != nil {
		return
	}
	secureStore := secure.NewDefault()
	tc := auth.NewTokenClient(auth.ProdConfig, nil)
	for id, entry := range store.Accounts {
		if entry.Kind != auth.AccountKindClaudeAI || entry.Email == "" {
			continue
		}
		tok, err := auth.EnsureFresh(ctx, secureStore, tc, id, time.Now(), 5*time.Minute)
		if err != nil || tok.AccessToken == "" {
			continue
		}
		p, _ := profile.Fetch(ctx, tok.AccessToken)
		if p.Email == "" {
			p.Email = entry.Email
		}
		SaveProfileAccountMetadata(p, auth.AccountKindClaudeAI)
	}
}

// LoadAuth loads and refreshes tokens for the active account.
func LoadAuth(ctx context.Context) (auth.PersistedTokens, error) {
	store := secure.NewDefault()
	cfg := auth.ProdConfig
	tc := auth.NewTokenClient(cfg, nil)
	return auth.EnsureFresh(ctx, store, tc, auth.ActiveEmail(), time.Now(), 5*time.Minute)
}
