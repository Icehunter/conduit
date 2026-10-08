package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/providerauth"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

func TestRouteForModel(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		endpoints []string
		want      Route
	}{
		{"grok on /responses (name heuristic would say chat)", "grok-4.5", []string{"/chat/completions", "/responses"}, RouteResponses},
		{"gpt-6-astra", "gpt-6-astra", []string{"/responses"}, RouteResponses},
		{"claude on /v1/messages", "claude-sonnet-4.5", []string{"/chat/completions", "/v1/messages"}, RouteMessages},
		{"claude without /v1/messages goes to chat (name heuristic would say messages)", "claude-old", []string{"/chat/completions"}, RouteChat},
		{"chat only", "gpt-4.1", []string{"/chat/completions"}, RouteChat},
		{"gpt-5-mini keeps chat when offered", "gpt-5-mini", []string{"/chat/completions", "/responses"}, RouteChat},
		{"gpt-5-mini responses-only", "gpt-5-mini", []string{"/responses"}, RouteResponses},
		{"endpoint names are matched case-insensitively", "x", []string{"/Responses"}, RouteResponses},
		{"fallback: gpt-5", "gpt-5", nil, RouteResponses},
		{"fallback: gpt-5-mini", "gpt-5-mini", nil, RouteChat},
		{"fallback: claude", "claude-haiku-4.5", nil, RouteMessages},
		{"fallback: grok has no heuristic", "grok-4.5", nil, RouteChat},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RouteForModel(tt.model, tt.endpoints); got != tt.want {
				t.Errorf("RouteForModel(%q, %v) = %v, want %v", tt.model, tt.endpoints, got, tt.want)
			}
		})
	}
}

func TestValidAPIEndpoint(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://api.business.githubcopilot.com", "https://api.business.githubcopilot.com"},
		{"https://api.enterprise.githubcopilot.com/", "https://api.enterprise.githubcopilot.com"},
		{"https://copilot-api.acme.ghe.com", "https://copilot-api.acme.ghe.com"},
		{"http://api.business.githubcopilot.com", ""},
		{"https://evil.example.com", ""},
		{"https://githubcopilot.com.evil.com", ""},
		{"https://user:pw@api.business.githubcopilot.com", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got, ok := validAPIEndpoint(tt.in)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("validAPIEndpoint(%q) = %q, %v; want %q", tt.in, got, ok, tt.want)
		}
	}
}

func TestResolveBaseURL(t *testing.T) {
	biz := &providerauth.ProviderCredential{Metadata: map[string]string{metaAPIEndpoint: "https://api.business.githubcopilot.com"}}
	tests := []struct {
		name       string
		configured string
		cred       *providerauth.ProviderCredential
		want       string
	}{
		{"default URL moves to the seat's endpoint", ChatBaseURL, biz, "https://api.business.githubcopilot.com"},
		{"empty URL too", "", biz, "https://api.business.githubcopilot.com"},
		{"individual account keeps the default", ChatBaseURL + "/", &providerauth.ProviderCredential{}, ChatBaseURL},
		{"user-chosen URL wins", "https://proxy.example.com", biz, "https://proxy.example.com"},
		{"invalid stored endpoint is ignored", ChatBaseURL, &providerauth.ProviderCredential{Metadata: map[string]string{metaAPIEndpoint: "https://evil.example.com"}}, ChatBaseURL},
		{"nil cred", ChatBaseURL, nil, ChatBaseURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveBaseURL(tt.configured, tt.cred); got != tt.want {
				t.Errorf("ResolveBaseURL = %q, want %q", got, tt.want)
			}
		})
	}
	if got := MessagesBaseURL("https://api.business.githubcopilot.com"); got != "https://api.business.githubcopilot.com" {
		t.Errorf("MessagesBaseURL(business) = %q", got)
	}
	if got := MessagesBaseURL("https://example.test"); got != ChatBaseURL {
		t.Errorf("MessagesBaseURL(other) = %q, want the default", got)
	}
}

func TestExchangeStoresAPIEndpointAndKeepsModelEndpoints(t *testing.T) {
	store := secure.NewMemoryStorage()
	if err := settings.SaveStructuredProviderCredential(store, ProviderID, &providerauth.ProviderCredential{
		AccessToken: "old", RefreshToken: "gh", Expiry: time.Now().Add(-time.Hour),
		Metadata: map[string]string{metaModelEndpoints: `{"grok-4.5":["/responses"]}`},
	}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, api, want string
	}{
		{"business endpoint stored", "https://api.business.githubcopilot.com", "https://api.business.githubcopilot.com"},
		{"foreign endpoint dropped", "https://evil.example.com", ""},
		{"no endpoint", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"token": "new", "expires_at": time.Now().Add(time.Hour).Unix(),
					"endpoints": map[string]string{"api": tt.api},
				})
			}))
			defer srv.Close()
			withTestEndpoints(t, "", srv.URL, "")
			a := NewAuthorizer(store)
			if err := a.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			cred, err := settings.LoadStructuredProviderCredential(store, ProviderID)
			if err != nil {
				t.Fatal(err)
			}
			if cred.AccessToken != "new" || cred.Metadata[metaAPIEndpoint] != tt.want {
				t.Errorf("cred = %+v, want endpoint %q", cred, tt.want)
			}
			if got := ModelEndpoints(cred, "grok-4.5"); len(got) != 1 || got[0] != "/responses" {
				t.Errorf("model endpoints lost across refresh: %v", got)
			}
			// Expire it again for the next case.
			cred.Expiry = time.Now().Add(-time.Hour)
			if err := settings.SaveStructuredProviderCredential(store, ProviderID, cred); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRefresh403(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantErr     error
		wantDeleted bool
	}{
		{"account without Copilot drops the credential", `{"message":"not a copilot user"}`, ErrNotAvailable, true},
		{"model policy 403 keeps it", `{"error":{"code":"model_not_supported","message":"The requested model is not supported."}}`, ErrModelPolicy, false},
		{"policy wording keeps it", `{"message":"model policy has not been accepted"}`, ErrModelPolicy, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := secure.NewMemoryStorage()
			if err := settings.SaveStructuredProviderCredential(store, ProviderID, &providerauth.ProviderCredential{
				AccessToken: "old", RefreshToken: "gh", Expiry: time.Now().Add(-time.Hour),
			}); err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			withTestEndpoints(t, "", srv.URL, "")
			err := NewAuthorizer(store).Refresh(context.Background())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			_, loadErr := settings.LoadStructuredProviderCredential(store, ProviderID)
			if deleted := loadErr != nil; deleted != tt.wantDeleted {
				t.Errorf("credential deleted = %v (load err %v), want %v", deleted, loadErr, tt.wantDeleted)
			}
		})
	}
}

func TestFetchModelsRecordsEndpointsAndHonoursAPIEndpoint(t *testing.T) {
	store := secure.NewMemoryStorage()
	if err := settings.SaveStructuredProviderCredential(store, ProviderID, &providerauth.ProviderCredential{
		AccessToken: "tok", Expiry: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[
			{"id":"grok-4.5","name":"Grok","supported_endpoints":["/chat/completions","/responses"]},
			{"id":"gpt-4.1","name":"GPT-4.1","supported_endpoints":["/chat/completions"]},
			{"id":"no-list","name":"No list"}]}`))
	}))
	defer srv.Close()
	withTestEndpoints(t, "", "", srv.URL)
	if _, err := NewAuthorizer(store).FetchModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	cred, err := settings.LoadStructuredProviderCredential(store, ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken != "tok" {
		t.Errorf("saving endpoints clobbered the token: %+v", cred)
	}
	if got := strings.Join(ModelEndpoints(cred, "grok-4.5"), ","); got != "/chat/completions,/responses" {
		t.Errorf("grok endpoints = %q", got)
	}
	if got := ModelEndpoints(cred, "no-list"); got != nil {
		t.Errorf("no-list endpoints = %v, want nil so routing falls back to the name", got)
	}

	// modelsEndpoint: the default URL follows the seat's endpoint, an override does not.
	biz := &providerauth.ProviderCredential{Metadata: map[string]string{metaAPIEndpoint: "https://api.business.githubcopilot.com"}}
	if got := modelsEndpoint(biz); got != srv.URL {
		t.Errorf("overridden models URL changed: %q", got)
	}
	modelsURLFunc = func() string { return modelsURL }
	if got := modelsEndpoint(biz); got != "https://api.business.githubcopilot.com/models" {
		t.Errorf("modelsEndpoint(business) = %q", got)
	}
	if got := modelsEndpoint(&providerauth.ProviderCredential{}); got != modelsURL {
		t.Errorf("modelsEndpoint(individual) = %q", got)
	}
}
