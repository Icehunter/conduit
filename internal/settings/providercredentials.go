package settings

import (
	"fmt"

	"github.com/icehunter/conduit/internal/secure"
)

const providerCredentialService = "provider-credentials"

// ProviderCredentialKey is the secure-storage key for a provider credential.
func ProviderCredentialKey(name string) string {
	return "provider:" + name
}

func SaveProviderCredential(store secure.Storage, name, value string) error {
	if store == nil {
		return fmt.Errorf("settings: secure storage is required")
	}
	if name == "" {
		return fmt.Errorf("settings: provider credential name is required")
	}
	if value == "" {
		return fmt.Errorf("settings: provider credential value is required")
	}
	return store.Set(providerCredentialService, ProviderCredentialKey(name), []byte(value))
}

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
	return string(raw), nil
}

func DeleteProviderCredential(store secure.Storage, name string) error {
	if store == nil || name == "" {
		return nil
	}
	return store.Delete(providerCredentialService, ProviderCredentialKey(name))
}
