package app

import (
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/providerauth"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

func TestNewProviderAPIClientXAIOAuth(t *testing.T) {
	store := secure.NewMemoryStorage()
	if err := settings.SaveStructuredProviderCredential(store, "grok-xai", &providerauth.ProviderCredential{
		AccessToken: "grok-token", RefreshToken: "rt", Expiry: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	client, err := NewProviderAPIClient(settings.ActiveProviderSettings{
		Kind: settings.ProviderKindOpenAICompatible, Credential: "grok-xai", BaseURL: "https://api.x.ai/v1/", Model: "grok-4",
	}, store, "test")
	if err != nil || client == nil {
		t.Fatalf("client=%v err=%v", client, err)
	}
}

func TestIsXAIOAuth(t *testing.T) {
	store := secure.NewMemoryStorage()
	_ = settings.SaveProviderCredential(store, "xai-key", "plain-api-key-value")
	_ = settings.SaveStructuredProviderCredential(store, "renamed", &providerauth.ProviderCredential{AccessToken: "t", Expiry: time.Now().Add(time.Hour)})
	tests := []struct {
		name       string
		credential string
		baseURL    string
		want       bool
	}{
		{"alias prefix", "grok-xai-2", "https://api.x.ai/v1", true},
		{"renamed oauth account on xai host", "renamed", "https://api.x.ai/v1", true},
		{"api key on xai host", "xai-key", "https://api.x.ai/v1", false},
		{"other host", "renamed", "https://api.openai.com/v1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isXAIOAuth(settings.ActiveProviderSettings{Credential: tt.credential}, tt.baseURL, store)
			if got != tt.want {
				t.Fatalf("isXAIOAuth = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestXAIConfig(t *testing.T) {
	cfg := xaiConfig("tok", "ua", nil)
	if cfg.ProviderKind != api.ProviderKindOpenAICompatible || cfg.BaseURL != "https://api.x.ai/v1" || cfg.AuthToken != "tok" {
		t.Fatalf("cfg = %+v", cfg)
	}
}
