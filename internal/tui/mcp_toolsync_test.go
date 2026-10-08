package tui

import (
	"context"
	"slices"
	"sort"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/app"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/mcp/mcptest"
	"github.com/icehunter/conduit/internal/tool"
)

func mcpToolNames(reg *tool.Registry, server string) []string {
	prefix := mcp.ToolNamePrefix(server)
	var names []string
	for _, t := range reg.All() {
		if strings.HasPrefix(t.Name(), prefix) {
			names = append(names, t.Name())
		}
	}
	sort.Strings(names)
	return names
}

// mcpPanelAt opens the /mcp panel on server's detail view with action
// selected, the way the user reaches it.
func mcpPanelAt(t *testing.T, m Model, mgr *mcp.Manager, server, action string) Model {
	t.Helper()
	srv, ok := mgr.Server(server)
	if !ok {
		t.Fatalf("%s not listed", server)
	}
	item := panelMCPItem{name: server, status: string(srv.Status), toolCount: len(srv.Tools), disabled: srv.Disabled}
	idx := slices.Index(mcpActions(&item), action)
	if idx < 0 {
		t.Fatalf("%s offers %v, not %q", server, mcpActions(&item), action)
	}
	m.panel = &panelState{tab: panelTabMCP, view: panelViewDetail, mcpItems: []panelMCPItem{item}, selected: idx}
	return m
}

// runCmd runs a command the way Bubble Tea would, off the event loop.
func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("action returned no command: its reconnect never runs")
	}
	return cmd()
}

// Reconnecting, enabling or disabling a server from the TUI used to leave the
// tool registry as it was at startup: reconnected servers' tools never reached
// the model, and disabled ones' stayed callable. The manager's change hook
// re-syncs the registry inside the reconnect, so it is current by the time
// the action's command returns.
func TestMCPActions_RegistryFollowsServer(t *testing.T) {
	cwd := mcptest.IsolateConfig(t)
	t.Chdir(cwd) // the panels scope MCP config to the working directory
	srv := mcptest.NewServer(t, mcp.ToolDef{Name: "search"})
	mcptest.WriteUserServers(t, map[string]mcp.ServerConfig{"docs": {Type: "http", URL: srv.URL}})

	mgr := mcp.NewManager()
	reg := tool.NewRegistry()
	mgr.SetOnChange(func() { app.SyncMCPTools(reg, mgr, nil) })
	if err := mgr.ConnectAll(context.Background(), cwd, true); err != nil {
		t.Fatal(err)
	}
	base := idleModel()
	base.cfg.MCPManager = mgr

	mcpPanel := func(action string) func(t *testing.T) tea.Msg {
		return func(t *testing.T) tea.Msg {
			m := mcpPanelAt(t, base, mgr, "docs", action)
			_, cmd := m.handlePanelKey(keyPress("enter"))
			return runCmd(t, cmd)
		}
	}
	pluginPanel := func(action string) func(t *testing.T) tea.Msg {
		return func(t *testing.T) tea.Msg {
			_, cmd := base.execMCPOptAction(&pluginPanelState{mcpActionTarget: "docs"}, action)
			return runCmd(t, cmd)
		}
	}

	steps := []struct {
		name     string
		before   func()
		act      func(t *testing.T) tea.Msg
		wantMsg  tea.Msg
		want     []string
		disabled bool
	}{
		{"/mcp Reconnect registers the server's new tools", func() { srv.SetTools(mcp.ToolDef{Name: "search"}, mcp.ToolDef{Name: "fetch"}) },
			mcpPanel("Reconnect"), mcpReconnectDoneMsg{}, []string{"mcp__docs__fetch", "mcp__docs__search"}, false},
		{"/mcp Disable unregisters them", nil,
			mcpPanel("Disable"), nil, nil, true},
		{"/mcp Enable registers them again", nil,
			mcpPanel("Enable"), nil, []string{"mcp__docs__fetch", "mcp__docs__search"}, false},
		{"plugin panel Disable unregisters them", nil,
			pluginPanel("Disable"), nil, nil, true},
		{"plugin panel Enable registers them again", nil,
			pluginPanel("Enable"), nil, []string{"mcp__docs__fetch", "mcp__docs__search"}, false},
		{"plugin panel Reconnect picks up a shrunk tool list", func() { srv.SetTools(mcp.ToolDef{Name: "fetch"}) },
			pluginPanel("Reconnect"), nil, []string{"mcp__docs__fetch"}, false},
	}
	for _, st := range steps {
		if st.before != nil {
			st.before()
		}
		if msg := st.act(t); msg != st.wantMsg {
			t.Errorf("%s: command delivered %#v, want %#v", st.name, msg, st.wantMsg)
		}
		if got := mcpToolNames(reg, "docs"); !slices.Equal(got, st.want) {
			t.Errorf("%s: registry has %v, want %v", st.name, got, st.want)
		}
		if got, _ := mgr.Server("docs"); got == nil || got.Disabled != st.disabled {
			t.Errorf("%s: server = %+v, want disabled=%v and still listed", st.name, got, st.disabled)
		}
	}
}

func TestConnectedMCPServerNames(t *testing.T) {
	mgr := mcp.NewManagerWithServers(map[string]*mcp.ConnectedServer{
		"zeta":    {Name: "zeta", Status: mcp.StatusConnected},
		"alpha":   {Name: "alpha", Status: mcp.StatusConnected},
		"pending": {Name: "pending", Status: mcp.StatusPending},
		"broken":  {Name: "broken", Status: mcp.StatusFailed},
	})
	if got, want := connectedMCPServerNames(mgr), []string{"alpha", "zeta"}; !slices.Equal(got, want) {
		t.Errorf("connectedMCPServerNames = %v, want %v", got, want)
	}
}

// A project (.mcp.json) server is held unconnected while the workspace is
// untrusted. Once the user accepts the trust dialog, reconnecting it from the
// /mcp panel has to count the workspace as trusted, or the server stays held
// for the rest of the session even though the user approved it.
func TestMCPPanelReconnectAfterTrustAccepted(t *testing.T) {
	cwd := mcptest.IsolateConfig(t)
	t.Chdir(cwd)
	srv := mcptest.NewServer(t, mcp.ToolDef{Name: "search"})
	mcptest.WriteProjectServers(t, cwd, map[string]mcp.ServerConfig{"proj": {Type: "http", URL: srv.URL}})
	mcptest.ApproveProjectServers(t, cwd, "proj")

	mgr := mcp.NewManager()
	if err := mgr.ConnectAll(context.Background(), cwd, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := mgr.Server("proj"); got == nil || got.Status != mcp.StatusNeedsApproval {
		t.Fatalf("untrusted startup: proj = %+v, want needs-approval", got)
	}

	m := idleModel()
	m.cfg.MCPManager = mgr
	m.cfg.NeedsTrust = true
	next, _ := m.Update(trustAcceptedMsg{})
	m = next.(Model)

	m = mcpPanelAt(t, m, mgr, "proj", "Reconnect")
	_, cmd := m.handlePanelKey(keyPress("enter"))
	runCmd(t, cmd)
	if got, _ := mgr.Server("proj"); got == nil || got.Status != mcp.StatusConnected {
		t.Errorf("after trust accepted, Reconnect left proj = %+v, want connected", got)
	}
}
