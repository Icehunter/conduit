package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/secure"
)

// TestConnect_HTTP_401_MarksNeedsAuth verifies that an HTTP MCP server
// that returns 401 on initialize lands in StatusNeedsAuth (not Failed)
// so the McpAuthTool / /mcp auth flow can drive the user through OAuth.
func TestConnect_HTTP_401_MarksNeedsAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	m := NewManager()
	cfg := ServerConfig{Type: "http", URL: srv.URL, Source: "test"}
	m.connectWithCwd(context.Background(), "needsauth", cfg, "")

	servers := m.Servers()
	if len(servers) != 1 {
		t.Fatalf("expected 1 server; got %d", len(servers))
	}
	got := servers[0]
	if got.Status != StatusNeedsAuth {
		t.Errorf("Status = %q; want %q", got.Status, StatusNeedsAuth)
	}
	if !strings.Contains(got.Error, "OAuth required") {
		t.Errorf("Error message should hint at OAuth; got %q", got.Error)
	}

	pending := m.PendingNeedsAuth()
	if len(pending) != 1 || pending[0] != "needsauth" {
		t.Errorf("PendingNeedsAuth() = %v; want [needsauth]", pending)
	}
}

// TestConnect_HTTP_BearerInjected verifies that when a token is persisted
// for a server, the connect path sends Authorization: Bearer <token>.
func TestConnect_HTTP_BearerInjected(t *testing.T) {
	gotAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		// Respond 401 so the test exits without waiting on a full handshake.
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	store := secure.NewMemoryStorage()
	_ = SaveServerToken(store, "auth-srv", &OAuthTokens{AccessToken: "secret-token"})

	m := NewManager()
	m.SetSecureStore(store)
	cfg := ServerConfig{Type: "http", URL: srv.URL}
	m.connectWithCwd(context.Background(), "auth-srv", cfg, "")

	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization header = %q; want %q", gotAuth, "Bearer secret-token")
	}
}

// TestConnect_HTTP_NoBearerWhenNoStore confirms the 401 path still works
// when no secure store is wired (e.g. tests, environments without auth).
func TestConnect_HTTP_NoBearerWhenNoStore(t *testing.T) {
	gotAuth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	m := NewManager()
	cfg := ServerConfig{Type: "http", URL: srv.URL}
	m.connectWithCwd(context.Background(), "x", cfg, "")

	if gotAuth != "" {
		t.Errorf("expected no Authorization header without store; got %q", gotAuth)
	}
}
