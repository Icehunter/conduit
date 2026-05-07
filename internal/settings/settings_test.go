package settings

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/secure"
)

func writeSettings(t *testing.T, dir, name string, s Settings) {
	t.Helper()
	data, _ := json.Marshal(s)
	_ = os.MkdirAll(filepath.Join(dir, ".claude"), 0755)
	_ = os.WriteFile(filepath.Join(dir, ".claude", name), data, 0644)
}

// projectPaths returns only the project-level settings paths (no user home).
func projectPaths(dir string) []string {
	return []string{
		filepath.Join(dir, ".claude", "settings.json"),
		filepath.Join(dir, ".claude", "settings.local.json"),
	}
}

func TestLoad_ConduitSettingsOverrideClaudeSettings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	project := filepath.Join(dir, "project")
	writeSettings(t, project, "settings.json", Settings{Model: "claude-sonnet-4-6"})
	if err := SaveConduitRawKey("model", "claude-opus-4-7"); err != nil {
		t.Fatalf("SaveConduitRawKey model: %v", err)
	}
	if err := SaveConduitRawKey("activeProvider", ActiveProviderSettings{Kind: "mcp", Server: "local-router", Model: "qwen3-coder"}); err != nil {
		t.Fatalf("SaveConduitRawKey activeProvider: %v", err)
	}

	merged, err := Load(project)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if merged.Model != "claude-opus-4-7" {
		t.Fatalf("Model = %q, want conduit overlay value", merged.Model)
	}
	if merged.ActiveProvider == nil || merged.ActiveProvider.Kind != "mcp" || merged.ActiveProvider.Server != "local-router" {
		t.Fatalf("ActiveProvider = %#v, want mcp local-router", merged.ActiveProvider)
	}
}

func TestLoad_ImportsClaudeUserSettingsOnceThenConduitWins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Dir(UserSettingsPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(UserSettingsPath(), []byte(`{"model":"claude-sonnet-4-6","theme":"dark"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	merged, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if merged.Model != "claude-sonnet-4-6" || merged.Theme != "dark" {
		t.Fatalf("first load = model %q theme %q, want imported Claude values", merged.Model, merged.Theme)
	}
	if err := SaveConduitModel("claude-opus-4-7"); err != nil {
		t.Fatalf("SaveConduitModel: %v", err)
	}
	if err := os.WriteFile(UserSettingsPath(), []byte(`{"model":"claude-haiku-4-5","theme":"light"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	merged, err = Load("")
	if err != nil {
		t.Fatalf("second Load: %v", err)
	}
	if merged.Model != "claude-opus-4-7" {
		t.Fatalf("second load model = %q, want Conduit value", merged.Model)
	}
	if merged.Theme != "dark" {
		t.Fatalf("second load theme = %q, want imported Conduit value to beat changed Claude global", merged.Theme)
	}
}

func TestLoad_ProviderRoles(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	project := filepath.Join(dir, "project")
	writeSettings(t, project, "settings.json", Settings{
		Providers: map[string]ActiveProviderSettings{
			"local.qwen": {Kind: "mcp", Server: "local-router", Model: "qwen3-coder"},
		},
		Roles: map[string]string{RoleImplement: "local.qwen"},
	})

	merged, err := Load(project)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	provider, ok := merged.ProviderForRole(RoleImplement)
	if !ok {
		t.Fatal("ProviderForRole(implement) not found")
	}
	if provider.Kind != "mcp" || provider.Server != "local-router" {
		t.Fatalf("provider = %#v, want local qwen", provider)
	}
}

func TestProviderForRoleSkipsDeletedAccountProvider(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := SaveConduitRawKey("providers", map[string]ActiveProviderSettings{
		"claude-subscription.deleted@example.com.claude-sonnet-4-6": {
			Kind:    "claude-subscription",
			Account: "deleted@example.com",
			Model:   "claude-sonnet-4-6",
		},
		"mcp.local-router": {
			Kind:   "mcp",
			Server: "local-router",
			Model:  "qwen3-coder",
		},
	}); err != nil {
		t.Fatalf("SaveConduitRawKey providers: %v", err)
	}
	if err := SaveConduitRawKey("roles", map[string]string{
		RoleMain:    "claude-subscription.deleted@example.com.claude-sonnet-4-6",
		RoleDefault: "mcp.local-router",
	}); err != nil {
		t.Fatalf("SaveConduitRawKey roles: %v", err)
	}

	merged, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if provider, ok := merged.ProviderForRole(RoleMain); ok {
		t.Fatalf("main provider = %#v, want deleted account provider skipped", provider)
	}
	if provider, ok := merged.ProviderForRole(RoleDefault); !ok || provider.Kind != "mcp" {
		t.Fatalf("default provider = %#v/%v, want mcp fallback still available", provider, ok)
	}
}

func TestProviderForRoleKeepsSameEmailDifferentAccountKinds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := SaveConduitRawKey("accounts", map[string]any{
		"active": "anthropic-console:same@example.com",
		"accounts": map[string]any{
			"claude-ai:same@example.com": map[string]any{
				"email": "same@example.com",
				"kind":  "claude-ai",
			},
			"anthropic-console:same@example.com": map[string]any{
				"email": "same@example.com",
				"kind":  "anthropic-console",
			},
		},
	}); err != nil {
		t.Fatalf("SaveConduitRawKey accounts: %v", err)
	}
	if err := SaveConduitRawKey("providers", map[string]ActiveProviderSettings{
		"claude-subscription.same@example.com.claude-sonnet-4-6": {
			Kind:    "claude-subscription",
			Account: "same@example.com",
			Model:   "claude-sonnet-4-6",
		},
		"anthropic-api.same@example.com.claude-sonnet-4-6": {
			Kind:    "anthropic-api",
			Account: "same@example.com",
			Model:   "claude-sonnet-4-6",
		},
	}); err != nil {
		t.Fatalf("SaveConduitRawKey providers: %v", err)
	}
	if err := SaveConduitRawKey("roles", map[string]string{
		RoleMain:       "claude-subscription.same@example.com.claude-sonnet-4-6",
		RoleBackground: "anthropic-api.same@example.com.claude-sonnet-4-6",
	}); err != nil {
		t.Fatalf("SaveConduitRawKey roles: %v", err)
	}

	merged, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if provider, ok := merged.ProviderForRole(RoleMain); !ok || provider.Kind != "claude-subscription" {
		t.Fatalf("main provider = %#v/%v, want Claude provider", provider, ok)
	}
	if provider, ok := merged.ProviderForRole(RoleBackground); !ok || provider.Kind != "anthropic-api" {
		t.Fatalf("background provider = %#v/%v, want Anthropic provider", provider, ok)
	}
}

func TestSaveActiveProviderMirrorsDefaultRole(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	value := ActiveProviderSettings{Kind: "mcp", Server: "local-router", Model: "qwen3-coder"}
	if err := SaveActiveProvider(value); err != nil {
		t.Fatalf("SaveActiveProvider: %v", err)
	}
	merged, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defaultProvider, ok := merged.ProviderForRole(RoleDefault)
	if !ok {
		t.Fatal("default provider not found")
	}
	if defaultProvider.Kind != "mcp" || defaultProvider.Server != "local-router" {
		t.Fatalf("default provider = %#v, want local-router", defaultProvider)
	}
	key := ProviderKey(value)
	if merged.Roles[RoleDefault] != key {
		t.Fatalf("roles.default = %q, want %q", merged.Roles[RoleDefault], key)
	}
	if got := merged.Providers[key]; got.Server != "local-router" {
		t.Fatalf("providers[%q] = %#v", key, got)
	}
}

func TestSaveRoleProvider_ClaudeAccountScopedRole(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	value := ActiveProviderSettings{
		Kind:    "claude-subscription",
		Account: "personal@example.com",
		Model:   "claude-opus-4-7",
	}
	if err := SaveRoleProvider(RolePlanning, value); err != nil {
		t.Fatalf("SaveRoleProvider: %v", err)
	}
	merged, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	key := "claude-subscription.personal@example.com.claude-opus-4-7"
	if merged.Roles[RolePlanning] != key {
		t.Fatalf("roles.planning = %q, want %q", merged.Roles[RolePlanning], key)
	}
	if got := merged.Providers[key]; got.Account != "personal@example.com" || got.Model != "claude-opus-4-7" {
		t.Fatalf("providers[%q] = %#v, want account-scoped Claude provider", key, got)
	}
}

func TestProviderKey_AnthropicAPIIncludesAccount(t *testing.T) {
	value := ActiveProviderSettings{
		Kind:    "anthropic-api",
		Account: "api@example.com",
		Model:   "claude-sonnet-4-6",
	}
	if got, want := ProviderKey(value), "anthropic-api.api@example.com.claude-sonnet-4-6"; got != want {
		t.Fatalf("ProviderKey = %q, want %q", got, want)
	}
}

func TestProviderKey_AnthropicAPIIncludesCredentialAndModel(t *testing.T) {
	value := ActiveProviderSettings{
		Kind:       ProviderKindAnthropicAPI,
		Credential: "work-api-key",
		Model:      "claude-custom-deployment",
	}
	if got, want := ProviderKey(value), "anthropic-api.work-api-key.claude-custom-deployment"; got != want {
		t.Fatalf("ProviderKey = %q, want %q", got, want)
	}
}

func TestProviderKey_ClaudeSubscriptionIncludesAccount(t *testing.T) {
	value := ActiveProviderSettings{
		Kind:    "claude-subscription",
		Account: "max@example.com",
		Model:   "claude-sonnet-4-6",
	}
	if got, want := ProviderKey(value), "claude-subscription.max@example.com.claude-sonnet-4-6"; got != want {
		t.Fatalf("ProviderKey = %q, want %q", got, want)
	}
}

func TestProviderKey_OpenAICompatibleIncludesCredentialAndModel(t *testing.T) {
	value := ActiveProviderSettings{
		Kind:       ProviderKindOpenAICompatible,
		Credential: "gemini-personal",
		BaseURL:    "https://generativelanguage.googleapis.com/v1beta/openai/",
		Model:      "gemini-2.5-pro",
	}
	if got, want := ProviderKey(value), "openai-compatible.gemini-personal.gemini-2.5-pro"; got != want {
		t.Fatalf("ProviderKey = %q, want %q", got, want)
	}
}

func TestValidateProviderSettings(t *testing.T) {
	tests := []struct {
		name    string
		input   ActiveProviderSettings
		wantErr bool
	}{
		{"claude-subscription needs model", ActiveProviderSettings{Kind: ProviderKindClaudeSubscription}, true},
		{"claude-subscription valid", ActiveProviderSettings{Kind: ProviderKindClaudeSubscription, Model: "claude-sonnet-4-6"}, false},
		{"anthropic-api needs model", ActiveProviderSettings{Kind: ProviderKindAnthropicAPI}, true},
		{"anthropic-api valid", ActiveProviderSettings{Kind: ProviderKindAnthropicAPI, Model: "claude-opus-4-7"}, false},
		{"mcp needs server", ActiveProviderSettings{Kind: ProviderKindMCP}, true},
		{"mcp valid", ActiveProviderSettings{Kind: ProviderKindMCP, Server: "local-router"}, false},
		{"openai-compatible needs baseURL", ActiveProviderSettings{Kind: ProviderKindOpenAICompatible, Model: "gemini-2.5-pro"}, true},
		{"openai-compatible needs model", ActiveProviderSettings{Kind: ProviderKindOpenAICompatible, BaseURL: "https://example.com/v1"}, true},
		{"openai-compatible needs absolute baseURL", ActiveProviderSettings{Kind: ProviderKindOpenAICompatible, BaseURL: "example.com/v1", Model: "gemini-2.5-pro"}, true},
		{"openai-compatible credential rejects raw key", ActiveProviderSettings{Kind: ProviderKindOpenAICompatible, Credential: "AIzaSyDNm8MyqnbIShouldBeAPIKeyNotAlias", BaseURL: "https://example.com/v1", Model: "gemini-2.5-pro"}, true},
		{"openai-compatible valid", ActiveProviderSettings{Kind: ProviderKindOpenAICompatible, BaseURL: "https://example.com/v1", Model: "gemini-2.5-pro"}, false},
		{"unknown kind", ActiveProviderSettings{Kind: "unknown", Model: "model"}, true},
		{"empty kind", ActiveProviderSettings{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProviderSettings(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateProviderSettings() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateProviderRegistry(t *testing.T) {
	providers := map[string]ActiveProviderSettings{
		"wrong.key": {Kind: ProviderKindOpenAICompatible, Credential: "gemini-personal", BaseURL: "https://example.com/v1", Model: "gemini-2.5-pro"},
	}
	roles := map[string]string{RolePlanning: "missing.provider"}
	errs := ValidateProviderRegistry(providers, roles)
	if len(errs) != 2 {
		t.Fatalf("ValidateProviderRegistry errors = %v, want key mismatch and missing role ref", errs)
	}
}

func TestSaveProviderEntryAndDeleteProviderEntry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	value := ActiveProviderSettings{
		Kind:       ProviderKindOpenAICompatible,
		Credential: "gemini-personal",
		BaseURL:    "https://generativelanguage.googleapis.com/v1beta/openai/",
		Model:      "gemini-2.5-pro",
	}
	if err := SaveProviderEntry(value); err != nil {
		t.Fatalf("SaveProviderEntry: %v", err)
	}
	cfg, err := LoadConduitConfig()
	if err != nil {
		t.Fatalf("LoadConduitConfig: %v", err)
	}
	key := ProviderKey(value)
	if got := cfg.Providers[key]; got.Kind != ProviderKindOpenAICompatible || got.BaseURL == "" {
		t.Fatalf("providers[%q] = %#v, want OpenAI-compatible provider", key, got)
	}
	if err := SaveRoleProvider(RolePlanning, value); err != nil {
		t.Fatalf("SaveRoleProvider: %v", err)
	}
	if err := DeleteProviderEntry(key); err != nil {
		t.Fatalf("DeleteProviderEntry: %v", err)
	}
	cfg, err = LoadConduitConfig()
	if err != nil {
		t.Fatalf("LoadConduitConfig after delete: %v", err)
	}
	if _, ok := cfg.Providers[key]; ok {
		t.Fatalf("providers[%q] still present after delete", key)
	}
	if got := cfg.Roles[RolePlanning]; got != "" {
		t.Fatalf("roles.planning = %q, want cleared", got)
	}
}

func TestClearRoleProvider(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	value := ActiveProviderSettings{Kind: ProviderKindMCP, Server: "local-router", Model: "qwen3-coder"}
	if err := SaveRoleProvider(RoleDefault, value); err != nil {
		t.Fatalf("SaveRoleProvider: %v", err)
	}
	if err := ClearRoleProvider(RoleDefault); err != nil {
		t.Fatalf("ClearRoleProvider: %v", err)
	}
	cfg, err := LoadConduitConfig()
	if err != nil {
		t.Fatalf("LoadConduitConfig: %v", err)
	}
	if cfg.ActiveProvider != nil {
		t.Fatalf("activeProvider = %#v, want nil", cfg.ActiveProvider)
	}
	if got := cfg.Roles[RoleDefault]; got != "" {
		t.Fatalf("roles.default = %q, want cleared", got)
	}
}

func TestProviderCredentialRoundTrip(t *testing.T) {
	store := secure.NewMemoryStorage()
	if err := SaveProviderCredential(store, "gemini-personal", "secret-key"); err != nil {
		t.Fatalf("SaveProviderCredential: %v", err)
	}
	got, err := LoadProviderCredential(store, "gemini-personal")
	if err != nil {
		t.Fatalf("LoadProviderCredential: %v", err)
	}
	if got != "secret-key" {
		t.Fatalf("credential = %q, want secret-key", got)
	}
	if err := DeleteProviderCredential(store, "gemini-personal"); err != nil {
		t.Fatalf("DeleteProviderCredential: %v", err)
	}
	if _, err := LoadProviderCredential(store, "gemini-personal"); !errors.Is(err, secure.ErrNotFound) {
		t.Fatalf("LoadProviderCredential after delete err = %v, want not found", err)
	}
}

func TestSaveConduitRawKey_DoesNotWriteClaudeSettings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := SaveRawKey("model", "claude-sonnet-4-6"); err != nil {
		t.Fatalf("SaveRawKey: %v", err)
	}
	if err := SaveConduitRawKey("activeProvider", ActiveProviderSettings{Kind: "mcp", Server: "local-router", Model: "qwen3-coder"}); err != nil {
		t.Fatalf("SaveConduitRawKey: %v", err)
	}

	if claudeData, err := os.ReadFile(UserSettingsPath()); err == nil && strings.Contains(string(claudeData), "activeProvider") {
		t.Fatalf("Claude settings should not contain activeProvider: %s", claudeData)
	}

	conduitData, err := os.ReadFile(ConduitSettingsPath())
	if err != nil {
		t.Fatalf("read conduit settings: %v", err)
	}
	if !strings.Contains(string(conduitData), "activeProvider") {
		t.Fatalf("Conduit settings should contain activeProvider: %s", conduitData)
	}
}

func TestApproveMcpjsonServer_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := ApproveMcpjsonServer("foo", "yes"); err != nil {
		t.Fatalf("approve yes: %v", err)
	}
	merged := loadPaths([]string{ConduitSettingsPath()})
	if !contains(merged.EnabledMcpjsonServers, "foo") {
		t.Errorf("expected 'foo' in EnabledMcpjsonServers; got %v", merged.EnabledMcpjsonServers)
	}

	// "no" moves it to disabled and removes from enabled.
	if err := ApproveMcpjsonServer("foo", "no"); err != nil {
		t.Fatalf("approve no: %v", err)
	}
	merged = loadPaths([]string{ConduitSettingsPath()})
	if contains(merged.EnabledMcpjsonServers, "foo") {
		t.Errorf("'foo' should have been removed from enabled; got %v", merged.EnabledMcpjsonServers)
	}
	if !contains(merged.DisabledMcpjsonServers, "foo") {
		t.Errorf("expected 'foo' in DisabledMcpjsonServers; got %v", merged.DisabledMcpjsonServers)
	}
}

func TestApproveMcpjsonServer_YesAllSetsFlag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := ApproveMcpjsonServer("bar", "yes_all"); err != nil {
		t.Fatalf("approve yes_all: %v", err)
	}
	merged := loadPaths([]string{ConduitSettingsPath()})
	if !merged.EnableAllProjectMcpServers {
		t.Errorf("EnableAllProjectMcpServers should be true after yes_all")
	}
	if !contains(merged.EnabledMcpjsonServers, "bar") {
		t.Errorf("'bar' should also be in EnabledMcpjsonServers; got %v", merged.EnabledMcpjsonServers)
	}
}

func TestApproveMcpjsonServer_ProjectScoped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))
	cwdA := filepath.Join(dir, "project-a")
	cwdB := filepath.Join(dir, "project-b")

	if err := ApproveMcpjsonServer("srv", "yes", cwdA); err != nil {
		t.Fatalf("approve project: %v", err)
	}
	a, err := Load(cwdA)
	if err != nil {
		t.Fatalf("Load project A: %v", err)
	}
	if !contains(a.EnabledMcpjsonServers, "srv") {
		t.Fatalf("project A enabled = %v, want srv", a.EnabledMcpjsonServers)
	}
	b, err := Load(cwdB)
	if err != nil {
		t.Fatalf("Load project B: %v", err)
	}
	if contains(b.EnabledMcpjsonServers, "srv") {
		t.Fatalf("project B unexpectedly inherited approval: %v", b.EnabledMcpjsonServers)
	}
}

func TestApproveMcpjsonServer_PreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// Pre-populate settings with an unrelated key.
	_ = SaveRawKey("model", "claude-sonnet-4-6")

	if err := ApproveMcpjsonServer("baz", "yes"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	data, _ := os.ReadFile(ConduitSettingsPath())
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	if _, ok := raw["model"]; !ok {
		t.Errorf("model key was clobbered by ApproveMcpjsonServer; raw=%s", data)
	}
	if _, ok := raw["enabledMcpjsonServers"]; !ok {
		t.Errorf("enabledMcpjsonServers missing; raw=%s", data)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestSavePermissionsField_PreservesSiblings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// Pre-populate with a permissions block that has allow/deny and a
	// sibling top-level key.
	seed := map[string]any{
		"model": "claude-sonnet-4-6",
		"permissions": map[string]any{
			"allow": []string{"Bash(git status)"},
			"deny":  []string{"Bash(rm -rf /)"},
		},
	}
	seedData, _ := json.Marshal(seed)
	_ = os.MkdirAll(filepath.Dir(UserSettingsPath()), 0o755)
	if err := os.WriteFile(UserSettingsPath(), seedData, 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	if err := SavePermissionsField("defaultMode", "plan"); err != nil {
		t.Fatalf("SavePermissionsField: %v", err)
	}

	data, err := os.ReadFile(UserSettingsPath())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got struct {
		Model       string `json:"model"`
		Permissions struct {
			Allow       []string `json:"allow"`
			Deny        []string `json:"deny"`
			DefaultMode string   `json:"defaultMode"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, data)
	}
	if got.Permissions.DefaultMode != "plan" {
		t.Errorf("defaultMode = %q, want plan", got.Permissions.DefaultMode)
	}
	if len(got.Permissions.Allow) != 1 || got.Permissions.Allow[0] != "Bash(git status)" {
		t.Errorf("allow clobbered: %v", got.Permissions.Allow)
	}
	if len(got.Permissions.Deny) != 1 || got.Permissions.Deny[0] != "Bash(rm -rf /)" {
		t.Errorf("deny clobbered: %v", got.Permissions.Deny)
	}
	if got.Model != "claude-sonnet-4-6" {
		t.Errorf("model clobbered: %q", got.Model)
	}
}

func TestSavePermissionsField_CreatesPermissionsObject(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := SavePermissionsField("defaultMode", "acceptEdits"); err != nil {
		t.Fatalf("SavePermissionsField: %v", err)
	}

	data, _ := os.ReadFile(UserSettingsPath())
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	perms, ok := raw["permissions"]
	if !ok {
		t.Fatalf("permissions key missing; raw=%s", data)
	}
	var p map[string]any
	_ = json.Unmarshal(perms, &p)
	if p["defaultMode"] != "acceptEdits" {
		t.Errorf("defaultMode = %v", p["defaultMode"])
	}
}

func TestSaveConduitPermissionsField_WritesConduitOverlay(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))

	if err := SaveConduitPermissionsField("defaultMode", "plan"); err != nil {
		t.Fatalf("SaveConduitPermissionsField: %v", err)
	}

	if _, err := os.Stat(UserSettingsPath()); !os.IsNotExist(err) {
		t.Fatalf("Claude settings should not be written, stat err=%v", err)
	}
	data, err := os.ReadFile(ConduitSettingsPath())
	if err != nil {
		t.Fatalf("read conduit settings: %v", err)
	}
	var got Settings
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Permissions.DefaultMode != "plan" {
		t.Errorf("defaultMode = %q, want plan", got.Permissions.DefaultMode)
	}
}

func TestSavePermissionsField_NilDeletes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := SavePermissionsField("defaultMode", "plan"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := SavePermissionsField("defaultMode", nil); err != nil {
		t.Fatalf("delete: %v", err)
	}

	data, _ := os.ReadFile(UserSettingsPath())
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	// Empty permissions object should be removed entirely.
	if _, ok := raw["permissions"]; ok {
		t.Errorf("permissions key should be removed when empty; raw=%s", data)
	}
}

func TestSavePermissionsField_EmptyFieldErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := SavePermissionsField("", "plan"); err == nil {
		t.Error("expected error for empty field")
	}
}

func TestPluginEnabled_DefaultsMissingEntriesToEnabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if !PluginEnabled("alpha@market") {
		t.Fatal("missing enabledPlugins entry should default to enabled")
	}
	if err := SetPluginEnabled("alpha@market", false); err != nil {
		t.Fatalf("SetPluginEnabled false: %v", err)
	}
	if PluginEnabled("alpha@market") {
		t.Fatal("explicit false should disable plugin")
	}
	if !PluginEnabled("beta@market") {
		t.Fatal("unlisted plugin should remain enabled when map has other entries")
	}
	if err := SetPluginEnabled("alpha@market", true); err != nil {
		t.Fatalf("SetPluginEnabled true: %v", err)
	}
	if !PluginEnabled("alpha@market") {
		t.Fatal("explicit true should enable plugin")
	}
}

func TestSettingsWrites_DoNotOverwriteInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	before := []byte(`{"model":`)

	writes := []struct {
		name string
		path string
		fn   func() error
	}{
		{"SaveRawKey", ConduitSettingsPath(), func() error { return SaveRawKey("model", "new") }},
		{"SavePermissionsField", UserSettingsPath(), func() error { return SavePermissionsField("defaultMode", "plan") }},
		{"ApproveMcpjsonServer", ConduitSettingsPath(), func() error { return ApproveMcpjsonServer("srv", "yes") }},
		{"SaveOutputStyle", ConduitSettingsPath(), func() error { return SaveOutputStyle("default") }},
	}
	for _, tt := range writes {
		if err := os.MkdirAll(filepath.Dir(tt.path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tt.path, before, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := tt.fn(); err == nil {
			t.Fatalf("%s should fail on invalid existing JSON", tt.name)
		}
		after, err := os.ReadFile(tt.path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatalf("%s overwrote invalid JSON: %q", tt.name, after)
		}
	}
}

func TestLoad_Empty(t *testing.T) {
	dir := t.TempDir()
	m := loadPaths(projectPaths(dir))
	if m.DefaultMode != "default" {
		t.Errorf("DefaultMode = %q", m.DefaultMode)
	}
	if len(m.Allow) != 0 || len(m.Deny) != 0 {
		t.Error("expected empty allow/deny")
	}
}

func TestLoad_ProjectSettings(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "settings.json", Settings{
		Permissions: Permissions{
			Allow: []string{"Bash(git status)"},
			Deny:  []string{"Bash(rm *)"},
		},
	})
	m := loadPaths(projectPaths(dir))
	if len(m.Allow) != 1 || m.Allow[0] != "Bash(git status)" {
		t.Errorf("allow not loaded; got %v", m.Allow)
	}
	if len(m.Deny) != 1 || m.Deny[0] != "Bash(rm *)" {
		t.Errorf("deny not loaded; got %v", m.Deny)
	}
}

func TestLoad_LocalOverridesProject(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "settings.json", Settings{
		Permissions: Permissions{DefaultMode: "acceptEdits"},
	})
	writeSettings(t, dir, "settings.local.json", Settings{
		Permissions: Permissions{DefaultMode: "bypassPermissions"},
	})
	m := loadPaths(projectPaths(dir))
	if m.DefaultMode != "bypassPermissions" {
		t.Errorf("local should override project; got %q", m.DefaultMode)
	}
}

func TestLoad_MergesAllowLists(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "settings.json", Settings{
		Permissions: Permissions{Allow: []string{"Bash(git log)"}},
	})
	writeSettings(t, dir, "settings.local.json", Settings{
		Permissions: Permissions{Allow: []string{"Bash(git status)"}},
	})
	m := loadPaths(projectPaths(dir))
	if len(m.Allow) != 2 {
		t.Errorf("expected 2 allow entries; got %v", m.Allow)
	}
}

func TestLoad_Hooks(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "settings.json", Settings{
		Hooks: HooksSettings{
			PreToolUse: []HookMatcher{{
				Matcher: "Bash",
				Hooks:   []Hook{{Type: "command", Command: "echo hi"}},
			}},
		},
	})
	m := loadPaths(projectPaths(dir))
	if len(m.Hooks.PreToolUse) != 1 {
		t.Errorf("expected 1 PreToolUse matcher; got %v", m.Hooks.PreToolUse)
	}
}

func TestHookMatcher_IsProjectLocal(t *testing.T) {
	cwd := "/home/user/myproject"
	cases := []struct {
		source string
		want   bool
	}{
		{filepath.Join(cwd, ".claude", "settings.json"), true},
		{filepath.Join(cwd, ".claude", "settings.local.json"), true},
		{"/home/user/.claude/settings.json", false}, // user-global
		{"/home/user/.conduit/conduit.json", false}, // conduit global
		{"", false}, // untagged
		{"/other/project/.claude/settings.json", false}, // different project
	}
	for _, tc := range cases {
		m := HookMatcher{SourceFile: tc.source}
		got := m.IsProjectLocal(cwd)
		if got != tc.want {
			t.Errorf("IsProjectLocal(%q) for cwd=%q = %v; want %v", tc.source, cwd, got, tc.want)
		}
	}
}

func TestFilterUntrustedHooks_RemovesProjectLocal(t *testing.T) {
	cwd := t.TempDir()
	clauDir := filepath.Join(cwd, ".claude")
	_ = os.MkdirAll(clauDir, 0755)

	projectLocal := HookMatcher{
		Matcher:    "",
		SourceFile: filepath.Join(clauDir, "settings.local.json"),
	}
	userGlobal := HookMatcher{
		Matcher:    "",
		SourceFile: "/home/user/.claude/settings.json",
	}
	h := &HooksSettings{
		PreToolUse:   []HookMatcher{projectLocal, userGlobal},
		SessionStart: []HookMatcher{projectLocal},
	}

	// Untrusted — project-local hooks must be stripped.
	filtered := FilterUntrustedHooks(h, cwd, false)
	if len(filtered.PreToolUse) != 1 {
		t.Errorf("untrusted: PreToolUse len = %d; want 1 (only user-global)", len(filtered.PreToolUse))
	}
	if filtered.PreToolUse[0].SourceFile != userGlobal.SourceFile {
		t.Errorf("untrusted: remaining matcher should be user-global, got %q", filtered.PreToolUse[0].SourceFile)
	}
	if len(filtered.SessionStart) != 0 {
		t.Errorf("untrusted: SessionStart len = %d; want 0", len(filtered.SessionStart))
	}

	// Trusted — all hooks pass through unchanged.
	trusted := FilterUntrustedHooks(h, cwd, true)
	if trusted != h {
		t.Error("trusted: expected original pointer returned unchanged")
	}
}

func TestFilterUntrustedHooks_NilSafe(t *testing.T) {
	if got := FilterUntrustedHooks(nil, "/some/cwd", false); got != nil {
		t.Errorf("nil input returned %v; want nil", got)
	}
}

func TestLoadPaths_TagsHookSource(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".claude"), 0755)
	writeSettings(t, dir, "settings.json", Settings{
		Hooks: HooksSettings{
			PreToolUse: []HookMatcher{{Matcher: ""}},
		},
	})
	m := loadPaths(projectPaths(dir))
	if len(m.Hooks.PreToolUse) == 0 {
		t.Fatal("expected at least one PreToolUse matcher")
	}
	src := m.Hooks.PreToolUse[0].SourceFile
	if src == "" {
		t.Error("SourceFile not tagged — should be set to the settings file path")
	}
	if !strings.Contains(src, ".claude") {
		t.Errorf("SourceFile %q doesn't look like a .claude path", src)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".claude"), 0755)
	_ = os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte("{bad json}"), 0644)
	m := loadPaths(projectPaths(dir))
	if len(m.Allow) != 0 {
		t.Error("invalid file should be skipped")
	}
}
