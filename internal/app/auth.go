package app

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/profile"
	"github.com/icehunter/conduit/internal/provider/copilot"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

// NewAPIClient builds a configured API client using persisted account
// credentials. Claude.ai uses OAuth bearer auth; Anthropic Console uses the
// minted API key when available.
func NewAPIClient(tok auth.PersistedTokens, wireVersion string) *api.Client {
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
		"context-management-2025-06-27",
		"prompt-caching-scope-2026-01-05",
		"advisor-tool-2026-03-01",
		"advanced-tool-use-2025-11-20",
		"context-1m-2025-08-07",
		"effort-2025-11-24",
		"extended-cache-ttl-2025-04-11",
		"oidc-federation-2026-04-01",
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
	// Use a proxy-aware transport when HTTPS_PROXY / HTTP_PROXY env vars are set.
	return api.NewClientWithProxy(cfg)
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
		if strings.HasPrefix(provider.Credential, copilot.ProviderID) || strings.Contains(baseURL, "api.githubcopilot.com") {
			auth := copilot.NewAuthorizerForCredential(store, provider.Credential)
			cred, err := auth.EnsureFresh(context.Background())
			if err != nil {
				return nil, fmt.Errorf("github copilot credential: %w", err)
			}
			var client *api.Client
			client = api.NewClientWithProxy(api.Config{
				ProviderKind: settings.ProviderKindOpenAICompatible,
				BaseURL:      baseURL,
				AuthToken:    cred.AccessToken,
				UserAgent:    fmt.Sprintf("conduit/%s (external, %s)", wireVersion, entrypoint),
				ExtraHeaders: copilot.Headers(),
				OnAuth401: func(ctx context.Context) error {
					if err := auth.Refresh(ctx); err != nil {
						return err
					}
					latest, err := auth.EnsureFresh(ctx)
					if err != nil {
						return err
					}
					client.SetAuthToken(latest.AccessToken)
					return nil
				},
			})
			return client, nil
		}
		key, err := settings.LoadProviderCredential(store, provider.Credential)
		if err != nil {
			return nil, fmt.Errorf("provider credential %q: %w", provider.Credential, err)
		}
		return api.NewClientWithProxy(api.Config{
			ProviderKind: settings.ProviderKindOpenAICompatible,
			BaseURL:      baseURL,
			APIKey:       key,
			UserAgent:    fmt.Sprintf("conduit/%s (external, %s)", wireVersion, entrypoint),
		}), nil
	default:
		return nil, fmt.Errorf("provider kind %q does not use a provider API client", provider.Kind)
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
