package tui

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/icehunter/conduit/internal/catalog"
	"github.com/icehunter/conduit/internal/provider/copilot"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

// emptyStorage is a mock secure.Storage that always returns ErrNotFound.
// Used to isolate tests from real keychain credentials.
type emptyStorage struct{}

func (e *emptyStorage) Get(_, _ string) ([]byte, error) { return nil, secure.ErrNotFound }
func (e *emptyStorage) Set(_, _ string, _ []byte) error { return nil }
func (e *emptyStorage) Delete(_, _ string) error        { return nil }

func TestAdvanceProviderForm_SkipsOAuthForNonOAuthProvider(t *testing.T) {
	m := Model{}
	f := &providerFormState{
		step:          providerFormStepAPIKey,
		input:         "secret-key",
		oauthProvider: false,
	}

	if err := m.advanceProviderForm(f, false, false); err != nil {
		t.Fatalf("advanceProviderForm: %v", err)
	}
	if f.step == providerFormStepOAuth {
		t.Fatalf("step = %v; want non-OAuth completion", f.step)
	}
}

func TestCodexCatalogModelIDs(t *testing.T) {
	tests := []struct {
		name string
		cat  *catalog.Catalog
		want []string
	}{
		{
			name: "nil catalog returns nil",
			cat:  nil,
			want: nil,
		},
		{
			name: "empty catalog returns nil",
			cat:  &catalog.Catalog{},
			want: nil,
		},
		{
			name: "filters to openai provider only",
			cat: &catalog.Catalog{Models: []catalog.ModelInfo{
				{ID: "anthropic/claude-opus-4-8", Provider: "anthropic"},
				{ID: "openai/gpt-5.5", Provider: "openai"},
			}},
			want: []string{"gpt-5.5"},
		},
		{
			name: "excludes batch, image, audio, oss, chat-latest, and o-series/legacy",
			cat: &catalog.Catalog{Models: []catalog.ModelInfo{
				{ID: "openai/gpt-5.5", Provider: "openai"},
				{ID: "openai/gpt-5.5:batch", Provider: "openai"},
				{ID: "openai/gpt-5.4-image-2", Provider: "openai"},
				{ID: "openai/gpt-audio", Provider: "openai"},
				{ID: "openai/gpt-oss-120b", Provider: "openai"},
				{ID: "openai/gpt-chat-latest", Provider: "openai"},
				{ID: "openai/o3-mini", Provider: "openai"},
				{ID: "openai/gpt-4o", Provider: "openai"},
				{ID: "openai/gpt-3.5-turbo", Provider: "openai"},
			}},
			want: []string{"gpt-5.5"},
		},
		{
			name: "includes full gpt-5.x/6.x family, sorted and deduped",
			cat: &catalog.Catalog{Models: []catalog.ModelInfo{
				{ID: "openai/gpt-6-astra", Provider: "openai"},
				{ID: "openai/gpt-5.1-codex-max", Provider: "openai"},
				{ID: "openai/gpt-5", Provider: "openai"},
				{ID: "openai/gpt-5", Provider: "openai"},
			}},
			want: []string{"gpt-5", "gpt-5.1-codex-max", "gpt-6-astra"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := codexCatalogModelIDs(tt.cat)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("codexCatalogModelIDs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCompleteCopilotOAuthForm_PrunesStaleProviders(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(dir, ".conduit"))

	providers := map[string]settings.ActiveProviderSettings{
		"openai-compatible.github-copilot.claude-haiku-4.5": {
			Kind:       settings.ProviderKindOpenAICompatible,
			Credential: copilot.ProviderID,
			BaseURL:    copilot.ChatBaseURL,
			Model:      "claude-haiku-4.5",
		},
		"openai-compatible.github-copilot.gpt-5-mini": {
			Kind:       settings.ProviderKindOpenAICompatible,
			Credential: copilot.ProviderID,
			BaseURL:    copilot.ChatBaseURL,
			Model:      "gpt-5-mini",
		},
		"openai-compatible.gemini.gemini-2.5-pro": {
			Kind:       settings.ProviderKindOpenAICompatible,
			Credential: "gemini",
			BaseURL:    "https://generativelanguage.googleapis.com/v1beta/openai/",
			Model:      "gemini-2.5-pro",
		},
	}
	roles := map[string]string{
		settings.RoleDefault: "openai-compatible.github-copilot.claude-haiku-4.5",
	}
	if err := settings.SaveConduitRawKey("providers", providers); err != nil {
		t.Fatalf("save providers: %v", err)
	}
	if err := settings.SaveConduitRawKey("roles", roles); err != nil {
		t.Fatalf("save roles: %v", err)
	}

	m := Model{providers: cloneProviderMap(providers), roles: cloneStringMap(roles)}
	f := &providerFormState{credential: copilot.ProviderID}
	if err := m.completeCopilotOAuthForm(f, []string{"gpt-5"}); err != nil {
		t.Fatalf("completeCopilotOAuthForm: %v", err)
	}

	cfg, err := settings.LoadConduitConfig()
	if err != nil {
		t.Fatalf("LoadConduitConfig: %v", err)
	}
	if _, ok := cfg.Providers["openai-compatible.github-copilot.claude-haiku-4.5"]; ok {
		t.Fatalf("stale copilot model still present")
	}
	if _, ok := cfg.Providers["openai-compatible.github-copilot.gpt-5-mini"]; ok {
		t.Fatalf("stale copilot model still present")
	}
	if _, ok := cfg.Providers["openai-compatible.github-copilot.gpt-5"]; !ok {
		t.Fatalf("expected refreshed copilot model")
	}
	if cfg.Roles[settings.RoleDefault] == "openai-compatible.github-copilot.claude-haiku-4.5" {
		t.Fatalf("roles.default still points at removed provider")
	}
}

func TestDiscoverCopilotModels_NoFallbackOnFailure(t *testing.T) {
	// Use empty storage to isolate from real keychain credentials
	auth := copilot.NewAuthorizerForCredential(&emptyStorage{}, copilot.ProviderID)
	msg := discoverCopilotModels(auth, "forced failure")
	if completed, ok := msg.(copilotOAuthCompletedMsg); !ok || completed.err == nil {
		t.Fatalf("expected copilotOAuthCompletedMsg with error, got %#v", msg)
	}
}

func TestProviderRowsDisplayUsesAliasOnly(t *testing.T) {
	m := Model{
		providers: map[string]settings.ActiveProviderSettings{
			"openai-compatible.github-copilot.gpt-5": {
				Kind:       settings.ProviderKindOpenAICompatible,
				Credential: copilot.ProviderID,
				BaseURL:    copilot.ChatBaseURL,
				Model:      "gpt-5",
			},
		},
	}
	rows := m.providerRows()
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	label := providerRowLabel(rows[0].provider)
	if label != "GitHub Copilot · credential github-copilot" {
		t.Fatalf("label = %q, want alias-only provider display", label)
	}
	geminiLabel := providerRowLabel(settings.ActiveProviderSettings{Kind: settings.ProviderKindOpenAICompatible, Credential: "gemini", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai/"})
	if geminiLabel != "Gemini · credential gemini" {
		t.Fatalf("gemini label = %q, want Gemini credential display", geminiLabel)
	}
	geminiBaseURLLabel := providerRowLabel(settings.ActiveProviderSettings{Kind: settings.ProviderKindOpenAICompatible, Credential: "", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai/"})
	if geminiBaseURLLabel != "Gemini" {
		t.Fatalf("gemini baseURL label = %q, want Gemini", geminiBaseURLLabel)
	}
}
