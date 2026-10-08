package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/mcp/mcptest"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/tools/localhelpertool"
	"github.com/icehunter/conduit/internal/tools/localimplementtool"
	"github.com/icehunter/conduit/internal/tools/mcpauthtool"
	"github.com/icehunter/conduit/internal/tools/mcptool"
	"github.com/icehunter/conduit/internal/tools/toolsearchtool"
)

// builtinTool stands in for a tool SyncMCPTools doesn't own.
type builtinTool struct{ name string }

func (b builtinTool) Name() string                           { return b.name }
func (b builtinTool) Description() string                    { return "builtin" }
func (b builtinTool) InputSchema() json.RawMessage           { return json.RawMessage(`{"type":"object"}`) }
func (b builtinTool) IsReadOnly(json.RawMessage) bool        { return true }
func (b builtinTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (b builtinTool) Execute(context.Context, json.RawMessage) (tool.Result, error) {
	return tool.TextResult("ok"), nil
}

// mcpDerivedNames lists the registry's tools SyncMCPTools manages, sorted.
func mcpDerivedNames(reg *tool.Registry) []string {
	var names []string
	for _, t := range reg.All() {
		if isMCPDerived(t) {
			names = append(names, t.Name())
		}
	}
	sort.Strings(names)
	return names
}

func defs(names ...string) []mcp.ToolDef {
	out := make([]mcp.ToolDef, 0, len(names))
	for _, n := range names {
		out = append(out, mcp.ToolDef{Name: n, Description: n + " tool"})
	}
	return out
}

// The registry follows every server state change made after BuildRegistry:
// before this, MCP tools were registered once at startup, so a server that
// connected later, reconnected or finished OAuth never got its tools to the
// model until a restart, and a disabled or removed server left stale tools.
func TestFollowMCPTools_RegistryTracksServerChanges(t *testing.T) {
	cwd := mcptest.IsolateConfig(t)
	docs := mcptest.NewServer(t, defs("search")...)
	local := mcptest.NewServer(t, defs("local_direct", "local_implement")...)
	servers := map[string]mcp.ServerConfig{
		"docs":         {Type: "http", URL: docs.URL},
		"local-router": {Type: "http", URL: local.URL},
	}
	mcptest.WriteUserServers(t, servers)

	ctx := context.Background()
	mgr := mcp.NewManager()
	reg := tool.NewRegistry()
	reg.Register(builtinTool{name: "Bash"})
	followMCPTools(reg, mgr, nil)
	reconnect := func(name string) func() error {
		return func() error { return mgr.Reconnect(ctx, name, cwd, true) }
	}
	localTools := []string{"LocalHelper", "LocalImplement", "mcp__local_router__local_direct", "mcp__local_router__local_implement"}

	steps := []struct {
		name string
		act  func() error
		want []string
	}{
		{"startup connect", func() error { return mgr.ConnectAll(ctx, cwd, true) },
			append([]string{"mcp__docs__search"}, localTools...)},
		{"reconnect picks up a changed tool list", func() error {
			docs.SetTools(defs("fetch", "search")...)
			return reconnect("docs")()
		}, append([]string{"mcp__docs__fetch", "mcp__docs__search"}, localTools...)},
		{"server demands OAuth", func() error {
			docs.SetUnauthorized(true)
			return reconnect("docs")()
		}, append([]string{"mcp__docs__authenticate"}, localTools...)},
		{"OAuth done, reconnect", func() error {
			docs.SetUnauthorized(false)
			return reconnect("docs")()
		}, append([]string{"mcp__docs__fetch", "mcp__docs__search"}, localTools...)},
		{"disable", func() error {
			if err := mcp.SetDisabled("docs", cwd, true); err != nil {
				return err
			}
			return reconnect("docs")()
		}, localTools},
		{"enable", func() error {
			if err := mcp.SetDisabled("docs", cwd, false); err != nil {
				return err
			}
			return reconnect("docs")()
		}, append([]string{"mcp__docs__fetch", "mcp__docs__search"}, localTools...)},
		{"local server removed", func() error {
			delete(servers, "local-router")
			mcptest.WriteUserServers(t, servers)
			mgr.DisconnectServer("local-router")
			return nil
		}, []string{"mcp__docs__fetch", "mcp__docs__search"}},
	}
	for _, st := range steps {
		if err := st.act(); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		want := slices.Clone(st.want)
		sort.Strings(want)
		if got := mcpDerivedNames(reg); !slices.Equal(got, want) {
			t.Errorf("%s: MCP tools = %v, want %v", st.name, got, want)
		}
		if _, ok := reg.Lookup("Bash"); !ok {
			t.Fatalf("%s: a built-in tool was dropped", st.name)
		}
	}
}

// A tool registered by the sync must reach the server that serves it, and
// one left over from an earlier connect must not survive a re-list.
func TestSyncMCPTools_RegisteredToolCallsServer(t *testing.T) {
	cwd := mcptest.IsolateConfig(t)
	srv := mcptest.NewServer(t, defs("ping")...)
	mcptest.WriteUserServers(t, map[string]mcp.ServerConfig{"echo": {Type: "http", URL: srv.URL}})
	mgr := mcp.NewManager()
	reg := tool.NewRegistry()
	followMCPTools(reg, mgr, nil)
	if err := mgr.ConnectAll(context.Background(), cwd, true); err != nil {
		t.Fatal(err)
	}
	ping, ok := reg.Lookup("mcp__echo__ping")
	if !ok {
		t.Fatal("mcp__echo__ping not registered after connect")
	}
	res, err := ping.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil || res.IsError || len(res.Content) == 0 || res.Content[0].Text != "ok" {
		t.Fatalf("Execute = %+v, %v", res, err)
	}
}

// Change notifications arrive from several goroutines at once (every
// server's connect runs on its own). However they interleave, the registry
// has to end up matching the manager.
func TestSyncMCPTools_ConcurrentChangesConverge(t *testing.T) {
	servers := map[string]*mcp.ConnectedServer{}
	for i := range 8 {
		name := fmt.Sprintf("s%d", i)
		servers[name] = &mcp.ConnectedServer{Name: name, Status: mcp.StatusConnected, Tools: defs("a", "b")}
	}
	mgr := mcp.NewManagerWithServers(servers)
	reg := tool.NewRegistry()
	reg.Register(builtinTool{name: "Bash"})
	followMCPTools(reg, mgr, nil)
	if got := len(mcpDerivedNames(reg)); got != 16 {
		t.Fatalf("registered %d MCP tools, want 16", got)
	}

	var wg sync.WaitGroup
	for name := range servers {
		if name == "s0" {
			continue
		}
		wg.Go(func() { mgr.DisconnectServer(name) })
		wg.Go(func() { SyncMCPTools(reg, mgr, nil) })
		wg.Go(func() {
			_, _ = reg.Lookup("mcp__" + name + "__a")
			_ = reg.All()
		})
	}
	wg.Wait()

	got := mcpDerivedNames(reg)
	if want := []string{"mcp__s0__a", "mcp__s0__b"}; !slices.Equal(got, want) {
		t.Errorf("MCP tools = %v, want %v", got, want)
	}
	if _, ok := reg.Lookup("Bash"); !ok {
		t.Error("built-in tool dropped")
	}
}

func TestSyncMCPTools_NilArgsAreNoOps(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(builtinTool{name: "Bash"})
	SyncMCPTools(reg, nil, nil)
	SyncMCPTools(nil, mcp.NewManager(), nil)
	if names := reg.All(); len(names) != 1 {
		t.Errorf("registry = %d tools, want 1", len(names))
	}
}

func TestIsMCPDerived(t *testing.T) {
	mgr := mcp.NewManager()
	nt := mcp.NamedTool{QualifiedName: "mcp__x__y", Def: mcp.ToolDef{Name: "y"}}
	tests := []struct {
		name string
		tool tool.Tool
		want bool
	}{
		{"built-in", builtinTool{name: "Bash"}, false},
		{"built-in with an MCP-looking name", builtinTool{name: "mcp__x__y"}, false},
		{"MCP tool", mcptool.New(mgr, nt), true},
		{"authenticate pseudo-tool", mcpauthtool.New(mgr, "web", "https://example.invalid/mcp"), true},
		{"LocalImplement", localimplementtool.NewDynamic(mgr, nil), true},
		{"LocalHelper", localhelpertool.NewDynamic(mgr, nil), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMCPDerived(tt.tool); got != tt.want {
				t.Errorf("isMCPDerived = %v, want %v", got, tt.want)
			}
		})
	}
}

// toolSearchOffers reports whether reg's ToolSearch offers name.
func toolSearchOffers(t *testing.T, reg *tool.Registry, name string) bool {
	t.Helper()
	ts, ok := reg.Lookup("ToolSearch")
	if !ok {
		t.Fatal("no ToolSearch")
	}
	res, err := ts.Execute(context.Background(), json.RawMessage(`{"query":"select:`+name+`"}`))
	if err != nil || len(res.Content) == 0 {
		t.Fatalf("ToolSearch = %+v, %v", res, err)
	}
	var out struct {
		Matches []struct {
			Name string `json:"name"`
		} `json:"matches"`
	}
	if err := json.Unmarshal([]byte(res.Content[0].Text), &out); err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(out.Matches, func(m struct {
		Name string `json:"name"`
	}) bool {
		return m.Name == name
	})
}

// Sub-agents and teammates run on views of the session registry. A server
// that connects after one is spawned reaches it, and its ToolSearch offers
// exactly what it can call; it used to search the session's registry and
// offer tools outside the child's allowlist.
func TestChildRegistriesFollowLateMCPConnect(t *testing.T) {
	cwd := mcptest.IsolateConfig(t)
	srv := mcptest.NewServer(t, mcp.ToolDef{Name: "search"})
	mcptest.WriteUserServers(t, map[string]mcp.ServerConfig{"docs": {Type: "http", URL: srv.URL}})
	mgr := mcp.NewManager()
	reg := tool.NewRegistry()
	reg.Register(toolsearchtool.New(reg))
	reg.Register(builtinTool{name: "Bash"})
	followMCPTools(reg, mgr, nil)

	// Spawned before the server connects.
	children := []struct {
		name         string
		reg          *tool.Registry
		wantCallable bool
	}{
		{"sub-agent with overrides", reg.WithOverrides(builtinTool{name: "EnterPlanMode"}), true},
		{"teammate allowlisting the tool", reg.Subset([]string{"ToolSearch", "Bash", "mcp__docs__search"}), true},
		{"teammate not allowlisting it", reg.Subset([]string{"ToolSearch", "Bash"}), false},
	}
	if err := mgr.ConnectAll(context.Background(), cwd, true); err != nil {
		t.Fatal(err)
	}
	for _, c := range children {
		t.Run(c.name, func(t *testing.T) {
			_, callable := c.reg.Lookup("mcp__docs__search")
			if callable != c.wantCallable {
				t.Errorf("child can call mcp__docs__search = %v, want %v", callable, c.wantCallable)
			}
			if offered := toolSearchOffers(t, c.reg, "mcp__docs__search"); offered != callable {
				t.Errorf("child's ToolSearch offers mcp__docs__search = %v, but the child can call it = %v", offered, callable)
			}
		})
	}
}
