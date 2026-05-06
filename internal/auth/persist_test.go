package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/secure"
)

const testEmail = "test@example.com"

// isolateClaudeDir redirects config writes to temp dirs
// so tests don't pollute the real user's active account setting.
func isolateClaudeDir(t *testing.T) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
}

func TestDeleteForEmail_RemovesTokens(t *testing.T) {
	isolateClaudeDir(t)
	s := secure.NewMemoryStorage()
	in := PersistedTokens{AccessToken: "AT", RefreshToken: "RT"}
	if err := SaveForEmail(s, in, testEmail); err != nil {
		t.Fatalf("SaveForEmail: %v", err)
	}
	if err := DeleteForEmail(s, testEmail); err != nil {
		t.Fatalf("DeleteForEmail: %v", err)
	}
	_, err := LoadForEmail(s, testEmail)
	if !errors.Is(err, secure.ErrNotFound) {
		t.Errorf("after Delete, LoadForEmail should return ErrNotFound; got %v", err)
	}
}

func TestDeleteForEmail_IdempotentWhenAbsent(t *testing.T) {
	isolateClaudeDir(t)
	s := secure.NewMemoryStorage()
	if err := DeleteForEmail(s, testEmail); err != nil {
		t.Errorf("Delete on empty store should succeed; got %v", err)
	}
}

func TestSaveLoadForEmail_RoundTrip(t *testing.T) {
	isolateClaudeDir(t)
	s := secure.NewMemoryStorage()
	in := PersistedTokens{
		AccessToken:  "AT",
		RefreshToken: "RT",
		ExpiresAt:    time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		TokenType:    "bearer",
		Scopes:       []string{"user:profile", "user:inference"},
	}
	if err := SaveForEmail(s, in, testEmail); err != nil {
		t.Fatalf("SaveForEmail: %v", err)
	}
	out, err := LoadForEmail(s, testEmail)
	if err != nil {
		t.Fatalf("LoadForEmail: %v", err)
	}
	if out.AccessToken != in.AccessToken || out.RefreshToken != in.RefreshToken {
		t.Errorf("tokens mismatch: %+v", out)
	}
	if !out.ExpiresAt.Equal(in.ExpiresAt) {
		t.Errorf("ExpiresAt = %v; want %v", out.ExpiresAt, in.ExpiresAt)
	}
}

func TestSaveAccountStore_PreservesUnknownFieldsAndRejectsInvalidJSON(t *testing.T) {
	isolateClaudeDir(t)
	path := accountsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	before := []byte(`{"accounts":{"active":"old@example.com","accounts":{}},"external":{"keep":true}}`)
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}

	err := SaveAccountStore(AccountStore{
		Active: "new@example.com",
		Accounts: map[string]AccountEntry{
			"new@example.com": {
				Email:   "new@example.com",
				AddedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
			},
		},
	})
	if err != nil {
		t.Fatalf("SaveAccountStore: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(after, &raw); err != nil {
		t.Fatal(err)
	}
	var external map[string]bool
	if err := json.Unmarshal(raw["external"], &external); err != nil {
		t.Fatal(err)
	}
	if !external["keep"] {
		t.Fatalf("external field not preserved: %s", raw["external"])
	}
	var accounts AccountStore
	if err := json.Unmarshal(raw["accounts"], &accounts); err != nil {
		t.Fatal(err)
	}
	if accounts.Active != AccountID(AccountKindClaudeAI, "new@example.com") {
		t.Fatalf("active = %q, want normalized new@example.com", accounts.Active)
	}

	bad := []byte(`{"active":`)
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveAccountStore(AccountStore{Active: "next@example.com"}); err == nil {
		t.Fatal("SaveAccountStore should fail on invalid existing JSON")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != string(bad) {
		t.Fatalf("invalid accounts file was overwritten: %q", unchanged)
	}
}

func TestSaveForEmailKindPersistsAccountKind(t *testing.T) {
	isolateClaudeDir(t)
	store := secure.NewMemoryStorage()
	tokens := PersistedTokens{
		AccessToken:  "AT",
		RefreshToken: "RT",
		ExpiresAt:    time.Now().Add(time.Hour),
	}
	if err := SaveForEmailKind(store, tokens, "api@example.com", AccountKindAnthropicConsole); err != nil {
		t.Fatalf("SaveForEmailKind: %v", err)
	}

	got, err := LoadForEmail(store, "api@example.com")
	if err != nil {
		t.Fatalf("LoadForEmail: %v", err)
	}
	if got.AccountKind != AccountKindAnthropicConsole {
		t.Fatalf("token account kind = %q, want %q", got.AccountKind, AccountKindAnthropicConsole)
	}
	accounts, err := LoadAccountStore()
	if err != nil {
		t.Fatalf("LoadAccountStore: %v", err)
	}
	apiID := AccountID(AccountKindAnthropicConsole, "api@example.com")
	if accounts.Accounts[apiID].Kind != AccountKindAnthropicConsole {
		t.Fatalf("account kind = %q, want %q", accounts.Accounts[apiID].Kind, AccountKindAnthropicConsole)
	}
}

func TestSaveForEmailKindAllowsSameEmailAcrossKinds(t *testing.T) {
	isolateClaudeDir(t)
	store := secure.NewMemoryStorage()
	if err := SaveForEmailKind(store, PersistedTokens{AccessToken: "claude"}, "same@example.com", AccountKindClaudeAI); err != nil {
		t.Fatalf("SaveForEmailKind Claude: %v", err)
	}
	if err := SaveForEmailKind(store, PersistedTokens{AccessToken: "console"}, "same@example.com", AccountKindAnthropicConsole); err != nil {
		t.Fatalf("SaveForEmailKind Console: %v", err)
	}

	accounts, err := LoadAccountStore()
	if err != nil {
		t.Fatalf("LoadAccountStore: %v", err)
	}
	claudeID := AccountID(AccountKindClaudeAI, "same@example.com")
	consoleID := AccountID(AccountKindAnthropicConsole, "same@example.com")
	if _, ok := accounts.Accounts[claudeID]; !ok {
		t.Fatalf("missing Claude account: %#v", accounts.Accounts)
	}
	if _, ok := accounts.Accounts[consoleID]; !ok {
		t.Fatalf("missing Console account: %#v", accounts.Accounts)
	}
	claudeToken, err := LoadForEmail(store, claudeID)
	if err != nil {
		t.Fatalf("LoadForEmail Claude: %v", err)
	}
	consoleToken, err := LoadForEmail(store, consoleID)
	if err != nil {
		t.Fatalf("LoadForEmail Console: %v", err)
	}
	if claudeToken.AccessToken != "claude" || consoleToken.AccessToken != "console" {
		t.Fatalf("tokens crossed: claude=%q console=%q", claudeToken.AccessToken, consoleToken.AccessToken)
	}
}

func TestLoadAccountStore_ImportsLegacyClaudeAccounts(t *testing.T) {
	isolateClaudeDir(t)
	legacyPath, err := legacyAccountsPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := AccountStore{
		Active: "legacy@example.com",
		Accounts: map[string]AccountEntry{
			"legacy@example.com": {
				Email:   "legacy@example.com",
				AddedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
			},
		},
	}
	legacyData, _ := json.Marshal(legacy)
	if err := os.WriteFile(legacyPath, legacyData, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadAccountStore()
	if err != nil {
		t.Fatalf("LoadAccountStore: %v", err)
	}
	legacyID := AccountID(AccountKindClaudeAI, "legacy@example.com")
	if got.Active != legacyID || got.Accounts[legacyID].Email != "legacy@example.com" {
		t.Fatalf("imported account store = %#v", got)
	}

	conduitPath := accountsPath()
	data, err := os.ReadFile(conduitPath)
	if err != nil {
		t.Fatalf("read conduit config: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw["accounts"]) == 0 {
		t.Fatalf("conduit config missing accounts after import: %s", data)
	}
}

func TestLoadForEmail_NotFoundMaps(t *testing.T) {
	isolateClaudeDir(t)
	s := secure.NewMemoryStorage()
	_, err := LoadForEmail(s, testEmail)
	if !errors.Is(err, secure.ErrNotFound) {
		t.Fatalf("err = %v; want secure.ErrNotFound", err)
	}
}

func TestEnsureFresh_NotLoggedIn(t *testing.T) {
	isolateClaudeDir(t)
	s := secure.NewMemoryStorage()
	_, err := EnsureFresh(context.Background(), s, nil, testEmail, time.Now(), time.Minute)
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("err = %v; want ErrNotLoggedIn", err)
	}
}

func TestEnsureFresh_NoRefreshNeeded(t *testing.T) {
	isolateClaudeDir(t)
	s := secure.NewMemoryStorage()
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	_ = SaveForEmail(s, PersistedTokens{
		AccessToken:  "still-good",
		RefreshToken: "RT",
		ExpiresAt:    now.Add(1 * time.Hour),
	}, testEmail)
	out, err := EnsureFresh(context.Background(), s, nil, testEmail, now, 5*time.Minute)
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if out.AccessToken != "still-good" {
		t.Errorf("AccessToken = %q; want still-good", out.AccessToken)
	}
}

func TestEnsureFresh_RefreshesAndPersists(t *testing.T) {
	isolateClaudeDir(t)
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "refresh_token" {
			t.Errorf("grant_type = %v", body["grant_type"])
		}
		if body["refresh_token"] != "OLD_RT" {
			t.Errorf("refresh_token = %v", body["refresh_token"])
		}
		_, _ = io.WriteString(w, `{
			"access_token": "NEW_AT",
			"refresh_token": "NEW_RT",
			"expires_in": 7200,
			"token_type": "bearer",
			"scope": "user:profile user:inference"
		}`)
	}))
	defer tokenSrv.Close()

	cfg := ProdConfig
	cfg.TokenURL = tokenSrv.URL + "/v1/oauth/token"
	tc := NewTokenClient(cfg, tokenSrv.Client())

	s := secure.NewMemoryStorage()
	_ = SaveForEmail(s, PersistedTokens{
		AccessToken:  "OLD_AT",
		RefreshToken: "OLD_RT",
		ExpiresAt:    now.Add(-1 * time.Hour),
		Scopes:       []string{"user:profile", "user:inference"},
	}, testEmail)

	out, err := EnsureFresh(context.Background(), s, tc, testEmail, now, 5*time.Minute)
	if err != nil {
		t.Fatalf("EnsureFresh: %v", err)
	}
	if out.AccessToken != "NEW_AT" {
		t.Errorf("AccessToken = %q", out.AccessToken)
	}
	if out.RefreshToken != "NEW_RT" {
		t.Errorf("RefreshToken = %q", out.RefreshToken)
	}
	if !out.ExpiresAt.Equal(now.Add(2 * time.Hour)) {
		t.Errorf("ExpiresAt = %v; want %v", out.ExpiresAt, now.Add(2*time.Hour))
	}

	// Verify persistence.
	again, err := LoadForEmail(s, testEmail)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again.AccessToken != "NEW_AT" {
		t.Errorf("persisted AccessToken = %q", again.AccessToken)
	}
}
