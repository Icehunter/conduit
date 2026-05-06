package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ProviderForRole resolves a named role to a provider. The legacy
// activeProvider field remains the fallback for main so existing configs keep
// working while roles/providers land.
func (m *Merged) ProviderForRole(role string) (*ActiveProviderSettings, bool) {
	if m == nil {
		return nil, false
	}
	if role == "" {
		role = RoleDefault
	}
	if ref := m.Roles[role]; ref != "" {
		if provider, ok := m.Providers[ref]; ok {
			if !m.providerAvailable(provider) {
				return nil, false
			}
			cp := provider
			return &cp, true
		}
	}
	if role == RoleDefault && m.ActiveProvider != nil {
		if !m.providerAvailable(*m.ActiveProvider) {
			return nil, false
		}
		cp := *m.ActiveProvider
		return &cp, true
	}
	return nil, false
}

func (m *Merged) providerAvailable(provider ActiveProviderSettings) bool {
	switch provider.Kind {
	case "claude-subscription", "anthropic-api":
		return m.accountProviderAvailable(provider)
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
	case "anthropic-api":
		return accountKind == "anthropic-console"
	case "claude-subscription":
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

// SaveRoleProvider persists a provider and assigns it to role. For default it
// also updates activeProvider for compatibility with older config readers.
func SaveRoleProvider(role string, value ActiveProviderSettings) error {
	path := ConduitSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := readRawObject(path)
	if err != nil {
		return err
	}

	active, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if role == "" {
		role = RoleDefault
	}
	if role == RoleDefault {
		raw["activeProvider"] = active
	}

	providers := map[string]ActiveProviderSettings{}
	if existing := raw["providers"]; len(existing) > 0 {
		_ = json.Unmarshal(existing, &providers)
	}
	key := ProviderKey(value)
	providers[key] = value
	encodedProviders, err := json.Marshal(providers)
	if err != nil {
		return err
	}
	raw["providers"] = encodedProviders

	roles := map[string]string{}
	if existing := raw["roles"]; len(existing) > 0 {
		_ = json.Unmarshal(existing, &roles)
	}
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
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// ProviderKey returns a deterministic config key for a provider.
func ProviderKey(value ActiveProviderSettings) string {
	kind := value.Kind
	if kind == "" {
		kind = "provider"
	}
	switch value.Kind {
	case "mcp":
		if value.Server != "" {
			return kind + "." + value.Server
		}
	case "claude-subscription":
		if value.Account != "" && value.Model != "" {
			return kind + "." + value.Account + "." + value.Model
		}
		if value.Model != "" {
			return kind + "." + value.Model
		}
	case "anthropic-api":
		if value.Account != "" && value.Model != "" {
			return kind + "." + value.Account + "." + value.Model
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
