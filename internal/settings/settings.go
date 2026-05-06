// Package settings loads and merges Claude Code settings files.
//
// Priority order (later overrides earlier):
//  1. ~/.claude/settings.json          (user global)
//  2. <project>/.claude/settings.json  (project shared)
//  3. <project>/.claude/settings.local.json (project local, gitignored)
//  4. ~/.conduit/conduit.json          (conduit-only user overlay)
//
// Mirrors src/utils/config.ts and src/utils/settings/settings.ts.
package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Permissions is the permissions section of a settings file.
type Permissions struct {
	Allow          []string `json:"allow"`
	Deny           []string `json:"deny"`
	Ask            []string `json:"ask"`
	DefaultMode    string   `json:"defaultMode"`
	AdditionalDirs []string `json:"additionalDirectories"`
}

// Hook is one hook entry. Type determines which fields are used.
// Mirrors src/schemas/hooks.ts (BashCommandHookSchema, HttpHookSchema,
// PromptHookSchema, AgentHookSchema).
type Hook struct {
	// Common fields
	Type          string `json:"type"`                    // "command" | "http" | "prompt" | "agent"
	StatusMessage string `json:"statusMessage,omitempty"` // spinner text while running
	If            string `json:"if,omitempty"`            // permission rule to gate firing
	TimeoutSecs   int    `json:"timeout,omitempty"`       // per-hook timeout override (seconds)
	Once          bool   `json:"once,omitempty"`          // remove after first execution
	Async         bool   `json:"async,omitempty"`         // fire-and-forget (non-blocking)

	// type="command"
	Command string `json:"command,omitempty"` // shell command

	// type="http"
	URL            string            `json:"url,omitempty"`            // POST target
	Headers        map[string]string `json:"headers,omitempty"`        // extra headers
	AllowedEnvVars []string          `json:"allowedEnvVars,omitempty"` // vars to interpolate in headers

	// type="prompt" | "agent"
	Prompt string `json:"prompt,omitempty"` // LLM prompt (may contain $ARGUMENTS)
	Model  string `json:"model,omitempty"`  // model override
}

// HookMatcher is a matcher + hooks pair.
type HookMatcher struct {
	Matcher    string `json:"matcher"` // tool name or glob, "" = all
	Hooks      []Hook `json:"hooks"`
	SourceFile string `json:"-"` // path of the settings file this came from; not serialized
}

// IsProjectLocal reports whether this matcher came from a project-scoped
// settings file (i.e. <cwd>/.claude/settings.json or settings.local.json).
// User-global hooks (~/.claude/settings.json, ~/.conduit/conduit.json) return false.
func (h HookMatcher) IsProjectLocal(cwd string) bool {
	if h.SourceFile == "" || cwd == "" {
		return false
	}
	clauDir := filepath.Join(cwd, ".claude") + string(filepath.Separator)
	return strings.HasPrefix(h.SourceFile, clauDir)
}

// HooksSettings is the hooks section.
type HooksSettings struct {
	PreToolUse   []HookMatcher `json:"PreToolUse"`
	PostToolUse  []HookMatcher `json:"PostToolUse"`
	SessionStart []HookMatcher `json:"SessionStart"`
	Stop         []HookMatcher `json:"Stop"`
}

// Settings is the parsed content of one settings file.
type Settings struct {
	Permissions Permissions   `json:"permissions"`
	Hooks       HooksSettings `json:"hooks"`
	// Env holds extra environment variables for tool execution.
	Env map[string]string `json:"env"`
	// EnabledPlugins mirrors the real Claude Code enabledPlugins field.
	// Key is "pluginName@marketplace", value is true/false.
	EnabledPlugins map[string]bool `json:"enabledPlugins,omitempty"`
	// OnboardingComplete is set to true after the first-run welcome screen
	// is dismissed. Mirrors CC's projectOnboardingState gate so returning
	// users skip the welcome.
	OnboardingComplete bool `json:"onboardingComplete,omitempty"`
	// MCP project-scope approval gate (mirrors CC's MCPServerApprovalDialog).
	// A server loaded from .mcp.json is allowed to connect only if its name
	// is in EnabledMcpjsonServers OR EnableAllProjectMcpServers is true.
	// Names in DisabledMcpjsonServers are never connected.
	EnabledMcpjsonServers      []string `json:"enabledMcpjsonServers,omitempty"`
	DisabledMcpjsonServers     []string `json:"disabledMcpjsonServers,omitempty"`
	EnableAllProjectMcpServers bool     `json:"enableAllProjectMcpServers,omitempty"`
	// Model is the preferred model name (e.g. "claude-opus-4-7").
	Model string `json:"model,omitempty"`
	// ActiveProvider is conduit's provider routing selector. It is intentionally
	// richer than "model" so API-key, OAuth/subscription, and MCP-backed
	// providers can carry their own auth and transport fields.
	ActiveProvider *ActiveProviderSettings `json:"activeProvider,omitempty"`
	// Providers is conduit's named provider registry. Roles reference these
	// keys so main/planning/background/implement can choose independently.
	Providers map[string]ActiveProviderSettings `json:"providers,omitempty"`
	// Roles maps role names such as "default" or "implement" to provider keys.
	Roles map[string]string `json:"roles,omitempty"`
	// OutputStyle is the active output style name, persisted across sessions.
	OutputStyle string `json:"outputStyle,omitempty"`
	// Theme is the active palette name (dark|light|dark-daltonized|
	// light-daltonized|dark-ansi|light-ansi). Matches Claude Code's
	// THEME_NAMES so settings.json values are interchangeable.
	Theme string `json:"theme,omitempty"`
	// UsageStatusEnabled enables the conduit-only footer that fetches and
	// renders Claude plan usage windows.
	UsageStatusEnabled bool `json:"usageStatusEnabled,omitempty"`
	// ThemeOverrides applies per-field color tweaks on top of the named
	// theme. Keys are lowercase Palette field names (e.g. "accent",
	// "success"); values are #RRGGBB hex or ANSI 0-15 codes.
	// conduit-only — Claude Code ignores this field.
	ThemeOverrides map[string]string `json:"themeOverrides,omitempty"`
	// Themes is a map of custom theme name → field map (same shape as
	// themeOverrides). Lets users define entirely new themes selectable
	// via /theme. conduit-only.
	Themes map[string]map[string]string `json:"themes,omitempty"`
}

// Merged is the result of loading and merging all settings layers.
type Merged struct {
	// Allow is the combined allow list (user + project + local).
	Allow []string
	// Deny is the combined deny list.
	Deny []string
	// Ask is the combined ask list.
	Ask []string
	// DefaultMode is the effective default permission mode.
	DefaultMode string
	// Hooks is the merged hooks configuration.
	Hooks HooksSettings
	// Env is the merged environment map.
	Env map[string]string
	// AdditionalDirs is the merged set of additional allowed dirs.
	AdditionalDirs []string
	// Model is the preferred model override from settings (last layer wins).
	Model string
	// ActiveProvider is the effective conduit provider routing selector.
	ActiveProvider *ActiveProviderSettings
	// Providers is the merged conduit provider registry.
	Providers map[string]ActiveProviderSettings
	// Roles maps role names to provider keys.
	Roles map[string]string
	// OutputStyle is the active output style name (last layer wins).
	OutputStyle string
	// Theme is the active palette name (last layer wins).
	Theme string
	// UsageStatusEnabled enables the conduit-only plan usage footer.
	UsageStatusEnabled bool
	// ThemeOverrides is the per-field color override map (last layer wins).
	ThemeOverrides map[string]string
	// Themes is the merged custom theme map (last layer wins per name).
	Themes map[string]map[string]string
	// OnboardingComplete is true once any layer sets it.
	OnboardingComplete bool
	// MCP project-scope approval gate. Last layer wins.
	EnabledMcpjsonServers      []string
	DisabledMcpjsonServers     []string
	EnableAllProjectMcpServers bool
}

// ActiveProviderSettings stores conduit's provider routing selector.
type ActiveProviderSettings struct {
	Kind          string `json:"kind"`
	Model         string `json:"model,omitempty"`
	Account       string `json:"account,omitempty"`
	Credential    string `json:"credential,omitempty"`
	Server        string `json:"server,omitempty"`
	DirectTool    string `json:"directTool,omitempty"`
	ImplementTool string `json:"implementTool,omitempty"`
}

const (
	RoleDefault    = "default"
	RoleMain       = "main"
	RolePlanning   = "planning"
	RoleBackground = "background"
	RoleImplement  = "implement"
)

// Load reads and merges settings from all layers for the given cwd.
func Load(cwd string) (*Merged, error) {
	_ = ensureConduitConfigImported()
	return loadPaths(settingsFiles(cwd))
}

// loadPaths merges settings from an explicit list of file paths (testable).
func loadPaths(paths []string) (*Merged, error) {
	merged := &Merged{
		DefaultMode: "default",
		Env:         make(map[string]string),
		Providers:   make(map[string]ActiveProviderSettings),
		Roles:       make(map[string]string),
	}
	for _, path := range paths {
		s, err := readFile(path)
		if err != nil {
			continue // missing or invalid file is skipped
		}
		// Tag every HookMatcher with the file it was loaded from so callers
		// can distinguish project-local hooks from user-global ones.
		tagHookSource(&s.Hooks, path)
		merged.Allow = append(merged.Allow, s.Permissions.Allow...)
		merged.Deny = append(merged.Deny, s.Permissions.Deny...)
		merged.Ask = append(merged.Ask, s.Permissions.Ask...)
		if s.Permissions.DefaultMode != "" {
			merged.DefaultMode = s.Permissions.DefaultMode
		}
		merged.AdditionalDirs = append(merged.AdditionalDirs, s.Permissions.AdditionalDirs...)
		mergeHooks(&merged.Hooks, &s.Hooks)
		for k, v := range s.Env {
			merged.Env[k] = v
		}
		if s.Model != "" {
			merged.Model = s.Model
		}
		if s.ActiveProvider != nil {
			cp := *s.ActiveProvider
			merged.ActiveProvider = &cp
		}
		for k, v := range s.Providers {
			merged.Providers[k] = v
		}
		for k, v := range s.Roles {
			merged.Roles[k] = v
		}
		if s.OutputStyle != "" {
			merged.OutputStyle = s.OutputStyle
		}
		if s.Theme != "" {
			merged.Theme = s.Theme
		}
		if s.UsageStatusEnabled {
			merged.UsageStatusEnabled = true
		}
		if len(s.ThemeOverrides) > 0 {
			if merged.ThemeOverrides == nil {
				merged.ThemeOverrides = map[string]string{}
			}
			for k, v := range s.ThemeOverrides {
				merged.ThemeOverrides[k] = v
			}
		}
		if len(s.Themes) > 0 {
			if merged.Themes == nil {
				merged.Themes = map[string]map[string]string{}
			}
			for k, v := range s.Themes {
				merged.Themes[k] = v
			}
		}
		if s.OnboardingComplete {
			merged.OnboardingComplete = true
		}
		merged.EnabledMcpjsonServers = append(merged.EnabledMcpjsonServers, s.EnabledMcpjsonServers...)
		merged.DisabledMcpjsonServers = append(merged.DisabledMcpjsonServers, s.DisabledMcpjsonServers...)
		if s.EnableAllProjectMcpServers {
			merged.EnableAllProjectMcpServers = true
		}
	}
	return merged, nil
}

func settingsFiles(cwd string) []string {
	paths := []string{}
	conduitExists := false
	if _, err := os.Stat(ConduitSettingsPath()); err == nil {
		conduitExists = true
	}
	if !conduitExists {
		paths = append(paths, filepath.Join(claudeDir(), "settings.json"))
	}
	if cwd != "" {
		paths = append(paths,
			filepath.Join(cwd, ".claude", "settings.json"),
			filepath.Join(cwd, ".claude", "settings.local.json"),
		)
	}
	if conduitExists {
		paths = append(paths, ConduitSettingsPath())
	}
	return paths
}

func readFile(path string) (*Settings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func mergeHooks(dst, src *HooksSettings) {
	dst.PreToolUse = append(dst.PreToolUse, src.PreToolUse...)
	dst.PostToolUse = append(dst.PostToolUse, src.PostToolUse...)
	dst.SessionStart = append(dst.SessionStart, src.SessionStart...)
	dst.Stop = append(dst.Stop, src.Stop...)
}

// tagHookSource sets SourceFile on every HookMatcher in h to path.
func tagHookSource(h *HooksSettings, path string) {
	tag := func(ms []HookMatcher) []HookMatcher {
		for i := range ms {
			ms[i].SourceFile = path
		}
		return ms
	}
	h.PreToolUse = tag(h.PreToolUse)
	h.PostToolUse = tag(h.PostToolUse)
	h.SessionStart = tag(h.SessionStart)
	h.Stop = tag(h.Stop)
}

// FilterUntrustedHooks returns a copy of h with project-local matchers removed
// when trusted is false. When trusted is true or h is nil it returns h unchanged.
// "Project-local" means the hook came from <cwd>/.claude/settings.json or
// <cwd>/.claude/settings.local.json — not from user-global config.
func FilterUntrustedHooks(h *HooksSettings, cwd string, trusted bool) *HooksSettings {
	if trusted || h == nil || cwd == "" {
		return h
	}
	filter := func(ms []HookMatcher) []HookMatcher {
		out := make([]HookMatcher, 0, len(ms))
		for _, m := range ms {
			if !m.IsProjectLocal(cwd) {
				out = append(out, m)
			}
		}
		return out
	}
	return &HooksSettings{
		PreToolUse:   filter(h.PreToolUse),
		PostToolUse:  filter(h.PostToolUse),
		SessionStart: filter(h.SessionStart),
		Stop:         filter(h.Stop),
	}
}

// UserSettingsPath returns the path to the user-global settings file.
func UserSettingsPath() string {
	return filepath.Join(claudeDir(), "settings.json")
}

// ConduitSettingsPath returns conduit's private user settings overlay. Values
// here load after Claude-compatible settings and should only be written by
// conduit-specific features.
func ConduitSettingsPath() string {
	return filepath.Join(ConduitDir(), "conduit.json")
}

// ConduitDir returns conduit's private user configuration directory.
func ConduitDir() string {
	if dir := os.Getenv("CONDUIT_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".conduit"
	}
	return filepath.Join(home, ".conduit")
}

// SaveOutputStyle persists the active output style name to the user settings file.
func SaveOutputStyle(name string) error {
	return SaveConduitOutputStyle(name)
}

// SetPluginEnabled sets enabledPlugins[pluginID] in the user settings file.
func SetPluginEnabled(pluginID string, enabled bool) error {
	return SaveConduitEnabledPlugin(pluginID, enabled)
}

// PluginEnabled reports whether an installed plugin should be active. The
// enabledPlugins map is sparse: missing entries are enabled by default.
func PluginEnabled(pluginID string) bool {
	cfg, err := LoadConduitConfig()
	if err != nil || cfg.EnabledPlugins == nil {
		return true
	}
	enabled, ok := cfg.EnabledPlugins[pluginID]
	if !ok {
		return true
	}
	return enabled
}

// RemovePlugin removes a plugin from enabledPlugins in the user settings file.
func RemovePlugin(pluginID string) error {
	return UpdateConduitConfig(func(cfg *ConduitConfig) {
		if cfg.EnabledPlugins != nil {
			delete(cfg.EnabledPlugins, pluginID)
		}
	})
}

// ApproveMcpjsonServer records an approval decision for a project-scope MCP
// server. Choices: "yes" → add to enabledMcpjsonServers; "yes_all" → set
// enableAllProjectMcpServers=true and add to enabled; "no" → add to
// disabledMcpjsonServers. Idempotent; preserves all other settings keys.
func ApproveMcpjsonServer(name, choice string) error {
	path := ConduitSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := readRawObject(path)
	if err != nil {
		return err
	}

	enabled := decodeStringList(raw["enabledMcpjsonServers"])
	disabled := decodeStringList(raw["disabledMcpjsonServers"])

	switch choice {
	case "yes", "yes_all":
		enabled = appendUnique(enabled, name)
		disabled = removeFrom(disabled, name)
		if choice == "yes_all" {
			raw["enableAllProjectMcpServers"] = json.RawMessage("true")
		}
	case "no":
		disabled = appendUnique(disabled, name)
		enabled = removeFrom(enabled, name)
	default:
		return fmt.Errorf("ApproveMcpjsonServer: unknown choice %q", choice)
	}

	if b, err := json.Marshal(enabled); err == nil {
		raw["enabledMcpjsonServers"] = b
	}
	if b, err := json.Marshal(disabled); err == nil {
		raw["disabledMcpjsonServers"] = b
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func decodeStringList(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	_ = json.Unmarshal(raw, &out)
	return out
}

func appendUnique(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

func removeFrom(list []string, s string) []string {
	out := list[:0]
	for _, x := range list {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

// SavePermissionsField updates a single sub-field under "permissions" in the
// user settings file (e.g. "defaultMode", "allow", "deny") while preserving
// the other sub-fields and all unrelated top-level keys.
//
// Pass value=nil to delete the sub-field. The "permissions" object itself is
// removed if it becomes empty.
func SavePermissionsField(field string, value interface{}) error {
	if field == "" {
		return fmt.Errorf("settings: SavePermissionsField: field is required")
	}
	return savePermissionsField(UserSettingsPath(), "SavePermissionsField", field, value)
}

// SaveConduitPermissionsField updates a single sub-field under "permissions"
// in ~/.conduit/conduit.json. Conduit-owned runtime preferences, such as the
// active permission mode, should use this overlay instead of mutating Claude
// Code's settings.json.
func SaveConduitPermissionsField(field string, value interface{}) error {
	if field == "" {
		return fmt.Errorf("settings: SaveConduitPermissionsField: field is required")
	}
	return savePermissionsField(ConduitSettingsPath(), "SaveConduitPermissionsField", field, value)
}

func savePermissionsField(path, op, field string, value interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("settings: %s: mkdir: %w", op, err)
	}
	raw, err := readRawObject(path)
	if err != nil {
		return fmt.Errorf("settings: %s: read: %w", op, err)
	}

	perms := make(map[string]json.RawMessage)
	if r, ok := raw["permissions"]; ok && len(r) > 0 {
		if err := json.Unmarshal(r, &perms); err != nil {
			return fmt.Errorf("settings: %s: parse permissions: %w", op, err)
		}
	}

	if value == nil {
		delete(perms, field)
	} else {
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("settings: %s: marshal value: %w", op, err)
		}
		perms[field] = encoded
	}

	if len(perms) == 0 {
		delete(raw, "permissions")
	} else {
		encoded, err := json.Marshal(perms)
		if err != nil {
			return fmt.Errorf("settings: %s: marshal permissions: %w", op, err)
		}
		raw["permissions"] = encoded
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("settings: %s: marshal settings: %w", op, err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return fmt.Errorf("settings: %s: write: %w", op, err)
	}
	return nil
}

// SaveRawKey persists an arbitrary key/value to the user settings file using
// raw-map preservation so no other fields are disturbed.
func SaveRawKey(key string, value interface{}) error {
	return SaveConduitRawKey(key, value)
}

// SaveConduitRawKey persists a conduit-only key/value to ~/.conduit/conduit.json
// using raw-map preservation so no other fields are disturbed.
func SaveConduitRawKey(key string, value interface{}) error {
	path := ConduitSettingsPath()
	return saveRawKey(path, key, value)
}

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

func saveRawKey(path, key string, value interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := readRawObject(path)
	if err != nil {
		return err
	}
	if value == nil {
		delete(raw, key)
	} else {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		raw[key] = encoded
	}
	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

func readRawObject(path string) (map[string]json.RawMessage, error) {
	raw := make(map[string]json.RawMessage)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return raw, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return raw, nil
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}
