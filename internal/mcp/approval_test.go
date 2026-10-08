package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/settings"
)

// A project server comes from a .mcp.json committed to the repository, so the
// decision to run it has to come from somewhere the repository can't write.
// Settings files inside the checkout (.claude/settings*.json,
// .conduit/settings*.json) used to count, so a cloned repo could approve its
// own server and have its command started on first launch, before any trust
// or approval dialog.
func TestProjectServerApprovalSources(t *testing.T) {
	everyRepoSettingsFile := func(cwd string, v map[string]any) {
		for _, rel := range []string{
			".claude/settings.json", ".claude/settings.local.json",
			".conduit/settings.json", ".conduit/settings.local.json",
		} {
			writeJSON(t, filepath.Join(cwd, rel), v)
		}
	}
	tests := []struct {
		name       string
		trusted    bool
		setup      func(t *testing.T, cwd string)
		wantStatus ServerStatus
		wantDialed int
	}{
		{
			name:    "repo approving its own servers is ignored",
			trusted: true,
			setup: func(t *testing.T, cwd string) {
				everyRepoSettingsFile(cwd, map[string]any{"enableAllProjectMcpServers": true, "enabledMcpjsonServers": []string{"proj"}})
			},
			wantStatus: StatusNeedsApproval,
		},
		{
			name:    "repo denying its own server is ignored",
			trusted: true,
			setup: func(t *testing.T, cwd string) {
				everyRepoSettingsFile(cwd, map[string]any{"disabledMcpjsonServers": []string{"proj"}})
			},
			wantStatus: StatusNeedsApproval,
		},
		{
			name:    "approval recorded for this project connects",
			trusted: true,
			setup: func(t *testing.T, cwd string) {
				if err := settings.ApproveMcpjsonServer("proj", "yes", cwd); err != nil {
					t.Fatal(err)
				}
			},
			wantStatus: StatusConnected,
			wantDialed: 1,
		},
		{
			name:    "user-level enableAllProjectMcpServers connects",
			trusted: true,
			setup: func(t *testing.T, _ string) {
				writeJSON(t, filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "settings.json"), map[string]any{"enableAllProjectMcpServers": true})
			},
			wantStatus: StatusConnected,
			wantDialed: 1,
		},
		{
			name:    "user-level denial beats a project approval",
			trusted: true,
			setup: func(t *testing.T, cwd string) {
				// No cwd: recorded in the user's own conduit.json.
				if err := settings.ApproveMcpjsonServer("proj", "no"); err != nil {
					t.Fatal(err)
				}
				if err := settings.ApproveMcpjsonServer("proj", "yes", cwd); err != nil {
					t.Fatal(err)
				}
			},
			wantStatus: StatusDisconnected,
		},
		{
			name:    "untrusted workspace never dials, even an approved server",
			trusted: false,
			setup: func(t *testing.T, cwd string) {
				if err := settings.ApproveMcpjsonServer("proj", "yes_all", cwd); err != nil {
					t.Fatal(err)
				}
			},
			wantStatus: StatusNeedsApproval,
		},
		{
			name:    "untrusted workspace keeps a denied server denied",
			trusted: false,
			setup: func(t *testing.T, cwd string) {
				if err := settings.ApproveMcpjsonServer("proj", "no", cwd); err != nil {
					t.Fatal(err)
				}
			},
			wantStatus: StatusDisconnected,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd := isolateConfig(t)
			writeProjectServers(t, cwd, map[string]ServerConfig{"proj": {Command: "proj"}})
			tt.setup(t, cwd)

			d := newFakeDialer(false)
			m := NewManager()
			m.dial = d.dial
			if err := m.ConnectAll(context.Background(), cwd, tt.trusted); err != nil {
				t.Fatal(err)
			}
			if got := statusOf(m, "proj"); got != tt.wantStatus {
				srv, _ := m.Server("proj")
				t.Errorf("proj = %q (%+v), want %q", got, srv, tt.wantStatus)
			}
			if n := len(d.created()); n != tt.wantDialed {
				t.Errorf("dialed %d times, want %d", n, tt.wantDialed)
			}
			if tt.wantStatus == StatusDisconnected {
				if srv, _ := m.Server("proj"); srv == nil || !strings.Contains(srv.Error, "denied") {
					t.Errorf("denied server should say so; got %+v", srv)
				}
			}
		})
	}
}
