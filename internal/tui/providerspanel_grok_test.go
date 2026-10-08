package tui

import (
	"errors"
	"net/http"
	"testing"

	"github.com/icehunter/conduit/internal/provider/xai"
	"github.com/icehunter/conduit/internal/settings"
)

func TestGrokProviderPresetAndLabels(t *testing.T) {
	found := false
	for _, p := range knownProviders {
		if p.id == xai.ProviderID {
			found = p.isOAuth && p.oauthKind == "grok"
		}
	}
	if !found {
		t.Fatal("Grok / xAI missing from the provider picker as an OAuth preset")
	}
	if providerCredentialAliasLooksSecret(xai.ProviderID) {
		t.Fatalf("default alias %q must not look like a secret", xai.ProviderID)
	}

	provider := settings.ActiveProviderSettings{Kind: settings.ProviderKindOpenAICompatible, Credential: xai.ProviderID + "-2", BaseURL: xai.BaseURL, Model: "grok-4"}
	if !isGrokProvider(provider) {
		t.Fatal("alias with the grok-xai prefix should be a Grok provider")
	}
	if isGrokProvider(settings.ActiveProviderSettings{Credential: "openai", BaseURL: "https://api.x.ai/v1"}) {
		t.Fatal("an API-key provider must not be treated as a Grok OAuth account")
	}
	if got := providerRowLabel(provider); got != "Grok / xAI · credential grok-xai-2" {
		t.Fatalf("row label = %q", got)
	}
	if got := len(providerDetailActionsFor(provider)); got != 3 {
		t.Fatalf("OAuth account actions = %d, want rename/disconnect/back", got)
	}
	f := formForProvider(provider, "key", false)
	if !f.oauthProvider || f.oauthKind != "grok" || f.baseURL != xai.BaseURL {
		t.Fatalf("form = %+v", f)
	}
}

func TestCompleteGrokOAuthFormRequiresModels(t *testing.T) {
	var m Model
	f := &providerFormState{oauthKind: "grok", credential: xai.ProviderID}
	if err := m.completeGrokOAuthForm(f, nil); err == nil {
		t.Fatal("expected error without models")
	}
	f.oauthStarting = true
	if err := m.completeGrokOAuthForm(f, nil); err == nil {
		t.Fatal("expected error while authorization is running")
	}
}

func TestGrokStaleOAuthMessagesAreDropped(t *testing.T) {
	m := Model{settingsPanel: &settingsPanelState{}}
	f := &providerFormState{step: providerFormStepOAuth, oauthKind: "grok", credential: xai.ProviderID}
	m.settingsPanel.providerForm = f
	id := f.beginOAuth()

	// A result from an earlier sign-in must not touch the current form.
	m, _ = m.handleGrokOAuthCompleted(grokOAuthCompletedMsg{id: id - 1, err: errors.New("stale")})
	if f.err != "" {
		t.Fatalf("stale completion applied: %q", f.err)
	}

	// Dismissing the form aborts the sign-in and invalidates its id.
	cancelled := false
	f.oauthCancel = func() { cancelled = true }
	f.cancelOAuth()
	if !cancelled || f.oauthID != 0 || f.oauthCancel != nil {
		t.Fatalf("cancelOAuth left state: cancelled=%v id=%d", cancelled, f.oauthID)
	}
	m, _ = m.handleGrokOAuthCompleted(grokOAuthCompletedMsg{id: id, err: errors.New("late")})
	if f.err != "" {
		t.Fatalf("completion after dismiss applied: %q", f.err)
	}

	// A listener that arrives after dismissal is closed, not leaked.
	flow, err := xai.StartBrowserFlow()
	if err != nil {
		t.Fatal(err)
	}
	m.settingsPanel.providerForm = nil
	if _, cmd := m.handleGrokOAuthStarted(grokOAuthStartedMsg{id: id, flow: flow}); cmd != nil {
		t.Fatal("no wait should be started for a dismissed form")
	}
	if _, err := http.Get(flow.RedirectURI()); err == nil {
		t.Fatal("listener still serving after dismissal")
	}
}
