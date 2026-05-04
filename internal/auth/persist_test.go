package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/secure"
)

const testEmail = "test@example.com"

func TestDeleteForEmail_RemovesTokens(t *testing.T) {
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
	s := secure.NewMemoryStorage()
	if err := DeleteForEmail(s, testEmail); err != nil {
		t.Errorf("Delete on empty store should succeed; got %v", err)
	}
}

func TestSaveLoadForEmail_RoundTrip(t *testing.T) {
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

func TestLoadForEmail_NotFoundMaps(t *testing.T) {
	s := secure.NewMemoryStorage()
	_, err := LoadForEmail(s, testEmail)
	if !errors.Is(err, secure.ErrNotFound) {
		t.Fatalf("err = %v; want secure.ErrNotFound", err)
	}
}

func TestEnsureFresh_NotLoggedIn(t *testing.T) {
	s := secure.NewMemoryStorage()
	_, err := EnsureFresh(context.Background(), s, nil, testEmail, time.Now(), time.Minute)
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("err = %v; want ErrNotLoggedIn", err)
	}
}

func TestEnsureFresh_NoRefreshNeeded(t *testing.T) {
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
