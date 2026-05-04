package auth

// Multi-account support.
//
// Accounts are stored in ~/.claude/accounts.json as a map of email → entry.
// The active account is tracked by the "active" field.
// Token bundles live in the keychain under "oauth-tokens-<email>".
// The legacy key "oauth-tokens" (no email suffix) maps to whichever account
// was active before multi-account was introduced.

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

func newDefaultStorage() secure.Storage { return secure.NewDefault() }

// AccountEntry holds metadata for one stored account.
type AccountEntry struct {
	Email    string    `json:"email"`
	AddedAt  time.Time `json:"added_at"`
}

// AccountStore is the shape of ~/.claude/accounts.json.
type AccountStore struct {
	Active   string                  `json:"active"` // email of active account
	Accounts map[string]AccountEntry `json:"accounts"`
}

func accountsPath() (string, error) {
	dir := settings.ClaudeDir()
	if dir == "" {
		return "", fmt.Errorf("accounts: cannot determine claude dir")
	}
	return filepath.Join(dir, "accounts.json"), nil
}

// LoadAccountStore reads ~/.claude/accounts.json. Returns an empty store if the
// file does not exist yet.
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

// saveAccountStore writes the store to disk.
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

// persistKeyForEmail returns the keychain item name for the given email.
// Empty email returns the legacy key so old single-account installs still work.
func persistKeyForEmail(email string) string {
	if email == "" {
		return PersistKey
	}
	return PersistKey + "-" + email
}

// SaveForEmail stores tokens under the email-scoped keychain key and registers
// the account in accounts.json, making it the active account.
func SaveForEmail(s secure.Storage, p PersistedTokens, email string) error {
	if err := Save(s, p); err != nil { // keep legacy key for compat
		return err
	}
	if email == "" {
		return nil
	}
	// Write under email-scoped key.
	buf, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("accounts: marshal tokens: %w", err)
	}
	if err := s.Set(Service, persistKeyForEmail(email), buf); err != nil {
		return err
	}
	// Register in accounts.json.
	store, _ := LoadAccountStore()
	store.Accounts[email] = AccountEntry{Email: email, AddedAt: time.Now()}
	store.Active = email
	return saveAccountStore(store)
}

// LoadForEmail loads tokens for the given email. Falls back to legacy key if
// email is empty or not found under the email-scoped key.
func LoadForEmail(s secure.Storage, email string) (PersistedTokens, error) {
	if email != "" {
		raw, err := s.Get(Service, persistKeyForEmail(email))
		if err == nil {
			var p PersistedTokens
			if err := json.Unmarshal(raw, &p); err == nil {
				return p, nil
			}
		}
	}
	return Load(s) // fallback to legacy key
}

// DeleteForEmail removes tokens for the given email from keychain and
// accounts.json. If this was the active account, active is cleared.
func DeleteForEmail(s secure.Storage, email string) error {
	if email != "" {
		_ = s.Delete(Service, persistKeyForEmail(email))
	}
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

// SetActive switches the active account to the given email. If the email isn't
// in the store yet but exists in the keychain under the email-scoped key, it is
// auto-registered. Returns an error only when no credentials are found at all.
func SetActive(store *AccountStore, email string) error {
	if _, ok := store.Accounts[email]; !ok {
		// Auto-register: check if a keychain entry exists for this email.
		s := newDefaultStorage()
		if _, err := s.Get(Service, persistKeyForEmail(email)); err != nil {
			// Also check the legacy key — this lets people switch by email
			// even if they've only ever logged in once on the legacy path.
			if _, lerr := s.Get(Service, PersistKey); lerr != nil {
				return fmt.Errorf("account %q not found — run /login first to add it", email)
			}
			// Legacy token exists but wasn't registered; register it now.
		}
		store.Accounts[email] = AccountEntry{Email: email, AddedAt: time.Now()}
	}
	store.Active = email
	return saveAccountStore(*store)
}

// ListAccounts returns all registered accounts with the active one marked.
func ListAccounts() (AccountStore, error) {
	return LoadAccountStore()
}

// ActiveEmail returns the email of the currently active account, or "" if
// only a legacy single-account token exists.
func ActiveEmail() string {
	store, err := LoadAccountStore()
	if err != nil {
		return ""
	}
	return store.Active
}
