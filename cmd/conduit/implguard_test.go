package main

import (
	"errors"
	"testing"

	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
)

func TestImplementGuardActive(t *testing.T) {
	api := settings.ActiveProviderSettings{Kind: settings.ProviderKindAnthropicAPI, Credential: "c", Model: "m"}
	mcp := settings.ActiveProviderSettings{Kind: settings.ProviderKindMCP, Server: "local-coder"}
	merged := func(implRef string, impl settings.ActiveProviderSettings) *settings.Merged {
		return &settings.Merged{
			Providers: map[string]settings.ActiveProviderSettings{"api.main": api, implRef: impl},
			Roles: map[string]string{
				settings.RoleMain:      "api.main",
				settings.RoleDefault:   "api.main",
				settings.RolePlanning:  "api.main",
				settings.RoleImplement: implRef,
			},
		}
	}

	tests := []struct {
		name     string
		merged   *settings.Merged
		mode     permissions.Mode
		verified bool
		want     bool
	}{
		{"no settings", nil, permissions.ModeDefault, false, false},
		{"no implement role", &settings.Merged{}, permissions.ModeDefault, false, false},
		{"mcp implementer in default mode", merged("mcp.local-coder", mcp), permissions.ModeDefault, false, true},
		{"mcp implementer in bypass mode", merged("mcp.local-coder", mcp), permissions.ModeBypassPermissions, false, true},
		{"mcp implementer with verified coding", merged("mcp.local-coder", mcp), permissions.ModeAcceptEdits, true, true},
		{"api implementer, agent on it in acceptEdits", merged("api.impl", api), permissions.ModeAcceptEdits, false, false},
		{"api implementer, agent on default role", merged("api.impl", api), permissions.ModeDefault, false, true},
		{"api implementer, agent planning", merged("api.impl", api), permissions.ModePlan, false, true},
		{"api implementer, verified coding keeps main", merged("api.impl", api), permissions.ModeBypassPermissions, true, true},
		{"implementer is the main provider", merged("api.main", api), permissions.ModeDefault, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, label := implementGuardActive(tt.merged, tt.mode, tt.verified)
			if got != tt.want {
				t.Fatalf("active = %v, want %v", got, tt.want)
			}
			if got && label == "" {
				t.Error("active guard has no label")
			}
		})
	}
}

func TestImplementGuardCheckFailsClosedOnLoadError(t *testing.T) {
	load := func(string) (*settings.Merged, error) { return nil, errors.New("boom") }
	if on, label := implementGuardCheck(load, "/x", permissions.ModeDefault, false); !on || label == "" {
		t.Errorf("load error: active = %v, label = %q; want active with a label", on, label)
	}
}
