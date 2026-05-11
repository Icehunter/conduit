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
