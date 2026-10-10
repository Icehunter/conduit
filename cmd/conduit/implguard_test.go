package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/icehunter/conduit/internal/implguard"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/mcp/mcptest"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
)

func TestImplementGuardActive(t *testing.T) {
	api := settings.ActiveProviderSettings{Kind: settings.ProviderKindAnthropicAPI, Credential: "c", Model: "m"}
	mcpA := settings.ActiveProviderSettings{Kind: settings.ProviderKindMCP, Server: "coder-a"}
	mcpB := settings.ActiveProviderSettings{Kind: settings.ProviderKindMCP, Server: "coder-b"}
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
	pooled := func() *settings.Merged {
		m := merged("mcp.a", mcpA)
		m.Providers["mcp.b"] = mcpB
		m.ProviderPools = map[string]settings.ProviderPoolSettings{"coders": {Members: []string{"mcp.a", "mcp.b"}}}
		m.RolePools = map[string]string{settings.RoleImplement: "coders"}
		return m
	}
	servers := func(up ...string) providerReachable {
		return func(cands []*settings.ActiveProviderSettings) bool {
			for _, p := range cands {
				if slices.Contains(up, p.Server) {
					return true
				}
			}
			return false
		}
	}

	tests := []struct {
		name            string
		merged          *settings.Merged
		mode            permissions.Mode
		verified        bool
		reachable       providerReachable
		wantActive      bool
		wantUnreachable bool
	}{
		{"nil settings", nil, permissions.ModeDefault, false, servers("coder-a"), false, false},
		{"no implement role", &settings.Merged{}, permissions.ModeDefault, false, servers("coder-a"), false, false},
		{"mcp implementer connected", merged("mcp.a", mcpA), permissions.ModeDefault, false, servers("coder-a"), true, false},
		{"mcp implementer connected, bypass", merged("mcp.a", mcpA), permissions.ModeBypassPermissions, false, servers("coder-a"), true, false},
		{"mcp implementer connected, verified coding", merged("mcp.a", mcpA), permissions.ModeAcceptEdits, true, servers("coder-a"), true, false},
		{"mcp implementer disconnected", merged("mcp.a", mcpA), permissions.ModeDefault, false, servers(), false, true},
		{"mcp implementer: other server up", merged("mcp.a", mcpA), permissions.ModeDefault, false, servers("coder-b"), false, true},
		{"mcp implementer, no probe", merged("mcp.a", mcpA), permissions.ModeDefault, false, nil, false, true},
		{"pool: both up", pooled(), permissions.ModeDefault, false, servers("coder-a", "coder-b"), true, false},
		{"pool: one member up", pooled(), permissions.ModeDefault, false, servers("coder-b"), true, false},
		{"pool: none up", pooled(), permissions.ModeDefault, false, servers(), false, true},
		{"api implementer, agent on it in acceptEdits", merged("api.impl", api), permissions.ModeAcceptEdits, false, servers(), false, false},
		{"api implementer, agent on default role", merged("api.impl", api), permissions.ModeDefault, false, servers(), true, false},
		{"api implementer, agent planning", merged("api.impl", api), permissions.ModePlan, false, servers(), true, false},
		{"api implementer, verified coding keeps main", merged("api.impl", api), permissions.ModeBypassPermissions, true, servers(), true, false},
		{"implementer is the main provider", merged("api.main", api), permissions.ModeDefault, false, servers(), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := implementGuardActive(tt.merged, tt.mode, tt.verified, tt.reachable)
			if st.Active != tt.wantActive || st.Unreachable != tt.wantUnreachable {
				t.Fatalf("status = %+v, want active %v unreachable %v", st, tt.wantActive, tt.wantUnreachable)
			}
			if (st.Active || st.Unreachable) && st.Label == "" {
				t.Error("status has no label")
			}
		})
	}
}

// TestImplementGuardFollowsReachability checks the guard re-probes on every
// call, so a server that connects late or drops mid-session flips it.
func TestImplementGuardFollowsReachability(t *testing.T) {
	mcpA := settings.ActiveProviderSettings{Kind: settings.ProviderKindMCP, Server: "coder-a"}
	m := &settings.Merged{
		Providers: map[string]settings.ActiveProviderSettings{"mcp.a": mcpA},
		Roles:     map[string]string{settings.RoleImplement: "mcp.a"},
	}
	up := false
	g := implguard.NewStatus(func() implguard.Status {
		return implementGuardActive(m, permissions.ModeDefault, false, func([]*settings.ActiveProviderSettings) bool { return up })
	})
	for i, want := range []bool{false, true, false, true} {
		up = want
		if on, _ := g.Active(); on != want {
			t.Fatalf("step %d: active = %v, want %v", i, on, want)
		}
	}
}

func TestImplementGuardCheckFailsClosedOnLoadError(t *testing.T) {
	load := func(string) (*settings.Merged, error) { return nil, errors.New("boom") }
	if st := implementGuardCheck(load, "/x", permissions.ModeDefault, false, nil); !st.Active || st.Label == "" {
		t.Errorf("load error: status = %+v; want active with a label", st)
	}
}

// With the role provider's server down and another pool member up, the guard
// probe uses the same resolution LocalImplement registers with: active while
// any member is up, inactive (Unreachable) once all are down.
func TestImplementGuardPoolMemberUp(t *testing.T) {
	cwd := mcptest.IsolateConfig(t)
	b := mcptest.NewServer(t, mcp.ToolDef{Name: "local_implement"})
	mcptest.WriteUserServers(t, map[string]mcp.ServerConfig{"coder-b": {Type: "http", URL: b.URL}})
	mgr := mcp.NewManager()
	if err := mgr.ConnectAll(context.Background(), cwd, true); err != nil {
		t.Fatal(err)
	}
	api := settings.ActiveProviderSettings{Kind: settings.ProviderKindAnthropicAPI, Credential: "c", Model: "m"}
	m := &settings.Merged{
		Providers: map[string]settings.ActiveProviderSettings{
			"api.main": api,
			"mcp.a":    {Kind: settings.ProviderKindMCP, Server: "coder-a"},
			"mcp.b":    {Kind: settings.ProviderKindMCP, Server: "coder-b"},
		},
		Roles:         map[string]string{settings.RoleMain: "api.main", settings.RoleDefault: "api.main", settings.RoleImplement: "mcp.a"},
		ProviderPools: map[string]settings.ProviderPoolSettings{"coders": {Members: []string{"mcp.a", "mcp.b"}}},
		RolePools:     map[string]string{settings.RoleImplement: "coders"},
	}
	reachable := mcpImplementReachable(mgr)
	if st := implementGuardActive(m, permissions.ModeDefault, false, reachable); !st.Active {
		t.Errorf("member up: status = %+v, want active", st)
	}
	mgr.DisconnectServer("coder-b")
	if st := implementGuardActive(m, permissions.ModeDefault, false, reachable); st.Active || !st.Unreachable {
		t.Errorf("all down: status = %+v, want inactive and unreachable", st)
	}
}
