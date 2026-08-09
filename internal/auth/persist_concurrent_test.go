package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/secure"
)

// rotatingTokenServer models a token endpoint that rotates the refresh token on
// every exchange and rejects any refresh token it has already consumed — which
// is how Anthropic and OpenAI both behave. Reuse is what revokes a session, so
// the count of reuse attempts is the thing under test.
type rotatingTokenServer struct {
	*httptest.Server
	mu       sync.Mutex
	valid    map[string]bool
	issued   atomic.Int32
	reuseHit atomic.Int32
}

func newRotatingTokenServer(t *testing.T, initial string) *rotatingTokenServer {
	t.Helper()
	rs := &rotatingTokenServer{valid: map[string]bool{initial: true}}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refresh_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		rs.mu.Lock()
		ok := rs.valid[body.RefreshToken]
		if ok {
			delete(rs.valid, body.RefreshToken) // single use
		}
		rs.mu.Unlock()

		if !ok {
			rs.reuseHit.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_grant","error_description":"refresh token already used"}`)
			return
		}

		n := rs.issued.Add(1)
		next := fmt.Sprintf("refresh-%d", n)
		rs.mu.Lock()
		rs.valid[next] = true
		rs.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"access-%d","refresh_token":%q,"expires_in":3600,"scope":"user:inference"}`, n, next)
	}))
	t.Cleanup(rs.Close)
	return rs
}

// Two conduit processes sharing one credential file must not each burn the
// refresh token. Before the cross-process lock the second process read the
// stale credential, exchanged the same refresh token, and revoked the first
// process's session.
func TestEnsureFresh_ParallelProcessesDoNotReuseRefreshToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	srv := newRotatingTokenServer(t, "refresh-0")

	const email = "user@example.com"
	expired := PersistedTokens{
		AccessToken:  "access-0",
		RefreshToken: "refresh-0",
		ExpiresAt:    time.Now().Add(-time.Hour),
		Scopes:       []string{"user:inference"},
	}
	// Two independent FileStorage handles over one file: the closest we get to
	// two processes without forking.
	seed := secure.NewFileStorage(path)
	if err := saveToken(seed, expired, email); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg := ProdConfig
	cfg.TokenURL = srv.URL + "/v1/oauth/token"
	client := NewTokenClient(cfg, srv.Client())

	var wg sync.WaitGroup
	results := make([]PersistedTokens, 2)
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s := secure.NewFileStorage(path) // separate handle == separate process
			// ensureFreshOnce, not EnsureFresh: the singleflight group is
			// package-global, so going through EnsureFresh would coalesce these
			// two goroutines and model one process rather than two.
			results[i], errs[i] = ensureFreshOnce(context.Background(), s, client, email, time.Now(), time.Minute)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("EnsureFresh[%d]: %v", i, err)
		}
	}
	if got := srv.reuseHit.Load(); got != 0 {
		t.Errorf("refresh token was reused %d time(s); the peer's session would be revoked", got)
	}
	if got := srv.issued.Load(); got != 1 {
		t.Errorf("issued %d tokens, want exactly 1 exchange across both processes", got)
	}
	// Both callers must end up holding the same, valid credential.
	if results[0].AccessToken != results[1].AccessToken {
		t.Errorf("processes disagree: %q vs %q", results[0].AccessToken, results[1].AccessToken)
	}
	final, err := LoadForEmail(secure.NewFileStorage(path), email)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if final.AccessToken != results[0].AccessToken {
		t.Errorf("stored %q, callers hold %q", final.AccessToken, results[0].AccessToken)
	}
}

// A process whose in-memory view is stale must adopt the credential a peer
// already refreshed rather than exchanging its own dead token.
func TestEnsureFresh_AdoptsPeerRefreshedCredential(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "creds.json")
	srv := newRotatingTokenServer(t, "refresh-0")
	const email = "user@example.com"

	stale := secure.NewFileStorage(path)
	if err := saveToken(stale, PersistedTokens{
		AccessToken: "access-0", RefreshToken: "refresh-0",
		ExpiresAt: time.Now().Add(-time.Hour), Scopes: []string{"user:inference"},
	}, email); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Warm the stale handle's cache so it holds the pre-refresh view.
	if _, err := LoadForEmail(stale, email); err != nil {
		t.Fatalf("warm: %v", err)
	}

	// A peer refreshes first, through its own handle.
	peer := secure.NewFileStorage(path)
	cfg := ProdConfig
	cfg.TokenURL = srv.URL + "/v1/oauth/token"
	client := NewTokenClient(cfg, srv.Client())
	if _, err := EnsureFresh(context.Background(), peer, client, email, time.Now(), time.Minute); err != nil {
		t.Fatalf("peer refresh: %v", err)
	}

	// The stale handle must notice and adopt, not exchange again.
	got, err := EnsureFresh(context.Background(), stale, client, email, time.Now(), time.Minute)
	if err != nil {
		t.Fatalf("stale EnsureFresh: %v", err)
	}
	if srv.reuseHit.Load() != 0 {
		t.Errorf("stale process reused a consumed refresh token")
	}
	if srv.issued.Load() != 1 {
		t.Errorf("issued %d tokens, want 1 — the stale process should have adopted", srv.issued.Load())
	}
	if got.AccessToken != "access-1" {
		t.Errorf("adopted %q, want the peer's access-1", got.AccessToken)
	}
}
