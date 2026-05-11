package tui

import (
	"path/filepath"
	"testing"

	"github.com/icehunter/conduit/internal/provider/copilot"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
)

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
	auth := copilot.NewAuthorizerForCredential(secure.NewDefault(), copilot.ProviderID)
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
