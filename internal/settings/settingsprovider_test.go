package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRepairConduitProviderRegistry_TransformsOpenAIResponsesKind(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))

	seed := map[string]any{
		"providers": map[string]any{
			"openai-responses.github-copilot": map[string]any{
				"kind":       "openai-responses",
				"model":      "gpt-5.5",
				"credential": "github-copilot",
				"baseURL":    "https://api.githubcopilot.com",
			},
		},
		"roles": map[string]string{
			RoleDefault: "openai-responses.github-copilot",
		},
	}
	data, err := json.Marshal(seed)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(ConduitSettingsPath()), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(ConduitSettingsPath(), data, 0o600); err != nil {
		t.Fatalf("write conduit.json: %v", err)
	}

	if err := RepairConduitProviderRegistry(); err != nil {
		t.Fatalf("RepairConduitProviderRegistry: %v", err)
	}

	cfg, err := LoadConduitConfig()
	if err != nil {
		t.Fatalf("LoadConduitConfig: %v", err)
	}
	if _, ok := cfg.Providers["openai-responses.github-copilot"]; ok {
		t.Fatalf("legacy provider key still present: %#v", cfg.Providers)
	}
	if _, ok := cfg.Providers["openai-compatible.github-copilot.gpt-5.5"]; !ok {
		t.Fatalf("expected migrated openai-compatible provider: %#v", cfg.Providers)
	}
	if cfg.Roles[RoleDefault] != "openai-compatible.github-copilot.gpt-5.5" {
		t.Fatalf("roles.default = %q, want migrated provider", cfg.Roles[RoleDefault])
	}
}

func TestSaveDiscoveredProviderModels(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))

	base := ActiveProviderSettings{
		Kind:       ProviderKindOpenAICompatible,
		Credential: "gemini-personal",
		BaseURL:    "https://generativelanguage.googleapis.com/v1beta/openai/",
	}
	if err := SaveDiscoveredProviderModels(base, []string{"gemini-1.5-pro"}); err != nil {
		t.Fatalf("save: %v", err)
	}

	cfg, err := LoadConduitConfig()
	if err != nil {
		t.Fatalf("LoadConduitConfig: %v", err)
	}
	if _, ok := cfg.Providers["openai-compatible.gemini-personal.gemini-1.5-pro"]; !ok {
		t.Fatalf("missing discovered model entry")
	}
}
