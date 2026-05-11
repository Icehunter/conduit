package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ProviderForRole resolves a named role to a provider. Missing roles fall back
// to the default role when configured.
func (m *Merged) ProviderForRole(role string) (*ActiveProviderSettings, bool) {
	if m == nil {
		return nil, false
	}
	if role == "" {
		role = RoleDefault
	}
	if provider, ok := m.providerForRoleRef(role); ok {
		return provider, true
	}
	return nil, false
}

func (m *Merged) providerForRoleRef(role string) (*ActiveProviderSettings, bool) {
	if m == nil {
		return nil, false
	}
	ref := m.Roles[role]
	if ref == "" {
		return nil, false
	}
	provider, ok := m.Providers[ref]
	if !ok {
		return nil, false
	}
	if !m.providerAvailable(provider) {
		return nil, false
	}
	cp := provider
	return &cp, true
}

func (m *Merged) providerAvailable(provider ActiveProviderSettings) bool {
	switch provider.Kind {
	case ProviderKindClaudeSubscription, ProviderKindAnthropicAPI:
		if provider.Kind == ProviderKindAnthropicAPI && provider.Credential != "" {
			return ValidateProviderSettings(provider) == nil
		}
		return m.accountProviderAvailable(provider)
	case ProviderKindOpenAICompatible:
		return ValidateProviderSettings(provider) == nil
	default:
		return true
	}
}

func (m *Merged) accountProviderAvailable(provider ActiveProviderSettings) bool {
	if provider.Account == "" {
		return false
	}
	accounts, ok := m.accountStore()
	if !ok || len(accounts.Accounts) == 0 {
		return false
	}
	if entry, ok := accounts.Accounts[provider.Account]; ok {
		return providerKindMatchesAccount(provider.Kind, entry.Kind)
	}
	for _, entry := range accounts.Accounts {
		if entry.Email == provider.Account && providerKindMatchesAccount(provider.Kind, entry.Kind) {
			return true
		}
	}
	return false
}

func (m *Merged) accountStore() (accountStoreSettings, bool) {
	if m == nil {
		return accountStoreSettings{}, false
	}
	path := ConduitSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return accountStoreSettings{}, false
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil || len(raw["accounts"]) == 0 {
		return accountStoreSettings{}, false
	}
	var accounts accountStoreSettings
	if err := json.Unmarshal(raw["accounts"], &accounts); err != nil {
		return accountStoreSettings{}, false
	}
	return accounts, true
}

type accountStoreSettings struct {
	Active   string                          `json:"active"`
	Accounts map[string]accountEntrySettings `json:"accounts"`
}

type accountEntrySettings struct {
	Email            string    `json:"email"`
	Kind             string    `json:"kind,omitempty"`
	DisplayName      string    `json:"display_name,omitempty"`
	OrganizationName string    `json:"organization_name,omitempty"`
	SubscriptionType string    `json:"subscription_type,omitempty"`
	AddedAt          time.Time `json:"added_at,omitempty"`
}

func providerKindMatchesAccount(providerKind, accountKind string) bool {
	switch providerKind {
	case ProviderKindAnthropicAPI:
		return accountKind == "anthropic-console"
	case ProviderKindClaudeSubscription:
		return accountKind == "" || accountKind == "claude-ai"
	default:
		return false
	}
}

// SaveActiveProvider persists the active default provider and mirrors it into
// providers + roles.default.
func SaveActiveProvider(value ActiveProviderSettings) error {
	return SaveRoleProvider(RoleDefault, value)
}

// SaveRoleProvider persists a provider and assigns it to role.
func SaveRoleProvider(role string, value ActiveProviderSettings) error {
	if err := ValidateProviderSettings(value); err != nil {
		return err
	}
	conduitConfigMu.Lock()
	defer conduitConfigMu.Unlock()
	path := ConduitSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := readRawObject(path)
	if err != nil {
		return err
	}

	if _, err := json.Marshal(value); err != nil {
		return err
	}
	if role == "" {
		role = RoleDefault
	}

	providers := map[string]ActiveProviderSettings{}
	if existing := raw["providers"]; len(existing) > 0 {
		_ = json.Unmarshal(existing, &providers)
	}
	roles := map[string]string{}
	if existing := raw["roles"]; len(existing) > 0 {
		_ = json.Unmarshal(existing, &roles)
	}
	providers, roles, _ = CanonicalizeProviderRegistry(providers, roles)
	key := ProviderKey(value)
	providers[key] = value
	encodedProviders, err := json.Marshal(providers)
	if err != nil {
		return err
	}
	raw["providers"] = encodedProviders

	roles[role] = key
	encodedRoles, err := json.Marshal(roles)
	if err != nil {
		return err
	}
	raw["roles"] = encodedRoles

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(out, '\n'))
}

// SaveProviderEntry persists a configured provider without changing any role.
func SaveProviderEntry(value ActiveProviderSettings) error {
	if err := ValidateProviderSettings(value); err != nil {
		return err
	}
	return UpdateConduitConfig(func(cfg *ConduitConfig) {
		if cfg.Providers == nil {
			cfg.Providers = map[string]ActiveProviderSettings{}
		}
		cfg.Providers, cfg.Roles, _ = CanonicalizeProviderRegistry(cfg.Providers, cfg.Roles)
		cfg.Providers[ProviderKey(value)] = value
	})
}

// DeleteProviderEntry removes a provider and clears any roles that reference it.
func DeleteProviderEntry(key string) error {
	return UpdateConduitConfig(func(cfg *ConduitConfig) {
		if cfg.Providers != nil {
			delete(cfg.Providers, key)
		}
		if cfg.Roles != nil {
			for role, ref := range cfg.Roles {
				if ref == key {
					delete(cfg.Roles, role)
				}
			}
		}
	})
}

// ClearRoleProvider removes the provider assignment for a role.
func ClearRoleProvider(role string) error {
	if role == "" {
		role = RoleDefault
	}
	return UpdateConduitConfig(func(cfg *ConduitConfig) {
		if cfg.Roles != nil {
			delete(cfg.Roles, role)
		}
	})
}

// RepairConduitProviderRegistry canonicalizes provider keys persisted in
// conduit.json and rewrites matching role references. It is intentionally
// narrow: invalid providers and missing refs remain for UI validation.
func RepairConduitProviderRegistry() error {
	cfg, err := LoadConduitConfig()
	if err != nil {
		return err
	}
	if len(cfg.Providers) == 0 && len(cfg.Roles) == 0 {
		return nil
	}
	providers, roles, changed := CanonicalizeProviderRegistry(cfg.Providers, cfg.Roles)
	cfg.Providers = providers
	cfg.Roles = roles
	if migrated := migrateLegacyProviderKinds(&cfg); migrated {
		changed = true
	}
	if !changed {
		return nil
	}
	return SaveConduitConfig(cfg)
}

func migrateLegacyProviderKinds(cfg *ConduitConfig) bool {
	if cfg == nil || len(cfg.Providers) == 0 {
		return false
	}
	changed := false
	for key, provider := range cfg.Providers {
		if provider.Kind != "openai-responses" {
			continue
		}
		provider.Kind = ProviderKindOpenAICompatible
		if provider.Model == "" {
			provider.Model = "gpt-5.5"
		}
		newKey := ProviderKey(provider)
		if newKey == key {
			cfg.Providers[key] = provider
			changed = true
			continue
		}
		delete(cfg.Providers, key)
		cfg.Providers[newKey] = provider
		for role, ref := range cfg.Roles {
			if ref == key {
				cfg.Roles[role] = newKey
			}
		}
		changed = true
	}
	return changed
}

// ProviderKey returns a deterministic config key for a provider.
func ProviderKey(value ActiveProviderSettings) string {
	kind := value.Kind
	if kind == "" {
		kind = "provider"
	}
	switch value.Kind {
	case ProviderKindMCP:
		if value.Server != "" {
			return kind + "." + value.Server
		}
	case ProviderKindClaudeSubscription:
		if value.Account != "" && value.Model != "" {
			return kind + "." + value.Account + "." + value.Model
		}
		if value.Model != "" {
			return kind + "." + value.Model
		}
	case ProviderKindAnthropicAPI:
		if value.Account != "" && value.Model != "" {
			return kind + "." + value.Account + "." + value.Model
		}
		if value.Credential != "" && value.Model != "" {
			return kind + "." + value.Credential + "." + value.Model
		}
		if value.Model != "" {
			return kind + "." + value.Model
		}
		if value.Credential != "" {
			return kind + "." + value.Credential
		}
	case ProviderKindOpenAICompatible:
		if value.Credential != "" && value.Model != "" {
			return kind + "." + value.Credential + "." + value.Model
		}
		if value.Model != "" {
			return kind + "." + value.Model
		}
		if value.Credential != "" {
			return kind + "." + value.Credential
		}
	default:
		if value.Credential != "" {
			return kind + "." + value.Credential
		}
		if value.Model != "" {
			return kind + "." + value.Model
		}
	}
	return kind + ".default"
}
