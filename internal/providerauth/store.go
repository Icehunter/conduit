package providerauth

import (
	"errors"
	"fmt"

	"github.com/icehunter/conduit/internal/secure"
)

const credentialService = "provider-auth"

func credentialKey(providerID string) string {
	return "providerauth:" + providerID
}

// SaveCredential stores an API key for the given provider in secure storage.
func SaveCredential(store secure.Storage, providerID, credential string) error {
	if store == nil {
		return fmt.Errorf("providerauth: secure storage required")
	}
	if providerID == "" {
		return fmt.Errorf("providerauth: provider ID required")
	}
	if credential == "" {
		return fmt.Errorf("providerauth: credential required")
	}
	return store.Set(credentialService, credentialKey(providerID), []byte(credential))
}

// LoadCredential retrieves the stored credential for the given provider.
// Returns ErrMissingCredential when no credential is stored.
func LoadCredential(store secure.Storage, providerID string) (string, error) {
	if store == nil {
		return "", fmt.Errorf("providerauth: secure storage required")
	}
	if providerID == "" {
		return "", fmt.Errorf("providerauth: provider ID required")
	}
	raw, err := store.Get(credentialService, credentialKey(providerID))
	if err != nil {
		if errors.Is(err, secure.ErrNotFound) {
			return "", ErrMissingCredential
		}
		return "", fmt.Errorf("providerauth: load credential for %s: %w", providerID, err)
	}
	return string(raw), nil
}

// DeleteCredential removes the stored credential for the given provider.
func DeleteCredential(store secure.Storage, providerID string) error {
	if store == nil || providerID == "" {
		return nil
	}
	return store.Delete(credentialService, credentialKey(providerID))
}

// IsConnected reports whether a credential is stored for the given provider.
func IsConnected(store secure.Storage, providerID string) bool {
	if store == nil || providerID == "" {
		return false
	}
	_, err := LoadCredential(store, providerID)
	return err == nil
}
