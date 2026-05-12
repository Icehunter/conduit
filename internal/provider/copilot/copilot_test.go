package copilot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/providerauth"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

func TestCopilotAuthorizer(t *testing.T) {
	store := secure.NewMemoryStorage()
	auth := NewAuthorizer(store)

	// Test ProviderID
	if auth.ProviderID() != "github-copilot" {
		t.Errorf("expected github-copilot, got %s", auth.ProviderID())
	}

	// Test Validate
	if err := auth.Validate(context.Background(), ""); err == nil {
		t.Error("expected error for empty credential")
	}
}

func TestPollTokenExchangesGitHubTokenForCopilotToken(t *testing.T) {
	store := secure.NewMemoryStorage()
	var pollCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/access_token":
			pollCount++
			w.Header().Set("Content-Type", "application/json")
			if pollCount == 1 {
				_ = json.NewEncoder(w).Encode(TokenResponse{Error: "authorization_pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(TokenResponse{AccessToken: "github-token"})
		case "/copilot_token":
			if got := r.Header.Get("Authorization"); got != "Bearer github-token" {
				t.Fatalf("Authorization = %q, want GitHub token bearer", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      "copilot-token",
				"expires_at": time.Now().Add(time.Hour).Unix(),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	withTestEndpoints(t, "", server.URL+"/access_token", server.URL+"/copilot_token", "")

	auth := NewAuthorizer(store)
	res, err := auth.PollToken(context.Background(), "device-code", 1)
	if err != nil {
		t.Fatalf("PollToken: %v", err)
	}
	if res.AccessToken != "copilot-token" || res.RefreshToken != "github-token" {
		t.Fatalf("token response = %#v, want Copilot access token with GitHub refresh token", res)
	}
	cred, err := settings.LoadStructuredProviderCredential(store, ProviderID)
	if err != nil {
		t.Fatalf("LoadStructuredProviderCredential: %v", err)
	}
	if cred.AccessToken != "copilot-token" || cred.RefreshToken != "github-token" {
		t.Fatalf("stored credential = %#v", cred)
	}
}

func TestFetchModelsDecodesOpenAIStyleList(t *testing.T) {
	store := secure.NewMemoryStorage()
	if err := settings.SaveStructuredProviderCredential(store, ProviderID, &providerauth.ProviderCredential{
		AccessToken: "copilot-token",
		Expiry:      time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("SaveStructuredProviderCredential: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer copilot-token" {
			t.Fatalf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": "gpt-4.1", "name": "GPT-4.1", "context_window": 128000, "supports_vision": true},
				{"id": "disabled-model", "disabled": true},
			},
		})
	}))
	defer server.Close()

	withTestEndpoints(t, "", "", "", server.URL)

	models, err := NewAuthorizer(store).FetchModels(context.Background())
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-4.1" || models[0].Provider != ProviderID || !models[0].Vision {
		t.Fatalf("models = %#v", models)
	}
}

func TestDecodeModelsUsesMaxPromptTokens(t *testing.T) {
	raw := strings.NewReader(`{
		"data": [{
			"id": "claude-haiku-4.5",
			"name": "Claude Haiku 4.5",
			"model_picker_enabled": true,
			"capabilities": {
				"limits": {
					"max_context_window_tokens": 144000,
					"max_prompt_tokens": 136000
				},
				"supports": {
					"tool_calls": true,
					"vision": true
				}
			}
		}]
	}`)
	models, err := decodeModels(raw)
	if err != nil {
		t.Fatalf("decodeModels: %v", err)
	}
	if got := models[0].ContextWindow; got != 136000 {
		t.Fatalf("ContextWindow = %d, want prompt-token limit 136000", got)
	}
}

func TestModelDiscoveryHeadersIncludeCopilotMetadata(t *testing.T) {
	headers := ModelDiscoveryHeaders()
	if headers["Copilot-Integration-Id"] == "" {
		t.Fatal("ModelDiscoveryHeaders missing Copilot-Integration-Id")
	}
	if headers["Editor-Version"] == "" {
		t.Fatal("ModelDiscoveryHeaders missing Editor-Version")
	}
	if headers["Editor-Plugin-Version"] == "" {
		t.Fatal("ModelDiscoveryHeaders missing Editor-Plugin-Version")
	}
	if headers["User-Agent"] == "" {
		t.Fatal("ModelDiscoveryHeaders missing User-Agent")
	}
	if headers["X-GitHub-Api-Version"] == "" {
		t.Fatal("ModelDiscoveryHeaders missing X-GitHub-Api-Version")
	}
	if headers["OpenAI-Intent"] == "" {
		t.Fatal("ModelDiscoveryHeaders missing OpenAI-Intent")
	}
}

func TestContextWindowForModelClampsOldCopilotEntries(t *testing.T) {
	if got := ContextWindowForModel("claude-haiku-4.5", 144000); got != 136000 {
		t.Fatalf("haiku context = %d, want 136000", got)
	}
	if got := ContextWindowForModel("gpt-5-mini", 264000); got != 128000 {
		t.Fatalf("gpt-5-mini context = %d, want 128000", got)
	}
}

func TestRuntimeRouteMappingMatchesOpenCode(t *testing.T) {
	tests := []struct {
		model         string
		wantMessages  bool
		wantResponses bool
	}{
		{model: "claude-haiku-4.5", wantMessages: true},
		{model: "claude-sonnet-4.5", wantMessages: true},
		{model: "gpt-5", wantResponses: true},
		{model: "gpt-5.1-codex", wantResponses: true},
		{model: "gpt-5-mini"},
		{model: "gpt-4.1"},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := UsesMessagesAPI(tt.model); got != tt.wantMessages {
				t.Fatalf("UsesMessagesAPI(%q) = %v, want %v", tt.model, got, tt.wantMessages)
			}
			if got := ShouldUseResponsesAPI(tt.model); got != tt.wantResponses {
				t.Fatalf("ShouldUseResponsesAPI(%q) = %v, want %v", tt.model, got, tt.wantResponses)
			}
		})
	}
}

func withTestEndpoints(t *testing.T, deviceURL, pollURL, tokenURL, modelURL string) {
	t.Helper()
	oldHTTPClient := httpClient
	oldDevice := deviceAuthURLFunc
	oldPoll := tokenPollURLFunc
	oldToken := tokenAPIURLFunc
	oldModels := modelsURLFunc
	httpClient = &http.Client{Timeout: 2 * time.Second}
	if deviceURL != "" {
		deviceAuthURLFunc = func() string { return deviceURL }
	}
	if pollURL != "" {
		tokenPollURLFunc = func() string { return pollURL }
	}
	if tokenURL != "" {
		tokenAPIURLFunc = func() string { return tokenURL }
	}
	if modelURL != "" {
		modelsURLFunc = func() string { return modelURL }
	}
	t.Cleanup(func() {
		httpClient = oldHTTPClient
		deviceAuthURLFunc = oldDevice
		tokenPollURLFunc = oldPoll
		tokenAPIURLFunc = oldToken
		modelsURLFunc = oldModels
	})
}
