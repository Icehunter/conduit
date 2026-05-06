package tui

import (
	"fmt"

	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/secure"
)

// logoutCredentials removes the active account's token from the keychain
// and clears it from conduit.json account metadata.
func logoutCredentials() error {
	store := defaultSecureStorage()
	email := auth.ActiveEmail()
	if email == "" {
		return fmt.Errorf("not logged in")
	}
	return auth.DeleteForEmail(store, email)
}

// defaultSecureStorage returns the platform keychain storage.
func defaultSecureStorage() secure.Storage {
	return secure.NewDefault()
}
