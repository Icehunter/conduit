package settings

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/icehunter/conduit/internal/providerauth"
	"github.com/icehunter/conduit/internal/secure"
)

const providerCredentialService = "provider-credentials"

// ProviderCredentialKey is the secure-storage key for a provider credential.
func ProviderCredentialKey(name string) string {
	return "provider:" + name
}

// SaveProviderCredential saves a simple API key.
func SaveProviderCredential(store secure.Storage, name, value string) error {
	if store == nil {
		return fmt.Errorf("settings: secure storage is required")
	}
	if name == "" || value == "" {
		return fmt.Errorf("settings: name and value are required")
	}
	return store.Set(providerCredentialService, ProviderCredentialKey(name), []byte(value))
}

// SaveStructuredProviderCredential saves a serialized ProviderCredential object.
func SaveStructuredProviderCredential(store secure.Storage, name string, cred *providerauth.ProviderCredential) error {
	if store == nil {
		return fmt.Errorf("settings: secure storage is required")
	}
	if name == "" {
		return fmt.Errorf("settings: provider credential name is required")
	}
	if cred == nil || cred.AccessToken == "" {
		return fmt.Errorf("settings: provider credential access token is required")
	}
	data, err := json.Marshal(cred)
	if err != nil {
		return fmt.Errorf("settings: failed to marshal credential: %w", err)
	}
	return store.Set(providerCredentialService, ProviderCredentialKey(name), data)
}

// LoadProviderCredential loads a simple API key.
func LoadProviderCredential(store secure.Storage, name string) (string, error) {
	if store == nil {
		return "", fmt.Errorf("settings: secure storage is required")
	}
	if name == "" {
		return "", fmt.Errorf("settings: provider credential name is required")
	}
	raw, err := store.Get(providerCredentialService, ProviderCredentialKey(name))
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(raw))
	if strings.HasPrefix(text, "{") {
		var cred providerauth.ProviderCredential
		if err := json.Unmarshal(raw, &cred); err == nil && cred.AccessToken != "" {
			return cred.AccessToken, nil
		}
	}
	return string(raw), nil
}

// LoadStructuredProviderCredential loads a structured ProviderCredential object.
func LoadStructuredProviderCredential(store secure.Storage, name string) (*providerauth.ProviderCredential, error) {
	if store == nil {
		return nil, fmt.Errorf("settings: secure storage is required")
	}
	if name == "" {
		return nil, fmt.Errorf("settings: provider credential name is required")
	}
	raw, err := store.Get(providerCredentialService, ProviderCredentialKey(name))
	if err != nil {
		return nil, err
	}
	var cred providerauth.ProviderCredential
	if err := json.Unmarshal(raw, &cred); err != nil {
		return nil, fmt.Errorf("settings: failed to unmarshal credential: %w", err)
	}
	return &cred, nil
}

// DeleteProviderCredential deletes a stored credential.
func DeleteProviderCredential(store secure.Storage, name string) error {
	if store == nil || name == "" {
		return nil
	}
	return store.Delete(providerCredentialService, ProviderCredentialKey(name))
}
