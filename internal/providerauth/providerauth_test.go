package providerauth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/icehunter/conduit/internal/providerauth"
	"github.com/icehunter/conduit/internal/secure"
)

// ── Store tests ───────────────────────────────────────────────────────────────

func TestSaveLoadDeleteCredential(t *testing.T) {
	store := secure.NewMemoryStorage()

	// Load before save returns ErrMissingCredential.
	_, err := providerauth.LoadCredential(store, "openai")
	if !errors.Is(err, providerauth.ErrMissingCredential) {
		t.Fatalf("LoadCredential before save: got %v, want ErrMissingCredential", err)
	}

	// Save and load round-trip.
	if err := providerauth.SaveCredential(store, "openai", "sk-testkey123"); err != nil {
		t.Fatalf("SaveCredential: %v", err)
	}
	got, err := providerauth.LoadCredential(store, "openai")
	if err != nil {
		t.Fatalf("LoadCredential after save: %v", err)
	}
	if got != "sk-testkey123" {
		t.Errorf("LoadCredential: got %q, want %q", got, "sk-testkey123")
	}

	// Delete, then load returns ErrMissingCredential again.
	if err := providerauth.DeleteCredential(store, "openai"); err != nil {
		t.Fatalf("DeleteCredential: %v", err)
	}
	_, err = providerauth.LoadCredential(store, "openai")
	if !errors.Is(err, providerauth.ErrMissingCredential) {
		t.Fatalf("LoadCredential after delete: got %v, want ErrMissingCredential", err)
	}
}

func TestIsConnected(t *testing.T) {
	store := secure.NewMemoryStorage()

	if providerauth.IsConnected(store, "gemini") {
		t.Error("IsConnected before save: want false")
	}

	_ = providerauth.SaveCredential(store, "gemini", "AIza-test-1234567")
	if !providerauth.IsConnected(store, "gemini") {
		t.Error("IsConnected after save: want true")
	}

	_ = providerauth.DeleteCredential(store, "gemini")
	if providerauth.IsConnected(store, "gemini") {
		t.Error("IsConnected after delete: want false")
	}
}

func TestSaveCredential_validation(t *testing.T) {
	store := secure.NewMemoryStorage()

	tests := []struct {
		name       string
		providerID string
		credential string
		wantErr    bool
	}{
		{"empty providerID", "", "sk-key123", true},
		{"empty credential", "openai", "", true},
		{"nil store", "openai", "sk-key123", false}, // tested separately
		{"valid", "openrouter", "sk-or-testkey123456", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := providerauth.SaveCredential(store, tt.providerID, tt.credential)
			if (err != nil) != tt.wantErr {
				t.Errorf("SaveCredential() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSaveCredential_nilStore(t *testing.T) {
	err := providerauth.SaveCredential(nil, "openai", "sk-key123")
	if err == nil {
		t.Error("SaveCredential with nil store: want error")
	}
}

func TestLoadCredential_nilStore(t *testing.T) {
	_, err := providerauth.LoadCredential(nil, "openai")
	if err == nil {
		t.Error("LoadCredential with nil store: want error")
	}
}

// ── Builtin tests ─────────────────────────────────────────────────────────────

func TestBuiltinConfigs(t *testing.T) {
	configs := providerauth.BuiltinConfigs()
	if len(configs) == 0 {
		t.Fatal("BuiltinConfigs: want at least one config")
	}
	ids := map[string]bool{}
	for _, c := range configs {
		if c.ID == "" {
			t.Error("config with empty ID")
		}
		if c.DisplayName == "" {
			t.Errorf("config %q: empty DisplayName", c.ID)
		}
		if len(c.Methods) == 0 {
			t.Errorf("config %q: no methods", c.ID)
		}
		if ids[c.ID] {
			t.Errorf("duplicate config ID %q", c.ID)
		}
		ids[c.ID] = true
	}

	// Required built-ins must be present.
	for _, required := range []string{"openai", "gemini", "openrouter"} {
		if _, ok := providerauth.BuiltinByID(required); !ok {
			t.Errorf("BuiltinByID(%q): not found", required)
		}
	}
}

func TestBuiltinByID_unknown(t *testing.T) {
	_, ok := providerauth.BuiltinByID("notareal-provider")
	if ok {
		t.Error("BuiltinByID(unknown): want false")
	}
}

// ── APIKeyAuthorizer tests ────────────────────────────────────────────────────

func TestAPIKeyAuthorizer_Authorize(t *testing.T) {
	store := secure.NewMemoryStorage()
	a, err := providerauth.NewBuiltinAuthorizer("openai", store)
	if err != nil {
		t.Fatalf("NewBuiltinAuthorizer: %v", err)
	}

	tests := []struct {
		name    string
		kind    string
		key     string
		wantErr bool
	}{
		{"valid key", providerauth.MethodAPIKey, "sk-validkeyxxxxxx", false},
		{"wrong method", "oauth", "sk-validkeyxxxxxx", true},
		{"empty key", providerauth.MethodAPIKey, "", true},
		{"short key", providerauth.MethodAPIKey, "sk-x", true},
		{"key with spaces", providerauth.MethodAPIKey, "sk- space here", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := a.Authorize(context.Background(), tt.kind, map[string]string{"key": tt.key})
			if (err != nil) != tt.wantErr {
				t.Errorf("Authorize() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestAPIKeyAuthorizer_Authorize_persists(t *testing.T) {
	store := secure.NewMemoryStorage()
	a, _ := providerauth.NewBuiltinAuthorizer("gemini", store)

	key := "AIza-testkey-1234567890"
	if _, err := a.Authorize(context.Background(), providerauth.MethodAPIKey, map[string]string{"key": key}); err != nil {
		t.Fatalf("Authorize: %v", err)
	}

	got, err := providerauth.LoadCredential(store, "gemini")
	if err != nil {
		t.Fatalf("LoadCredential after Authorize: %v", err)
	}
	if got != key {
		t.Errorf("persisted key = %q, want %q", got, key)
	}
}

func TestAPIKeyAuthorizer_Validate(t *testing.T) {
	store := secure.NewMemoryStorage()
	a, _ := providerauth.NewBuiltinAuthorizer("openrouter", store)

	if err := a.Validate(context.Background(), "sk-or-validkey123456"); err != nil {
		t.Errorf("Validate valid key: %v", err)
	}
	if err := a.Validate(context.Background(), ""); err == nil {
		t.Error("Validate empty key: want error")
	}
}

func TestNewBuiltinAuthorizer_unknown(t *testing.T) {
	store := secure.NewMemoryStorage()
	_, err := providerauth.NewBuiltinAuthorizer("unknown-provider", store)
	if err == nil {
		t.Error("NewBuiltinAuthorizer(unknown): want error")
	}
}
