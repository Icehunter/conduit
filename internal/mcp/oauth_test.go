package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/secure"
)

func TestDiscoverAuthServer_OAuthMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/oauth-authorization-server" {
			_ = json.NewEncoder(w).Encode(AuthServerMetadata{
				Issuer:                "https://example.com",
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
				RegistrationEndpoint:  "https://example.com/register",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	md, err := DiscoverAuthServer(context.Background(), srv.URL+"/v1/sse")
	if err != nil {
		t.Fatalf("DiscoverAuthServer: %v", err)
	}
	if md.AuthorizationEndpoint != "https://example.com/authorize" {
		t.Errorf("authorization_endpoint = %q", md.AuthorizationEndpoint)
	}
	if md.RegistrationEndpoint == "" {
		t.Errorf("expected registration_endpoint to be set")
	}
}

func TestDiscoverAuthServer_FollowsProtectedResourcePointer(t *testing.T) {
	// Two servers: an MCP origin that exposes only a protected-resource
	// document pointing at a separate authorization server origin.
	asMux := http.NewServeMux()
	var asURL string
	as := httptest.NewServer(asMux)
	defer as.Close()
	asURL = as.URL
	asMux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(AuthServerMetadata{
			AuthorizationEndpoint: asURL + "/authorize",
			TokenEndpoint:         asURL + "/token",
			RegistrationEndpoint:  asURL + "/register",
		})
	})

	mcpMux := http.NewServeMux()
	mcpMux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ProtectedResourceMetadata{
			Resource:             "https://mcp.example/mcp",
			AuthorizationServers: []string{asURL},
		})
	})
	mcp := httptest.NewServer(mcpMux)
	defer mcp.Close()

	md, err := DiscoverAuthServer(context.Background(), mcp.URL+"/mcp/")
	if err != nil {
		t.Fatalf("DiscoverAuthServer: %v", err)
	}
	if md.AuthorizationEndpoint != asURL+"/authorize" {
		t.Errorf("expected authorization_endpoint at %s; got %q", asURL, md.AuthorizationEndpoint)
	}
	if md.RegistrationEndpoint != asURL+"/register" {
		t.Errorf("registration_endpoint = %q; want %s/register", md.RegistrationEndpoint, asURL)
	}
}

func TestDiscoverAuthServer_FallsBackWhenNoProtectedResource(t *testing.T) {
	// Server has no protected-resource document but exposes RFC 8414 at
	// its own origin — discovery should still succeed via the fallback
	// path. (This is the common case for MCPs where server == AS.)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-protected-resource":
			http.NotFound(w, r)
		case "/.well-known/oauth-authorization-server":
			_ = json.NewEncoder(w).Encode(AuthServerMetadata{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	md, err := DiscoverAuthServer(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("DiscoverAuthServer: %v", err)
	}
	if md.TokenEndpoint != "https://example.com/token" {
		t.Errorf("fallback path failed: %+v", md)
	}
}

func TestDiscoverAuthServer_NoMetadataAnywhere(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, err := DiscoverAuthServer(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error when nothing is exposed; got nil")
	}
	if !strings.Contains(err.Error(), "does not expose") {
		t.Errorf("error should explain the discovery failure clearly; got %v", err)
	}
}

func TestDiscoverAuthServer_OIDCFallback(t *testing.T) {
	// First well-known returns 404 (RFC 8414 not supported); OIDC fallback wins.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = json.NewEncoder(w).Encode(AuthServerMetadata{
				AuthorizationEndpoint: "https://example.com/authorize",
				TokenEndpoint:         "https://example.com/token",
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	md, err := DiscoverAuthServer(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("DiscoverAuthServer with OIDC fallback: %v", err)
	}
	if md.TokenEndpoint != "https://example.com/token" {
		t.Errorf("expected fallback to OIDC discovery; got %+v", md)
	}
}

func TestDiscoverAuthServer_RejectsIncomplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "well-known") {
			// Metadata missing required endpoints.
			_, _ = w.Write([]byte(`{"issuer":"https://example.com"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, err := DiscoverAuthServer(context.Background(), srv.URL)
	if err == nil {
		t.Fatal("expected error for incomplete metadata; got nil")
	}
}

func TestRegisterClient_DCR(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST; got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["client_name"] != "conduit (test)" {
			t.Errorf("client_name = %v", body["client_name"])
		}
		_ = json.NewEncoder(w).Encode(ClientRegistration{
			ClientID:                "abc-123",
			TokenEndpointAuthMethod: "none",
		})
	}))
	defer srv.Close()

	reg, err := RegisterClient(context.Background(), srv.URL, "conduit (test)", []string{"http://127.0.0.1:42/callback"})
	if err != nil {
		t.Fatalf("RegisterClient: %v", err)
	}
	if reg.ClientID != "abc-123" {
		t.Errorf("client_id = %q", reg.ClientID)
	}
}

func TestAuthorizeURL_BuildsCorrectQuery(t *testing.T) {
	md := &AuthServerMetadata{AuthorizationEndpoint: "https://example.com/authorize"}
	got := AuthorizeURL(md, "client-id", "http://127.0.0.1:42/callback", "state-x", "challenge-y", []string{"a", "b"})
	for _, want := range []string{
		"https://example.com/authorize?",
		"response_type=code",
		"client_id=client-id",
		"redirect_uri=http%3A%2F%2F127.0.0.1%3A42%2Fcallback",
		"code_challenge=challenge-y",
		"code_challenge_method=S256",
		"state=state-x",
		"scope=a+b",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("URL missing %q: %s", want, got)
		}
	}
}

func TestAuthorizeURL_PreservesExistingQuery(t *testing.T) {
	md := &AuthServerMetadata{AuthorizationEndpoint: "https://example.com/authorize?prompt=consent"}
	got := AuthorizeURL(md, "x", "http://127.0.0.1:1/cb", "s", "c", nil)
	if !strings.Contains(got, "?prompt=consent&") {
		t.Errorf("existing query parameter not preserved: %s", got)
	}
}

func TestExchangeCode_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("code_verifier") != "verifier-xyz" {
			t.Errorf("code_verifier = %q", r.Form.Get("code_verifier"))
		}
		_, _ = w.Write([]byte(`{"access_token":"AT","refresh_token":"RT","token_type":"Bearer","expires_in":3600,"scope":"read"}`))
	}))
	defer srv.Close()

	tokens, err := ExchangeCode(context.Background(), srv.URL, "code-x", "http://127.0.0.1:1/cb", "client-1", "verifier-xyz")
	if err != nil {
		t.Fatalf("ExchangeCode: %v", err)
	}
	if tokens.AccessToken != "AT" || tokens.RefreshToken != "RT" || tokens.TokenType != "Bearer" {
		t.Errorf("tokens = %+v", tokens)
	}
	if tokens.ExpiresAt.IsZero() {
		t.Error("ExpiresAt should be populated from expires_in")
	}
}

func TestExchangeCode_SurfacesOAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"code expired"}`))
	}))
	defer srv.Close()

	_, err := ExchangeCode(context.Background(), srv.URL, "x", "http://x/cb", "c", "v")
	if err == nil {
		t.Fatal("expected error; got nil")
	}
	if !strings.Contains(err.Error(), "invalid_grant") || !strings.Contains(err.Error(), "code expired") {
		t.Errorf("error didn't surface AS message: %v", err)
	}
}

func TestRefreshToken_Roundtrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "old-rt" {
			t.Errorf("refresh_token = %q", r.Form.Get("refresh_token"))
		}
		_, _ = w.Write([]byte(`{"access_token":"new-AT","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()

	tokens, err := RefreshToken(context.Background(), srv.URL, "old-rt", "client-1")
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if tokens.AccessToken != "new-AT" {
		t.Errorf("AccessToken = %q", tokens.AccessToken)
	}
}

// --- persistence tests ---

func TestSaveLoadServerToken_RoundTrip(t *testing.T) {
	s := secure.NewMemoryStorage()
	in := &OAuthTokens{
		AccessToken:  "AT",
		RefreshToken: "RT",
		TokenType:    "Bearer",
	}
	if err := SaveServerToken(s, "atlassian", in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := LoadServerToken(s, "atlassian")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.AccessToken != "AT" || out.RefreshToken != "RT" {
		t.Errorf("round-trip mismatch: %+v", out)
	}
}

func TestSaveLoadServerToken_DistinctServers(t *testing.T) {
	s := secure.NewMemoryStorage()
	_ = SaveServerToken(s, "a", &OAuthTokens{AccessToken: "ta"})
	_ = SaveServerToken(s, "b", &OAuthTokens{AccessToken: "tb"})

	a, _ := LoadServerToken(s, "a")
	b, _ := LoadServerToken(s, "b")
	if a.AccessToken != "ta" || b.AccessToken != "tb" {
		t.Errorf("server tokens cross-contaminated: a=%q b=%q", a.AccessToken, b.AccessToken)
	}
}

func TestDeleteServerToken_Idempotent(t *testing.T) {
	s := secure.NewMemoryStorage()
	if err := DeleteServerToken(s, "missing"); err != nil {
		t.Errorf("Delete on absent server should succeed; got %v", err)
	}
	_ = SaveServerToken(s, "x", &OAuthTokens{AccessToken: "AT"})
	if err := DeleteServerToken(s, "x"); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if _, err := LoadServerToken(s, "x"); err == nil {
		t.Errorf("Load after Delete should error")
	}
}
