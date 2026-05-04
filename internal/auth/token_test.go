package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExchangeCodeForTokens_RequestShape asserts our token-exchange request
// matches decoded/1220.js function rm6 byte-for-byte:
//
//   POST <TOKEN_URL>
//   Content-Type: application/json
//   {
//     "grant_type": "authorization_code",
//     "code": "<auth code>",
//     "redirect_uri": "http://localhost:<port>/callback" or MANUAL_REDIRECT_URL,
//     "client_id": "<client_id>",
//     "code_verifier": "<verifier>",
//     "state": "<state>"
//   }
func TestExchangeCodeForTokens_RequestShape(t *testing.T) {
	type wantBody struct {
		GrantType    string `json:"grant_type"`
		Code         string `json:"code"`
		RedirectURI  string `json:"redirect_uri"`
		ClientID     string `json:"client_id"`
		CodeVerifier string `json:"code_verifier"`
		State        string `json:"state"`
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s; want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q; want application/json", ct)
		}
		var got wantBody
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if got.GrantType != "authorization_code" {
			t.Errorf("grant_type = %q", got.GrantType)
		}
		if got.Code != "auth-code-xyz" {
			t.Errorf("code = %q", got.Code)
		}
		if got.RedirectURI != "http://localhost:53412/callback" {
			t.Errorf("redirect_uri = %q", got.RedirectURI)
		}
		if got.ClientID != ProdConfig.ClientID {
			t.Errorf("client_id = %q", got.ClientID)
		}
		if got.CodeVerifier != "verifier-abc" {
			t.Errorf("code_verifier = %q", got.CodeVerifier)
		}
		if got.State != "state-123" {
			t.Errorf("state = %q", got.State)
		}
		_, _ = io.WriteString(w, `{
			"access_token": "at-1",
			"refresh_token": "rt-1",
			"expires_in": 3600,
			"token_type": "bearer",
			"scope": "user:profile user:inference"
		}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := ProdConfig
	cfg.TokenURL = srv.URL + "/v1/oauth/token"

	c := NewTokenClient(cfg, srv.Client())

	tok, err := c.ExchangeCodeForTokens(context.Background(), ExchangeParams{
		Code:         "auth-code-xyz",
		State:        "state-123",
		CodeVerifier: "verifier-abc",
		Port:         53412,
		UseManual:    false,
	})
	if err != nil {
		t.Fatalf("ExchangeCodeForTokens: %v", err)
	}
	if tok.AccessToken != "at-1" {
		t.Errorf("AccessToken = %q", tok.AccessToken)
	}
	if tok.RefreshToken != "rt-1" {
		t.Errorf("RefreshToken = %q", tok.RefreshToken)
	}
	if tok.ExpiresIn != 3600 {
		t.Errorf("ExpiresIn = %d", tok.ExpiresIn)
	}
	wantScopes := []string{"user:profile", "user:inference"}
	if len(tok.Scopes) != len(wantScopes) {
		t.Fatalf("Scopes = %v; want %v", tok.Scopes, wantScopes)
	}
	for i, s := range wantScopes {
		if tok.Scopes[i] != s {
			t.Errorf("Scopes[%d] = %q; want %q", i, tok.Scopes[i], s)
		}
	}
}

// TestExchangeCodeForTokens_ManualRedirect verifies the manual-paste flow uses
// MANUAL_REDIRECT_URL instead of the localhost callback.
func TestExchangeCodeForTokens_ManualRedirect(t *testing.T) {
	var sawRedirect string
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sawRedirect, _ = body["redirect_uri"].(string)
		_, _ = io.WriteString(w, `{"access_token":"a","refresh_token":"r","expires_in":1,"token_type":"bearer","scope":""}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := ProdConfig
	cfg.TokenURL = srv.URL + "/v1/oauth/token"
	c := NewTokenClient(cfg, srv.Client())

	_, err := c.ExchangeCodeForTokens(context.Background(), ExchangeParams{
		Code:         "code",
		State:        "state",
		CodeVerifier: "v",
		UseManual:    true,
	})
	if err != nil {
		t.Fatalf("ExchangeCodeForTokens: %v", err)
	}
	if sawRedirect != ProdConfig.ManualRedirectURL {
		t.Errorf("redirect_uri = %q; want %q", sawRedirect, ProdConfig.ManualRedirectURL)
	}
}

// TestExchangeCodeForTokens_Rejects401 maps to decoded/1220.js:79-84:
//   $.status === 401 -> "Authentication failed: Invalid authorization code"
func TestExchangeCodeForTokens_Rejects401(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"invalid_grant"}`, http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := ProdConfig
	cfg.TokenURL = srv.URL + "/v1/oauth/token"
	c := NewTokenClient(cfg, srv.Client())

	_, err := c.ExchangeCodeForTokens(context.Background(), ExchangeParams{
		Code: "x", State: "y", CodeVerifier: "v", Port: 1,
	})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "Invalid authorization code") {
		t.Errorf("error = %v; want contains 'Invalid authorization code'", err)
	}
}

// TestExchangeCodeForTokens_RejectsNonBearer enforces decoded/1390.js:38:
//   token_type && token_type.toLowerCase() !== "bearer" -> error
func TestExchangeCodeForTokens_RejectsNonBearer(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"access_token": "x",
			"refresh_token": "r",
			"expires_in": 1,
			"token_type": "MAC",
			"scope": ""
		}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := ProdConfig
	cfg.TokenURL = srv.URL + "/v1/oauth/token"
	c := NewTokenClient(cfg, srv.Client())

	_, err := c.ExchangeCodeForTokens(context.Background(), ExchangeParams{
		Code: "x", State: "y", CodeVerifier: "v", Port: 1,
	})
	if err == nil {
		t.Fatal("expected error on non-bearer token_type")
	}
	if !strings.Contains(err.Error(), "bearer") {
		t.Errorf("error = %v; should mention bearer", err)
	}
}

// TestRefreshOAuthToken_RequestShape mirrors decoded/1220.js:87-100 (function r__).
//
//   POST <TOKEN_URL>
//   {
//     "grant_type": "refresh_token",
//     "refresh_token": "<rt>",
//     "client_id": "<cid>",
//     "scope": "<space-joined claude_ai scopes>"
//   }
func TestRefreshOAuthToken_RequestShape(t *testing.T) {
	type wantBody struct {
		GrantType    string `json:"grant_type"`
		RefreshToken string `json:"refresh_token"`
		ClientID     string `json:"client_id"`
		Scope        string `json:"scope"`
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		var got wantBody
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		if got.GrantType != "refresh_token" {
			t.Errorf("grant_type = %q", got.GrantType)
		}
		if got.RefreshToken != "old-rt" {
			t.Errorf("refresh_token = %q", got.RefreshToken)
		}
		if got.ClientID != ProdConfig.ClientID {
			t.Errorf("client_id = %q", got.ClientID)
		}
		// Default refresh request uses the claude_ai scope set, space-joined.
		want := strings.Join(ScopesClaudeAI, " ")
		if got.Scope != want {
			t.Errorf("scope = %q; want %q", got.Scope, want)
		}
		_, _ = io.WriteString(w, `{
			"access_token": "new-at",
			"refresh_token": "new-rt",
			"expires_in": 3600,
			"token_type": "bearer",
			"scope": "user:profile user:inference"
		}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := ProdConfig
	cfg.TokenURL = srv.URL + "/v1/oauth/token"
	c := NewTokenClient(cfg, srv.Client())

	tok, err := c.RefreshOAuthToken(context.Background(), "old-rt", RefreshOptions{})
	if err != nil {
		t.Fatalf("RefreshOAuthToken: %v", err)
	}
	if tok.AccessToken != "new-at" {
		t.Errorf("AccessToken = %q", tok.AccessToken)
	}
}

// TestRefreshOAuthToken_KeepsOldRefreshTokenIfOmitted mirrors
// decoded/1220.js:102: `refresh_token: z = H` — when the response omits
// refresh_token, the previous one is reused.
func TestRefreshOAuthToken_KeepsOldRefreshTokenIfOmitted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"access_token": "new-at",
			"expires_in": 3600,
			"token_type": "bearer",
			"scope": ""
		}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := ProdConfig
	cfg.TokenURL = srv.URL + "/v1/oauth/token"
	c := NewTokenClient(cfg, srv.Client())

	tok, err := c.RefreshOAuthToken(context.Background(), "preserved-rt", RefreshOptions{})
	if err != nil {
		t.Fatalf("RefreshOAuthToken: %v", err)
	}
	if tok.RefreshToken != "preserved-rt" {
		t.Errorf("RefreshToken = %q; want preserved-rt", tok.RefreshToken)
	}
}

// TestExchangeCodeForTokens_EmailFromAccount verifies that account.email_address
// in the token response is surfaced as Tokens.Email, avoiding the extra profile
// round-trip for accounts that include it (e.g. Teams).
func TestExchangeCodeForTokens_EmailFromAccount(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"access_token": "at-1",
			"refresh_token": "rt-1",
			"expires_in": 3600,
			"token_type": "bearer",
			"scope": "user:profile user:inference",
			"account": {
				"uuid": "acct-uuid-123",
				"email_address": "user@example.com"
			}
		}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := ProdConfig
	cfg.TokenURL = srv.URL + "/v1/oauth/token"
	c := NewTokenClient(cfg, srv.Client())

	tok, err := c.ExchangeCodeForTokens(context.Background(), ExchangeParams{
		Code:         "code",
		State:        "state",
		CodeVerifier: "verifier",
		Port:         12345,
	})
	if err != nil {
		t.Fatalf("ExchangeCodeForTokens: %v", err)
	}
	if tok.Email != "user@example.com" {
		t.Errorf("Email = %q; want user@example.com", tok.Email)
	}
}

// TestExchangeCodeForTokens_EmailAbsent verifies that Tokens.Email is empty
// when the token response omits the account field (e.g. Max subscribers).
func TestExchangeCodeForTokens_EmailAbsent(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"access_token": "at-1",
			"refresh_token": "rt-1",
			"expires_in": 3600,
			"token_type": "bearer",
			"scope": "user:profile user:inference"
		}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := ProdConfig
	cfg.TokenURL = srv.URL + "/v1/oauth/token"
	c := NewTokenClient(cfg, srv.Client())

	tok, err := c.ExchangeCodeForTokens(context.Background(), ExchangeParams{
		Code:         "code",
		State:        "state",
		CodeVerifier: "verifier",
		Port:         12345,
	})
	if err != nil {
		t.Fatalf("ExchangeCodeForTokens: %v", err)
	}
	if tok.Email != "" {
		t.Errorf("Email = %q; want empty", tok.Email)
	}
}

// TestRefreshOAuthToken_ContextCancel ensures we honor the caller's context.
func TestRefreshOAuthToken_ContextCancel(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := ProdConfig
	cfg.TokenURL = srv.URL + "/v1/oauth/token"
	c := NewTokenClient(cfg, srv.Client())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.RefreshOAuthToken(ctx, "rt", RefreshOptions{})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v; want context.Canceled in chain", err)
	}
}
