package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEndpointIdentityKeepsLegacyKeys(t *testing.T) {
	p := ActiveProviderSettings{Kind: ProviderKindOpenAICompatible, Credential: "local", Model: "qwen", BaseURL: "http://one/v1"}
	if got := ProviderKey(p); got != "openai-compatible.local.qwen" {
		t.Fatalf("legacy key %q", got)
	}
	p.EndpointID = "gpu-one"
	first := ProviderKey(p)
	p.EndpointID = "gpu-two"
	second := ProviderKey(p)
	if first == second {
		t.Fatal("endpoint identities collide")
	}
	p.EndpointID = "invalid/endpoint"
	if ValidateProviderSettings(p) == nil {
		t.Fatal("invalid endpoint accepted")
	}
}

func TestCodingPolicyWholeObjectMerge(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, "global"))
	os.MkdirAll(filepath.Dir(ConduitSettingsPath()), 0o700)
	os.WriteFile(ConduitSettingsPath(), []byte(`{"definitionOfDone":{"enabled":true,"checks":[{"id":"test","command":"go test ./..."}]},"episodes":{"enabled":true,"includeContent":true}}`), 0o600)
	os.MkdirAll(filepath.Join(dir, ".conduit"), 0o700)
	os.WriteFile(filepath.Join(dir, ".conduit", "settings.json"), []byte(`{"definitionOfDone":{"enabled":false},"episodes":{"enabled":false}}`), 0o600)
	p, err := LoadCodingPolicies(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.DefinitionOfDone == nil || p.DefinitionOfDone.Enabled || len(p.DefinitionOfDone.Checks) != 0 || p.Episodes == nil || p.Episodes.Enabled || p.Episodes.IncludeContent {
		t.Fatalf("policies=%+v", p)
	}
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.DefinitionOfDone == nil || m.DefinitionOfDone.Enabled {
		t.Fatalf("merged=%+v", m)
	}
}
func TestCodingPoliciesRejectMalformed(t *testing.T) {
	for _, body := range []string{`{"definitionOfDone":{"enabled":"yes"}}`, `{"definitionOfDone":{"enable":true}}`, `{"episodes":{"enabled":true,"typo":1}}`, `{"providerPools":{"gpu":{"members":["a"],"unknown":true}}}`, `{"definitionOfDone":`} {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CONDUIT_CONFIG_DIR", dir)
			os.WriteFile(ConduitSettingsPath(), []byte(body), 0o600)
			if _, err := LoadCodingPolicies(""); err == nil {
				t.Fatal("malformed policy accepted")
			}
		})
	}
}

func TestMalformedPolicySurvivesLegacyLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", dir)
	body := `{"definitionOfDone":{"enabled":true,"checks":[],"typo":1}}`
	os.WriteFile(ConduitSettingsPath(), []byte(body), 0o600)
	_, _ = Load("")
	if _, err := LoadCodingPolicies(""); err == nil {
		t.Fatal("legacy migration erased invalid coding policy")
	}
	data, err := os.ReadFile(ConduitSettingsPath())
	if err != nil || string(data) != body {
		t.Fatalf("policy rewritten: %s (%v)", data, err)
	}
}

func TestRepairPreservesPoolReferences(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", dir)
	body := `{"schemaVersion":1,"providers":{"gpu":{"kind":"openai-compatible","endpointId":"gpu-a","credential":"local","model":"qwen","baseURL":"http://localhost:1234/v1"}},"roles":{"implement":"gpu"},"providerPools":{"local":{"members":["gpu"],"maxConcurrent":{"gpu":2}}},"rolePools":{"implement":"local"}}`
	os.WriteFile(ConduitSettingsPath(), []byte(body), 0o600)
	if err := RepairConduitProviderRegistry(); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConduitConfig()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateProviderPools(cfg.Providers, cfg.ProviderPools, cfg.RolePools); err != nil {
		t.Fatal(err)
	}
	ref := cfg.Roles[RoleImplement]
	if cfg.ProviderPools["local"].Members[0] != ref || cfg.ProviderPools["local"].MaxConcurrent[ref] != 2 {
		t.Fatalf("pool refs %+v", cfg.ProviderPools)
	}
}

func TestPoolRejectsConflictingPhysicalCapacity(t *testing.T) {
	providers := map[string]ActiveProviderSettings{
		"small": {Kind: ProviderKindMCP, Server: "one", EndpointID: "gpu"},
		"large": {Kind: ProviderKindMCP, Server: "two", EndpointID: "gpu"},
	}
	pools := map[string]ProviderPoolSettings{"pool": {Members: []string{"small", "large"}, MaxConcurrent: map[string]int{"small": 1, "large": 4}}}
	if err := ValidateProviderPools(providers, pools, map[string]string{"implement": "pool"}); err == nil {
		t.Fatal("conflicting endpoint capacity accepted")
	}
}
