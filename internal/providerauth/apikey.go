package providerauth

import (
	"context"
	"fmt"

	"github.com/icehunter/conduit/internal/secure"
)

// APIKeyAuthorizer implements Authorizer for providers that use a static API key.
// It validates the key format locally (no network call) and persists it in
// secure storage.
type APIKeyAuthorizer struct {
	cfg   Config
	store secure.Storage
}

// NewAPIKeyAuthorizer returns an Authorizer for the given provider config.
func NewAPIKeyAuthorizer(cfg Config, store secure.Storage) *APIKeyAuthorizer {
	return &APIKeyAuthorizer{cfg: cfg, store: store}
}

// NewBuiltinAuthorizer returns an APIKeyAuthorizer for a built-in provider ID.
// Returns an error if the ID is not a known built-in.
func NewBuiltinAuthorizer(providerID string, store secure.Storage) (*APIKeyAuthorizer, error) {
	cfg, ok := BuiltinByID(providerID)
	if !ok {
		return nil, fmt.Errorf("providerauth: unknown built-in provider %q", providerID)
	}
	return NewAPIKeyAuthorizer(cfg, store), nil
}

func (a *APIKeyAuthorizer) ProviderID() string { return a.cfg.ID }

func (a *APIKeyAuthorizer) Methods() []Method { return a.cfg.Methods }

// Authorize validates the "key" param and stores it. kind must be MethodAPIKey.
func (a *APIKeyAuthorizer) Authorize(_ context.Context, kind string, params map[string]string) (string, error) {
	if kind != MethodAPIKey {
		return "", fmt.Errorf("providerauth: %s does not support method %q", a.cfg.ID, kind)
	}
	key := params["key"]
	if err := a.Validate(context.Background(), key); err != nil {
		return "", err
	}
	if err := SaveCredential(a.store, a.cfg.ID, key); err != nil {
		return "", fmt.Errorf("providerauth: save credential for %s: %w", a.cfg.ID, err)
	}
	return key, nil
}

// Validate checks that credential is a plausible API key (non-empty, no spaces,
// at least 8 characters). It does not make any network calls.
func (a *APIKeyAuthorizer) Validate(_ context.Context, credential string) error {
	if !looksLikeAPIKey(credential) {
		return fmt.Errorf("providerauth: invalid API key for %s — must be at least 8 non-whitespace characters", a.cfg.ID)
	}
	return nil
}
