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
// the account in accounts.json as the active account.
//
// accounts.json is ALWAYS updated regardless of keychain write results.
// Token writes are best-effort — a permissions or I/O error on the credential
// file must not prevent account tracking from working.
func SaveForEmail(s secure.Storage, p PersistedTokens, email string) error {
	buf, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("accounts: marshal tokens: %w", err)
	}

	// Write token to keychain (both legacy and email-scoped). Best-effort.
	_ = s.Set(Service, PersistKey, buf)
	if email != "" {
		_ = s.Set(Service, persistKeyForEmail(email), buf)
	}

	if email == "" {
		return nil
	}

	// Always register the account in accounts.json — this must succeed.
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

// LoadForEmail loads tokens for the given email.
// When email is empty, loads the legacy single-account key.
// When email is non-empty: tries the email-scoped key first. If not found,
// falls back to the legacy key and immediately writes the scoped key so
// future loads find it directly. This auto-heal covers the case where
// SaveForEmail ran but the scoped write silently failed.
func LoadForEmail(s secure.Storage, email string) (PersistedTokens, error) {
	if email == "" {
		return Load(s)
	}
	// Email-scoped key — the authoritative path.
	raw, err := s.Get(Service, persistKeyForEmail(email))
	if err == nil {
		var p PersistedTokens
		if err := json.Unmarshal(raw, &p); err == nil {
			return p, nil
		}
	}
	// Scoped key absent — try the legacy key and heal.
	p, lerr := Load(s)
	if lerr != nil {
		return PersistedTokens{}, lerr
	}
	// Write the scoped key now so next load is fast.
	if buf, err := json.Marshal(p); err == nil {
		_ = s.Set(Service, persistKeyForEmail(email), buf)
	}
	return p, nil
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

// SetActive switches the active account to the given email.
// The email must have a token stored under its email-scoped keychain key
// (written by SaveForEmail during /login). Returns an error if no such
// token exists — the user must /login for that account first.
func SetActive(store *AccountStore, email string) error {
	s := newDefaultStorage()
	if _, err := s.Get(Service, persistKeyForEmail(email)); err != nil {
		return fmt.Errorf("no saved credentials for %q — run /login to add this account", email)
	}
	// Register in the store if first time seeing this email.
	if _, ok := store.Accounts[email]; !ok {
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
