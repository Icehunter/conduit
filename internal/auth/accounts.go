package auth

// Multi-account support.
//
// Each account's token is stored in the platform keychain under the key
// "oauth-tokens-<email>". ~/.claude/accounts.json tracks which accounts
// exist and which is currently active.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

// AccountEntry holds metadata for one stored account.
type AccountEntry struct {
	Email   string    `json:"email"`
	AddedAt time.Time `json:"added_at"`
}

// AccountStore is the shape of ~/.claude/accounts.json.
type AccountStore struct {
	Active   string                  `json:"active"`
	Accounts map[string]AccountEntry `json:"accounts"`
}

func accountsPath() (string, error) {
	dir := settings.ClaudeDir()
	if dir == "" {
		return "", fmt.Errorf("accounts: cannot determine claude dir")
	}
	return filepath.Join(dir, "accounts.json"), nil
}

// LoadAccountStore reads ~/.claude/accounts.json. Returns an empty store if
// the file doesn't exist yet.
func LoadAccountStore() (AccountStore, error) {
	p, err := accountsPath()
	if err != nil {
		return AccountStore{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return AccountStore{Accounts: map[string]AccountEntry{}}, nil
		}
		return AccountStore{}, fmt.Errorf("accounts: read: %w", err)
	}
	var s AccountStore
	if err := json.Unmarshal(data, &s); err != nil {
		return AccountStore{}, fmt.Errorf("accounts: parse: %w", err)
	}
	if s.Accounts == nil {
		s.Accounts = map[string]AccountEntry{}
	}
	return s, nil
}

// SaveAccountStore writes the account store to ~/.claude/accounts.json.
// Exported so the TUI account panel can remove accounts without a full delete.
func SaveAccountStore(s AccountStore) error { return saveAccountStore(s) }

func saveAccountStore(s AccountStore) error {
	p, err := accountsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("accounts: marshal: %w", err)
	}
	return os.WriteFile(p, data, 0o600)
}

// keyForEmail returns the keychain key name for an email.
func keyForEmail(email string) string {
	return PersistKey + "-" + email
}

// SaveForEmail writes the token to the platform keychain under the email-
// scoped key and registers the account in accounts.json as active.
func SaveForEmail(s secure.Storage, p PersistedTokens, email string) error {
	if email == "" {
		return fmt.Errorf("accounts: email required")
	}
	if err := saveToken(s, p, email); err != nil {
		return err
	}
	return registerAccount(email)
}

// saveToken writes the token to the keychain only (no accounts.json).
// Used internally by EnsureFresh to persist refreshed tokens.
func saveToken(s secure.Storage, p PersistedTokens, email string) error {
	buf, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("accounts: marshal tokens: %w", err)
	}
	if err := s.Set(Service, keyForEmail(email), buf); err != nil {
		return fmt.Errorf("accounts: save token for %s: %w", email, err)
	}
	return nil
}

// registerAccount adds the email to accounts.json and sets it as active.
func registerAccount(email string) error {
	store, _ := LoadAccountStore()
	if store.Accounts == nil {
		store.Accounts = map[string]AccountEntry{}
	}
	if _, exists := store.Accounts[email]; !exists {
		store.Accounts[email] = AccountEntry{Email: email, AddedAt: time.Now()}
	}
	store.Active = email
	return saveAccountStore(store)
}

// LoadForEmail loads and decodes the token for the given email.
func LoadForEmail(s secure.Storage, email string) (PersistedTokens, error) {
	if email == "" {
		return PersistedTokens{}, secure.ErrNotFound
	}
	raw, err := s.Get(Service, keyForEmail(email))
	if err != nil {
		return PersistedTokens{}, err
	}
	var p PersistedTokens
	if err := json.Unmarshal(raw, &p); err != nil {
		return PersistedTokens{}, fmt.Errorf("accounts: decode token for %s: %w", email, err)
	}
	return p, nil
}

// DeleteForEmail removes an account's token and accounts.json entry.
func DeleteForEmail(s secure.Storage, email string) error {
	_ = s.Delete(Service, keyForEmail(email))
	store, err := LoadAccountStore()
	if err != nil {
		return err
	}
	delete(store.Accounts, email)
	if store.Active == email {
		store.Active = ""
	}
	return saveAccountStore(store)
}

// SetActive switches the active account. Requires that the email already
// has a saved token (from a prior /login).
func SetActive(store *AccountStore, email string) error {
	s := secure.NewDefault()
	if _, err := s.Get(Service, keyForEmail(email)); err != nil {
		return fmt.Errorf("no saved credentials for %q — run /login first", email)
	}
	if _, ok := store.Accounts[email]; !ok {
		store.Accounts[email] = AccountEntry{Email: email, AddedAt: time.Now()}
	}
	store.Active = email
	return saveAccountStore(*store)
}

// ActiveEmail returns the currently active account email, or "" if none.
func ActiveEmail() string {
	store, err := LoadAccountStore()
	if err != nil {
		return ""
	}
	return store.Active
}

// ListAccounts returns all registered accounts.
func ListAccounts() (AccountStore, error) {
	return LoadAccountStore()
}
