package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/pendingedits"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
)

func TestVerifiedCodingKeepsMainForEditing(t *testing.T) {
	m := Model{cfg: Config{VerifiedCoding: true}, providers: map[string]settings.ActiveProviderSettings{"local": {Kind: settings.ProviderKindMCP, Server: "coder"}}, roles: map[string]string{settings.RoleImplement: "local"}}
	for _, mode := range []permissions.Mode{permissions.ModeDefault, permissions.ModeAcceptEdits, permissions.ModeAcceptEditsLive, permissions.ModeBypassPermissions} {
		m.permissionMode = mode
		if got := m.currentProviderRole(); got != settings.RoleMain {
			t.Fatalf("mode=%s role=%s", mode, got)
		}
	}
	m.permissionMode = permissions.ModePlan
	if got := m.currentProviderRole(); got != settings.RolePlanning {
		t.Fatalf("plan role=%s", got)
	}
	m.localMode = true
	if _, ok := m.activeMCPProvider(); ok {
		t.Fatal("verified coding bypassed agent via MCP")
	}
}

func TestVerifiedCodingInvalidMainCannotFallBack(t *testing.T) {
	invalid := settings.ActiveProviderSettings{Kind: settings.ProviderKindOpenAICompatible, Model: "explicit-invalid"}
	m := Model{cfg: Config{VerifiedCoding: true, Loop: agent.NewLoop(nil, nil, agent.LoopConfig{}), NewProviderAPIClient: func(p settings.ActiveProviderSettings) (*api.Client, error) {
		if p.Model != invalid.Model {
			t.Fatalf("silently selected fallback provider: %+v", p)
		}
		return nil, errors.New("explicit provider is invalid")
	}}, providers: map[string]settings.ActiveProviderSettings{
		"main":    invalid,
		"default": {Kind: settings.ProviderKindOpenAICompatible, Model: "fallback", BaseURL: "http://localhost:8000/v1"},
	}, roles: map[string]string{settings.RoleMain: "main", settings.RoleDefault: "default"}}
	m.applyEffectiveProviderForMode()
	if m.verifiedProviderErr == nil {
		t.Fatal("explicit invalid provider did not block the run")
	}
}

func TestDiffReviewCancellationHasNoExplicitDecision(t *testing.T) {
	table := pendingedits.NewTable()
	_ = table.Stage(pendingedits.Entry{Path: "file", NewContent: []byte("candidate")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := askDiffReview(ctx, table, func([]pendingedits.Entry, chan<- DiffReviewResult) {})
	if !result.Cancelled || result.ExplicitDecision || len(result.Original) != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestReviewRoleIsSelectable(t *testing.T) {
	if got := nextProviderRole(settings.RoleImplement); got != settings.RoleReview {
		t.Fatalf("next role=%s", got)
	}
	if providerRolePrompt(settings.RoleReview) == providerRolePrompt("") {
		t.Fatal("missing review prompt")
	}
}
