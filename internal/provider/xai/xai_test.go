package xai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/providerauth"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

func testJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "none"})
	body, _ := json.Marshal(payload)
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body) + ".sig"
}

// stubIssuer points the package at srv for the OAuth and API endpoints.
func stubIssuer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	oldIssuer, oldAPI, oldClient := issuerURL, apiBaseURL, httpClient
	issuerURL = func() string { return srv.URL }
	apiBaseURL = func() string { return srv.URL + "/v1" }
	httpClient = srv.Client()
	t.Cleanup(func() { issuerURL, apiBaseURL, httpClient = oldIssuer, oldAPI, oldClient })
}

func TestBuildAuthorizeURL(t *testing.T) {
	raw := BuildAuthorizeURL("http://127.0.0.1:5555/callback", "chal", "st", "no")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme+"://"+u.Host+u.Path != Issuer+"/oauth2/authorize" {
		t.Fatalf("url = %s", raw)
	}
	q := u.Query()
	want := map[string]string{
		"response_type": "code", "client_id": ClientID, "redirect_uri": "http://127.0.0.1:5555/callback",
		"scope": Scope, "code_challenge": "chal", "code_challenge_method": "S256",
		"state": "st", "nonce": "no", "referrer": "conduit",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	for _, scope := range []string{"offline_access", "api:access", "grok-cli:access"} {
		if !strings.Contains(q.Get("scope"), scope) {
			t.Errorf("scope missing %s", scope)
		}
	}
}

func TestExchangeAndRefresh(t *testing.T) {
	var forms []url.Values
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/token" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		forms = append(forms, r.PostForm)
		if r.PostForm.Get("grant_type") == "refresh_token" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at2", "expires_in": 3600})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at1", "refresh_token": "rt1", "expires_in": 3600})
	}))
	defer srv.Close()
	stubIssuer(t, srv)

	tok, err := ExchangeCode(context.Background(), "code", "http://127.0.0.1:1/callback", "ver")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at1" || tok.RefreshToken != "rt1" {
		t.Fatalf("tokens = %+v", tok)
	}
	f := forms[0]
	if f.Get("grant_type") != "authorization_code" || f.Get("code") != "code" || f.Get("code_verifier") != "ver" || f.Get("client_id") != ClientID {
		t.Fatalf("exchange form = %v", f)
	}

	next, err := RefreshToken(context.Background(), "rt1")
	if err != nil {
		t.Fatal(err)
	}
	if next.AccessToken != "at2" || next.RefreshToken != "rt1" {
		t.Fatalf("refresh should keep the old refresh token: %+v", next)
	}
	if forms[1].Get("refresh_token") != "rt1" || forms[1].Get("client_id") != ClientID {
		t.Fatalf("refresh form = %v", forms[1])
	}
}

func TestTokenRequestInvalidGrant(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant", "error_description": "refresh token revoked"})
	}))
	defer srv.Close()
	stubIssuer(t, srv)
	_, err := RefreshToken(context.Background(), "rt")
	if err == nil || !IsInvalidGrant(err) || !strings.Contains(err.Error(), "refresh token revoked") {
		t.Fatalf("err = %v", err)
	}
}

func TestCredentialFromTokensReadsIDToken(t *testing.T) {
	cred := CredentialFromTokens(&TokenResponse{
		AccessToken: "a", RefreshToken: "r", ExpiresIn: 60,
		IDToken: testJWT(t, map[string]any{"sub": "user-1", "email": "me@example.com"}),
	})
	if cred.Metadata["account_id"] != "user-1" || cred.Metadata["email"] != "me@example.com" {
		t.Fatalf("metadata = %v", cred.Metadata)
	}
	if d := time.Until(cred.Expiry); d <= 0 || d > time.Minute {
		t.Fatalf("expiry = %v", d)
	}
}

func saveCred(t *testing.T, store secure.Storage, name string, expiry time.Time, refresh string) {
	t.Helper()
	err := settings.SaveStructuredProviderCredential(store, name, &providerauth.ProviderCredential{
		AccessToken: "old", RefreshToken: refresh, Expiry: expiry, Metadata: map[string]string{"email": "me@example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestEnsureFreshRefreshesAndPersists(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new", "refresh_token": "rt-new", "expires_in": 3600})
	}))
	defer srv.Close()
	stubIssuer(t, srv)

	store := secure.NewMemoryStorage()
	a := NewAuthorizerForCredential(store, ProviderID+"-2")
	saveCred(t, store, ProviderID+"-2", time.Now().Add(time.Hour), "rt")
	cred, err := a.EnsureFresh(context.Background())
	if err != nil || cred.AccessToken != "old" || calls.Load() != 0 {
		t.Fatalf("fresh credential must not refresh: %v %+v calls=%d", err, cred, calls.Load())
	}

	saveCred(t, store, ProviderID+"-2", time.Now().Add(time.Minute), "rt")
	cred, err = a.EnsureFresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken != "new" || cred.Metadata["email"] != "me@example.com" || calls.Load() != 1 {
		t.Fatalf("cred = %+v calls=%d", cred, calls.Load())
	}
	stored, err := settings.LoadStructuredProviderCredential(store, ProviderID+"-2")
	if err != nil || stored.AccessToken != "new" || stored.RefreshToken != "rt-new" {
		t.Fatalf("stored = %+v err=%v", stored, err)
	}
	acct, err := a.GetAccount(context.Background())
	if err != nil || acct.DisplayName != "me@example.com" || acct.Method != providerauth.MethodOAuth {
		t.Fatalf("account = %+v err=%v", acct, err)
	}
}

func TestEnsureFreshMissingRefreshToken(t *testing.T) {
	store := secure.NewMemoryStorage()
	saveCred(t, store, ProviderID, time.Now().Add(-time.Hour), "")
	if _, err := NewAuthorizer(store).EnsureFresh(context.Background()); err == nil {
		t.Fatal("expected error for expired credential without refresh token")
	}
}

func TestBrowserFlowEndToEnd(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotForm = r.PostForm
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "refresh_token": "rt", "expires_in": 3600,
			"id_token": testJWT(t, map[string]any{"sub": "u1", "email": "a@b.c"}),
		})
	}))
	defer srv.Close()
	stubIssuer(t, srv)

	store := secure.NewMemoryStorage()
	a := NewAuthorizerForCredential(store, ProviderID)
	flow, err := StartBrowserFlow()
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	if !strings.HasPrefix(flow.RedirectURI(), "http://127.0.0.1:") || !strings.HasSuffix(flow.RedirectURI(), "/callback") {
		t.Fatalf("redirect = %s", flow.RedirectURI())
	}
	authURL, _ := url.Parse(flow.URL())
	state := authURL.Query().Get("state")

	// Preflight from the consent origin.
	req, _ := http.NewRequest(http.MethodOptions, flow.RedirectURI(), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("Access-Control-Allow-Origin") != accountsOrigin || resp.Header.Get("Access-Control-Allow-Private-Network") != "true" {
		t.Fatalf("preflight = %d %v", resp.StatusCode, resp.Header)
	}

	go func() {
		r, err := http.Get(flow.RedirectURI() + "?code=abc&state=" + url.QueryEscape(state))
		if err == nil {
			_ = r.Body.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cred, err := a.FinishBrowserFlow(ctx, flow)
	if err != nil {
		t.Fatal(err)
	}
	if gotForm.Get("code") != "abc" || gotForm.Get("redirect_uri") != flow.RedirectURI() || gotForm.Get("code_verifier") == "" {
		t.Fatalf("exchange form = %v", gotForm)
	}
	if cred.Metadata["email"] != "a@b.c" {
		t.Fatalf("metadata = %v", cred.Metadata)
	}
	if stored, err := settings.LoadStructuredProviderCredential(store, ProviderID); err != nil || stored.AccessToken != "at" {
		t.Fatalf("credential not persisted: %+v %v", stored, err)
	}
}

func TestBrowserFlowIgnoresStrayCallbacks(t *testing.T) {
	flow, err := StartBrowserFlow()
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	state := func() string { u, _ := url.Parse(flow.URL()); return u.Query().Get("state") }()
	for name, query := range map[string]string{
		"wrong state":   "?code=abc&state=wrong",
		"no code":       "?state=" + url.QueryEscape(state),
		"foreign error": "?error=access_denied&state=wrong",
		"no parameters": "",
	} {
		r, err := http.Get(flow.RedirectURI() + query)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
		if r.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, r.StatusCode)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := flow.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stray callbacks must not complete the flow, err = %v", err)
	}
}

func TestBrowserFlowReportsAuthorizationError(t *testing.T) {
	flow, err := StartBrowserFlow()
	if err != nil {
		t.Fatal(err)
	}
	defer flow.Close()
	u, _ := url.Parse(flow.URL())
	r, err := http.Get(flow.RedirectURI() + "?error=access_denied&error_description=no&state=" + url.QueryEscape(u.Query().Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	_ = r.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := flow.Wait(ctx); err == nil || !strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("err = %v", err)
	}
}

func TestForceRefreshIgnoresExpiry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "new", "refresh_token": "rt-new", "expires_in": 3600})
	}))
	defer srv.Close()
	stubIssuer(t, srv)
	store := secure.NewMemoryStorage()
	saveCred(t, store, ProviderID, time.Now().Add(time.Hour), "rt")
	cred, err := NewAuthorizer(store).ForceRefresh(context.Background())
	if err != nil || cred.AccessToken != "new" || calls.Load() != 1 {
		t.Fatalf("cred=%+v err=%v calls=%d", cred, err, calls.Load())
	}
}

func TestDeviceFlow(t *testing.T) {
	var polls atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/oauth2/device/code":
			if r.PostForm.Get("client_id") != ClientID || r.PostForm.Get("scope") != Scope {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(DeviceCode{DeviceCode: "dc", UserCode: "ABCD", VerificationURI: "https://accounts.x.ai/device", ExpiresIn: 60})
		case "/oauth2/token":
			if r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || r.PostForm.Get("device_code") != "dc" {
				http.Error(w, "bad grant", http.StatusBadRequest)
				return
			}
			if polls.Add(1) == 1 {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "refresh_token": "rt", "expires_in": 3600})
		}
	}))
	defer srv.Close()
	stubIssuer(t, srv)
	oldPoll := pollInterval
	pollInterval = time.Millisecond
	defer func() { pollInterval = oldPoll }()

	store := secure.NewMemoryStorage()
	var shown *DeviceCode
	cred, err := NewAuthorizer(store).AuthorizeDevice(context.Background(), func(dc *DeviceCode) { shown = dc })
	if err != nil {
		t.Fatal(err)
	}
	if shown == nil || shown.UserCode != "ABCD" || cred.AccessToken != "at" || polls.Load() != 2 {
		t.Fatalf("shown=%+v cred=%+v polls=%d", shown, cred, polls.Load())
	}
	if stored, err := settings.LoadStructuredProviderCredential(store, ProviderID); err != nil || stored.RefreshToken != "rt" {
		t.Fatalf("credential not persisted: %+v %v", stored, err)
	}
}

func TestPollForTokenTerminalErrors(t *testing.T) {
	for _, code := range []string{"access_denied", "expired_token"} {
		t.Run(code, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
			}))
			defer srv.Close()
			stubIssuer(t, srv)
			if _, err := PollForToken(context.Background(), "dc", 60, 0); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestFetchModels(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[
			{"id":"grok-4-fast","context_length":2000000,"completion_text_token_price":50,"prompt_image_token_price":20,"capabilities":{"reasoning_effort":["low","high"]}},
			{"id":"grok-code-fast-1","context_length":256000,"completion_text_token_price":20},
			{"id":"grok-imagine-image","image_price":5}
		]}`))
	}))
	defer srv.Close()
	stubIssuer(t, srv)

	models, err := FetchModels(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	byID := map[string]int{}
	for i, m := range models {
		byID[m.ID] = i
	}
	fast := models[byID["grok-4-fast"]]
	if fast.Name != "Grok 4 Fast" || fast.ContextWindow != 2000000 || !fast.Vision || !fast.Thinking || fast.Provider != ProviderID {
		t.Fatalf("grok-4-fast = %+v", fast)
	}
	code := models[byID["grok-code-fast-1"]]
	if code.Vision || code.Thinking {
		t.Fatalf("grok-code-fast-1 = %+v", code)
	}

	if _, err := FetchModels(context.Background(), "bad"); err == nil {
		t.Fatal("expected error for rejected token")
	}
}

func TestFetchModelsEmpty(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-imagine-image"}]}`))
	}))
	defer srv.Close()
	stubIssuer(t, srv)
	if _, err := FetchModels(context.Background(), "tok"); err == nil {
		t.Fatal("expected empty-catalog error")
	}
}

func TestLiveModelsUsesFreshToken(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer old" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"grok-4","completion_text_token_price":1}]}`))
	}))
	defer srv.Close()
	stubIssuer(t, srv)
	store := secure.NewMemoryStorage()
	saveCred(t, store, ProviderID, time.Now().Add(time.Hour), "rt")
	models, err := NewAuthorizer(store).LiveModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "grok-4" {
		t.Fatalf("models=%+v err=%v", models, err)
	}
}

func TestContextWindowForModel(t *testing.T) {
	if got := ContextWindowForModel("anything", 123); got != 123 {
		t.Fatalf("explicit window = %d", got)
	}
	if got := ContextWindowForModel("unknown", 0); got != DefaultContextWindow {
		t.Fatalf("default window = %d", got)
	}
}
