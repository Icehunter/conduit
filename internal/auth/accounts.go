package auth

// Multi-account support.
//
// Each account's token is stored in the platform keychain under the key
// "oauth-tokens-<kind>:<email>". ~/.conduit/conduit.json tracks which
// accounts exist and which is currently active.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

// AccountEntry holds metadata for one stored account.
type AccountEntry struct {
	Email            string    `json:"email"`
	Kind             string    `json:"kind,omitempty"`
	DisplayName      string    `json:"display_name,omitempty"`
	OrganizationName string    `json:"organization_name,omitempty"`
	SubscriptionType string    `json:"subscription_type,omitempty"`
	AddedAt          time.Time `json:"added_at"`
}

// AccountStore is the shape of the "accounts" object in ~/.conduit/conduit.json.
type AccountStore struct {
	Active   string                  `json:"active"`
	Accounts map[string]AccountEntry `json:"accounts"`
}

// AccountID returns the stable account store/keychain identity for an auth
// source and email. Email alone is not unique: Claude.ai and Anthropic Console
// accounts may legitimately share the same address.
func AccountID(kind, email string) string {
	if email == "" {
		return ""
	}
	if kind == "" {
		kind = AccountKindClaudeAI
	}
	return kind + ":" + email
}

func splitAccountID(id string) (kind, email string, ok bool) {
	kind, email, ok = strings.Cut(id, ":")
	if !ok || email == "" {
		return "", id, false
	}
	switch kind {
	case AccountKindClaudeAI, AccountKindAnthropicConsole:
		return kind, email, true
	default:
		return "", id, false
	}
}

func accountsPath() string {
	return settings.ConduitSettingsPath()
}

func legacyAccountsPath() (string, error) {
	dir := settings.ClaudeDir()
	if dir == "" {
		return "", fmt.Errorf("accounts: cannot determine claude dir")
	}
	return filepath.Join(dir, "accounts.json"), nil
}

// LoadAccountStore reads the "accounts" object from ~/.conduit/conduit.json.
// Returns an empty store if the file or key doesn't exist yet.
func LoadAccountStore() (AccountStore, error) {
	p := accountsPath()
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return loadOrImportLegacyAccountStore()
		}
		return AccountStore{}, fmt.Errorf("accounts: read: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return AccountStore{}, fmt.Errorf("accounts: parse: %w", err)
	}
	if len(raw["accounts"]) == 0 {
		return loadOrImportLegacyAccountStore()
	}
	var s AccountStore
	if err := json.Unmarshal(raw["accounts"], &s); err != nil {
		return AccountStore{}, fmt.Errorf("accounts: parse accounts: %w", err)
	}
	if s.Accounts == nil {
		s.Accounts = map[string]AccountEntry{}
	}
	normalized := normalizeAccountStore(s)
	if !reflect.DeepEqual(s, normalized) {
		_ = saveAccountStore(normalized)
	}
	return normalized, nil
}

func loadOrImportLegacyAccountStore() (AccountStore, error) {
	legacy, err := loadLegacyAccountStore()
	if err != nil {
		return AccountStore{Accounts: map[string]AccountEntry{}}, nil
	}
	if legacy.Accounts == nil {
		legacy.Accounts = map[string]AccountEntry{}
	}
	if legacy.Active == "" && len(legacy.Accounts) == 0 {
		return legacy, nil
	}
	legacy = normalizeAccountStore(legacy)
	_ = saveAccountStore(legacy)
	return legacy, nil
}

func loadLegacyAccountStore() (AccountStore, error) {
	p, err := legacyAccountsPath()
	if err != nil {
		return AccountStore{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return AccountStore{}, err
	}
	var s AccountStore
	if err := json.Unmarshal(data, &s); err != nil {
		return AccountStore{}, err
	}
	if s.Accounts == nil {
		s.Accounts = map[string]AccountEntry{}
	}
	return normalizeAccountStore(s), nil
}

func normalizeAccountStore(s AccountStore) AccountStore {
	if s.Accounts == nil {
		s.Accounts = map[string]AccountEntry{}
	}
	normalized := make(map[string]AccountEntry, len(s.Accounts))
	active := s.Active
	for key, entry := range s.Accounts {
		if entry.Email == "" {
			if kind, email, ok := splitAccountID(key); ok {
				entry.Kind = kind
				entry.Email = email
			} else {
				entry.Email = key
			}
		}
		if entry.Kind == "" {
			entry.Kind = AccountKindClaudeAI
		}
		id := AccountID(entry.Kind, entry.Email)
		normalized[id] = entry
		if active == key || active == entry.Email {
			active = id
		}
	}
	s.Accounts = normalized
	s.Active = active
	return s
}

// SaveAccountStore writes the account store to ~/.conduit/conduit.json.
// Exported so the TUI account panel can remove accounts without a full delete.
func SaveAccountStore(s AccountStore) error { return saveAccountStore(normalizeAccountStore(s)) }

func saveAccountStore(s AccountStore) error {
	p := accountsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	raw := make(map[string]json.RawMessage)
	if data, err := os.ReadFile(p); err == nil && len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("accounts: parse existing: %w", err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("accounts: read existing: %w", err)
	}
	accounts, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("accounts: marshal accounts: %w", err)
	}
	raw["accounts"] = accounts
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("accounts: marshal: %w", err)
	}
	return os.WriteFile(p, data, 0o600)
}

// keyForEmail returns the legacy keychain key name for an email.
func keyForEmail(email string) string {
	return PersistKey + "-" + email
}

func keyForEmailKind(email, kind string) string {
	return PersistKey + "-" + AccountID(kind, email)
}

// SaveForEmail writes the token to the platform keychain under the email-
// scoped key and registers the account in conduit.json as active.
func SaveForEmail(s secure.Storage, p PersistedTokens, email string) error {
	return SaveForEmailKind(s, p, email, p.AccountKind)
}

// SaveForEmailKind writes the token to the platform keychain under the email-
// scoped key and registers the account in conduit.json as active with its
// auth source.
func SaveForEmailKind(s secure.Storage, p PersistedTokens, email, kind string) error {
	if email == "" {
		return fmt.Errorf("accounts: email required")
	}
	if kind == "" {
		kind = InferAccountKind(p)
	}
	p.AccountKind = kind
	if err := saveTokenForKind(s, p, email, kind); err != nil {
		return err
	}
	return registerAccount(email, kind)
}

// saveToken writes the token to the keychain only (no account metadata).
// Used internally by EnsureFresh to persist refreshed tokens.
func saveToken(s secure.Storage, p PersistedTokens, email string) error {
	kind, resolvedEmail := resolveAccountRef(email)
	if resolvedEmail == "" {
		resolvedEmail = email
	}
	if kind == "" {
		kind = InferAccountKind(p)
	}
	return saveTokenForKind(s, p, resolvedEmail, kind)
}

func saveTokenForKind(s secure.Storage, p PersistedTokens, email, kind string) error {
	buf, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("accounts: marshal tokens: %w", err)
	}
	if err := s.Set(Service, keyForEmailKind(email, kind), buf); err != nil {
		return fmt.Errorf("accounts: save token for %s: %w", email, err)
	}
	return nil
}

// registerAccount adds the email to conduit.json and sets it as active.
func registerAccount(email, kind string) error {
	store, _ := LoadAccountStore()
	if store.Accounts == nil {
		store.Accounts = map[string]AccountEntry{}
	}
	id := AccountID(kind, email)
	entry, exists := store.Accounts[id]
	if !exists {
		entry = AccountEntry{Email: email, AddedAt: time.Now()}
	}
	if kind != "" {
		entry.Kind = kind
	}
	store.Accounts[id] = entry
	store.Active = id
	return saveAccountStore(store)
}

func SaveAccountProfile(email, kind, displayName, organizationName, subscriptionType string) error {
	store, err := LoadAccountStore()
	if err != nil {
		return err
	}
	id := AccountID(kind, email)
	entry, ok := store.Accounts[id]
	if !ok {
		entry = AccountEntry{Email: email, Kind: kind, AddedAt: time.Now()}
	}
	if displayName != "" {
		entry.DisplayName = displayName
	}
	if organizationName != "" {
		entry.OrganizationName = organizationName
	}
	if subscriptionType != "" {
		entry.SubscriptionType = subscriptionType
	}
	store.Accounts[id] = entry
	return saveAccountStore(store)
}

// LoadForEmail loads and decodes the token for the given email.
func LoadForEmail(s secure.Storage, email string) (PersistedTokens, error) {
	if email == "" {
		return PersistedTokens{}, secure.ErrNotFound
	}
	kind, resolvedEmail := resolveAccountRef(email)
	raw, err := s.Get(Service, keyForEmailKind(resolvedEmail, kind))
	if err != nil {
		raw, err = s.Get(Service, keyForEmail(resolvedEmail))
	}
	if err != nil {
		return PersistedTokens{}, err
	}
	var p PersistedTokens
	if err := json.Unmarshal(raw, &p); err != nil {
		return PersistedTokens{}, fmt.Errorf("accounts: decode token for %s: %w", email, err)
	}
	return p, nil
}

// DeleteForEmail removes an account's token and conduit.json entry.
func DeleteForEmail(s secure.Storage, email string) error {
	store, err := LoadAccountStore()
	if err != nil {
		return err
	}
	kind, resolvedEmail, id := resolveAccountRefFromStore(store, email)
	_ = s.Delete(Service, keyForEmailKind(resolvedEmail, kind))
	if !strings.Contains(email, ":") {
		_ = s.Delete(Service, keyForEmail(email))
	}
	delete(store.Accounts, id)
	if store.Active == id {
		store.Active = ""
	}
	return saveAccountStore(store)
}

// SetActive switches the active account. Requires that the account already
// has a saved token (from a prior /login).
func SetActive(store *AccountStore, email string) error {
	s := secure.NewDefault()
	kind, resolvedEmail, id := resolveAccountRefFromStore(*store, email)
	if _, err := s.Get(Service, keyForEmailKind(resolvedEmail, kind)); err != nil {
		return fmt.Errorf("no saved credentials for %q — run /login first", email)
	}
	if _, ok := store.Accounts[id]; !ok {
		store.Accounts[id] = AccountEntry{Email: resolvedEmail, Kind: kind, AddedAt: time.Now()}
	}
	store.Active = id
	return saveAccountStore(*store)
}

// ActiveEmail returns the currently active account identity.
// If active is unset but exactly one account exists, that account is
// auto-selected and persisted so subsequent calls are consistent.
// Returns "" only when no accounts are registered.
func ActiveEmail() string {
	store, err := LoadAccountStore()
	if err != nil {
		return ""
	}
	if store.Active != "" {
		return store.Active
	}
	// Auto-select: pick the most-recently-added account when active is blank.
	if len(store.Accounts) == 0 {
		return ""
	}
	var best string
	var bestTime time.Time
	for email, entry := range store.Accounts {
		if best == "" || entry.AddedAt.After(bestTime) {
			best = email
			bestTime = entry.AddedAt
		}
	}
	store.Active = best
	_ = saveAccountStore(store)
	return best
}

func resolveAccountRef(ref string) (kind, email string) {
	store, err := LoadAccountStore()
	if err == nil {
		kind, email, _ = resolveAccountRefFromStore(store, ref)
		return kind, email
	}
	if k, e, ok := splitAccountID(ref); ok {
		return k, e
	}
	return AccountKindClaudeAI, ref
}

func resolveAccountRefFromStore(store AccountStore, ref string) (kind, email, id string) {
	store = normalizeAccountStore(store)
	if entry, ok := store.Accounts[ref]; ok {
		return entry.Kind, entry.Email, ref
	}
	if k, e, ok := splitAccountID(ref); ok {
		return k, e, ref
	}
	var matchedID string
	var matchedEntry AccountEntry
	for accountID, entry := range store.Accounts {
		if entry.Email != ref {
			continue
		}
		if accountID == store.Active {
			return entry.Kind, entry.Email, accountID
		}
		if matchedID == "" || entry.AddedAt.After(matchedEntry.AddedAt) {
			matchedID = accountID
			matchedEntry = entry
		}
	}
	if matchedID != "" {
		return matchedEntry.Kind, matchedEntry.Email, matchedID
	}
	return AccountKindClaudeAI, ref, AccountID(AccountKindClaudeAI, ref)
}

// ListAccounts returns all registered accounts.
func ListAccounts() (AccountStore, error) {
	return LoadAccountStore()
}
