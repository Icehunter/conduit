package commands

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/mcp/mcptest"
	"github.com/icehunter/conduit/internal/settings"
)

// /mcp-approve runs on the TUI's event loop, behind the startup approval
// picker. It must not block on the connect (up to mcp.ConnectTimeout), and
// the decision has to show in PendingApprovals as soon as it returns: the
// TUI asks about the next pending server right away, and used to ask about a
// denied server again, and again on every startup. The decision is recorded
// under the user's home, never in the repository, whose own settings files
// don't count as approval.
func TestMCPApproveCommand(t *testing.T) {
	tests := []struct {
		name          string
		trusted       bool // workspace trust at startup
		choice        string
		wantPending   []string // PendingApprovals as soon as the command returns
		wantConnected []string // servers that end up connected
		wantDenied    []string
	}{
		{"yes connects that server", true, "yes", []string{"b"}, []string{"a"}, nil},
		{"yes_all connects every waiting server", true, "yes_all", nil, []string{"a", "b"}, nil},
		{"no denies without asking again", true, "no", []string{"b"}, nil, []string{"a"}},
		{"yes connects in a workspace not yet trusted", false, "yes", []string{"b"}, []string{"a"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd := mcptest.IsolateConfig(t)
			a := mcptest.NewServer(t, mcp.ToolDef{Name: "search"})
			b := mcptest.NewServer(t, mcp.ToolDef{Name: "fetch"})
			mcptest.WriteProjectServers(t, cwd, map[string]mcp.ServerConfig{
				"a": {Type: "http", URL: a.URL},
				"b": {Type: "http", URL: b.URL},
			})
			mgr := mcp.NewManager()
			if err := mgr.ConnectAll(context.Background(), cwd, tt.trusted); err != nil {
				t.Fatal(err)
			}
			if got := mgr.PendingApprovals(); !slices.Equal(got, []string{"a", "b"}) {
				t.Fatalf("setup: PendingApprovals = %v", got)
			}
			changed := make(chan struct{}, 64)
			mgr.SetOnChange(func() {
				select {
				case changed <- struct{}{}:
				default:
				}
			})

			// Hold every handshake: a command that waited on the connect
			// would not return until released.
			releaseA, releaseB := a.HoldInitialize(t), b.HoldInitialize(t)
			r := New()
			RegisterMCPApproveCommand(r, mgr, cwd)
			type dispatched struct {
				res Result
				ok  bool
			}
			done := make(chan dispatched, 1)
			go func() {
				res, ok := r.Dispatch("/mcp-approve a " + tt.choice)
				done <- dispatched{res, ok}
			}()
			var got dispatched
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("/mcp-approve blocked on the server's handshake")
			}
			if !got.ok || got.res.Type != "flash" || !strings.Contains(got.res.Text, "a") {
				t.Fatalf("Dispatch = %+v, %v", got.res, got.ok)
			}
			if got := mgr.PendingApprovals(); !slices.Equal(got, tt.wantPending) {
				t.Errorf("PendingApprovals right after = %v, want %v", got, tt.wantPending)
			}

			releaseA()
			releaseB()
			connected := func() bool {
				for _, name := range tt.wantConnected {
					if srv, ok := mgr.Server(name); !ok || srv.Status != mcp.StatusConnected {
						return false
					}
				}
				return true
			}
			for !connected() {
				select {
				case <-changed:
				case <-time.After(10 * time.Second):
					t.Fatalf("approved servers %v never connected", tt.wantConnected)
				}
			}
			for _, name := range tt.wantDenied {
				srv, ok := mgr.Server(name)
				if !ok || srv.Status != mcp.StatusDisconnected || !strings.Contains(srv.Error, "denied") {
					t.Errorf("%s = %+v, want disconnected as denied", name, srv)
				}
			}

			// Recorded in conduit's per-project state under the user's home,
			// and nowhere in the repository.
			state, ok, err := settings.LoadConduitProjectState(cwd)
			if err != nil || !ok {
				t.Fatalf("project state = %+v, %v, %v", state, ok, err)
			}
			list := state.EnabledMcpjsonServers
			if tt.choice == "no" {
				list = state.DisabledMcpjsonServers
			}
			if !slices.Contains(list, "a") {
				t.Errorf("decision %q not recorded in project state %+v", tt.choice, state)
			}
			for _, dir := range []string{".claude", ".conduit"} {
				if _, err := os.Stat(filepath.Join(cwd, dir)); !os.IsNotExist(err) {
					t.Errorf("approval wrote into the repository's %s (stat err %v)", dir, err)
				}
			}

			// The decision persists: the next startup asks only about what
			// is still undecided.
			next := mcp.NewManager()
			if err := next.ConnectAll(context.Background(), cwd, true); err != nil {
				t.Fatal(err)
			}
			if got := next.PendingApprovals(); !slices.Equal(got, tt.wantPending) {
				t.Errorf("next startup PendingApprovals = %v, want %v", got, tt.wantPending)
			}
		})
	}
}
