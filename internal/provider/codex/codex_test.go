package codex

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/secure"
)

func testJWT(t *testing.T, payload map[string]any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "none"})
	body, _ := json.Marshal(payload)
	return base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body) + ".sig"
}

func TestExtractAccountID(t *testing.T) {
	tests := []struct {
		name string
		tok  *TokenResponse
		want string
	}{
		{
			name: "root claim from id token",
			tok:  &TokenResponse{IDToken: testJWT(t, map[string]any{"chatgpt_account_id": "acc-root"}), AccessToken: testJWT(t, map[string]any{"chatgpt_account_id": "acc-access"})},
			want: "acc-root",
		},
		{
			name: "nested auth claim",
			tok:  &TokenResponse{IDToken: testJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc-nested"}})},
			want: "acc-nested",
		},
		{
			name: "organization fallback",
			tok:  &TokenResponse{AccessToken: testJWT(t, map[string]any{"organizations": []map[string]string{{"id": "org-1"}}})},
			want: "org-1",
		},
		{
			name: "missing",
			tok:  &TokenResponse{IDToken: testJWT(t, map[string]any{"email": "me@example.com"})},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractAccountID(tt.tok); got != tt.want {
				t.Fatalf("ExtractAccountID = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildAuthorizeURL(t *testing.T) {
	oldIssuer := issuerURL
	issuerURL = func() string { return "https://auth.example.test" }
	defer func() { issuerURL = oldIssuer }()

	raw := BuildAuthorizeURL("http://localhost:1455/auth/callback", "challenge", "state")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.String() == "" || u.Scheme != "https" || u.Host != "auth.example.test" || u.Path != "/oauth/authorize" {
		t.Fatalf("url = %s", raw)
	}
	if q.Get("client_id") != ClientID || q.Get("code_challenge") != "challenge" || q.Get("state") != "state" {
		t.Fatalf("query = %#v", q)
	}
	if q.Get("codex_cli_simplified_flow") != "true" || q.Get("id_token_add_organizations") != "true" {
		t.Fatalf("missing codex params: %#v", q)
	}
	if q.Get("originator") != "opencode" {
		t.Fatalf("originator = %q, want opencode", q.Get("originator"))
	}
}

func TestRefreshTokenRequest(t *testing.T) {
	var captured string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		captured = r.Form.Encode()
		_, _ = w.Write([]byte(`{"access_token":"access","refresh_token":"refresh2","expires_in":3600}`))
	}))
	defer srv.Close()

	oldIssuer := issuerURL
	issuerURL = func() string { return srv.URL }
	defer func() { issuerURL = oldIssuer }()

	tok, err := RefreshToken(context.Background(), "refresh1")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "access" || tok.RefreshToken != "refresh2" {
		t.Fatalf("token = %#v", tok)
	}
	if !strings.Contains(captured, "grant_type=refresh_token") || !strings.Contains(captured, "refresh_token=refresh1") {
		t.Fatalf("form = %s", captured)
	}
}

func TestCredentialFromTokens(t *testing.T) {
	tok := &TokenResponse{
		IDToken:      testJWT(t, map[string]any{"chatgpt_account_id": "acc-123", "email": "me@example.com"}),
		AccessToken:  "access",
		RefreshToken: "refresh",
		ExpiresIn:    120,
	}
	before := time.Now()
	cred := CredentialFromTokens(tok)
	if cred.AccessToken != "access" || cred.RefreshToken != "refresh" {
		t.Fatalf("cred = %#v", cred)
	}
	if cred.Metadata["account_id"] != "acc-123" || cred.Metadata["email"] != "me@example.com" {
		t.Fatalf("metadata = %#v", cred.Metadata)
	}
	if cred.Expiry.Before(before.Add(90 * time.Second)) {
		t.Fatalf("expiry = %s, too soon", cred.Expiry)
	}
}

func TestIsAgenticModel(t *testing.T) {
	tests := map[string]bool{
		"gpt-5.5": true, "gpt-5.3-codex": true, "gpt-5.3-codex-spark": true, "gpt-6-sol": true, "openai/gpt-5.4-mini": true,
		"gpt-5-pro": false, "gpt-5.2-pro": false, "gpt-5.5-pro": false, "gpt-5-pro-2025-10-06": false,
		"gpt-5-chat-latest": false, "gpt-5-search-api": false, "gpt-5-codex-batch": false, "gpt-oss-120b": false,
		"gpt-realtime-5": false, "gpt-5-audio-preview": false, "gpt-4.1": false, "o3": false, "": false,
	}
	for id, want := range tests {
		if got := IsAgenticModel(id); got != want {
			t.Errorf("IsAgenticModel(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestContextWindowForModelCapsAtInputLimit(t *testing.T) {
	tests := []struct {
		model   string
		current int
		want    int
	}{
		{"gpt-5.5", 400000, InputLimit}, // advertised 400k, input limit 272k
		{"gpt-6-sol", 400000, InputLimit},
		{"gpt-6-sol", 128000, 128000},
		{"gpt-6-sol", 0, InputLimit},
	}
	for _, tt := range tests {
		if got := ContextWindowForModel(tt.model, tt.current); got != tt.want {
			t.Errorf("ContextWindowForModel(%q, %d) = %d, want %d", tt.model, tt.current, got, tt.want)
		}
	}
	for _, m := range FallbackModels() {
		if m.ContextWindow > InputLimit {
			t.Errorf("%s window %d exceeds the input limit", m.ID, m.ContextWindow)
		}
	}
}

func TestFetchModels(t *testing.T) {
	var gotAuth, gotAccount, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotAccount, gotVersion = r.Header.Get("Authorization"), r.Header.Get("ChatGPT-Account-Id"), r.URL.Query().Get("client_version")
		_, _ = w.Write([]byte(`{"models":[
			{"slug":"gpt-5.5","display_name":"GPT-5.5","visibility":"list","context_window":400000,"supported_reasoning_levels":[{"effort":"low"}]},
			{"slug":"gpt-5.5-pro","visibility":"list","context_window":400000},
			{"slug":"gpt-5.4-mini","visibility":"list","context_window":128000},
			{"slug":"gpt-5.3-codex","visibility":"hide","context_window":272000},
			{"slug":"codex-auto-review","visibility":"list"}]}`))
	}))
	defer srv.Close()
	old := modelsBaseURL
	modelsBaseURL = srv.URL
	defer func() { modelsBaseURL = old }()

	models, err := FetchModels(context.Background(), "tok", "acct")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" || gotAccount != "acct" || gotVersion == "" {
		t.Errorf("auth %q account %q version %q", gotAuth, gotAccount, gotVersion)
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
		if m.Provider != ProviderID || m.ContextWindow > InputLimit {
			t.Errorf("model %+v", m)
		}
	}
	if strings.Join(ids, ",") != "gpt-5.5,gpt-5.4-mini" {
		t.Errorf("ids = %v, want pro, hidden and non-gpt entries filtered", ids)
	}
	if models[0].ContextWindow != InputLimit || models[1].ContextWindow != 128000 || !models[0].Thinking || models[1].Thinking {
		t.Errorf("models = %+v", models)
	}
}

func TestFetchModelsErrorsFallBackToStatic(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"http error": func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) },
		"empty":      func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"models":[]}`)) },
		"garbage":    func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`<html>`)) },
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()
			old := modelsBaseURL
			modelsBaseURL = srv.URL
			defer func() { modelsBaseURL = old }()
			if _, err := FetchModels(context.Background(), "tok", ""); err == nil {
				t.Fatal("want an error so callers use the static list")
			}
		})
	}
	// No credential at all: LiveModelIDs errors, the static list stays available.
	store := secure.NewMemoryStorage()
	if _, err := NewAuthorizer(store).LiveModelIDs(context.Background()); err == nil {
		t.Error("LiveModelIDs without a credential should error")
	}
	if len(FallbackModelIDs()) == 0 {
		t.Error("static fallback empty")
	}
}

func TestRequestHeaders(t *testing.T) {
	h := RequestHeaders("client-id")
	tests := []struct {
		name string
		req  *api.MessageRequest
		want string
	}{
		{"hint wins", &api.MessageRequest{Hints: &api.WireHints{SessionID: "sess"}}, "sess"},
		{"no hints", &api.MessageRequest{}, "client-id"},
		{"nil request", nil, "client-id"},
		{"empty hint session", &api.MessageRequest{Hints: &api.WireHints{}}, "client-id"},
	}
	for _, tt := range tests {
		if got := h(tt.req)["session_id"]; got != tt.want {
			t.Errorf("%s: session_id = %q, want %q", tt.name, got, tt.want)
		}
	}
	if got := RequestHeaders("")(&api.MessageRequest{}); got != nil {
		t.Errorf("no id at all should send no header, got %v", got)
	}
}
