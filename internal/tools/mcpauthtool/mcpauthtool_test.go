package mcpauthtool

import (
	"context"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/mcp/mcptest"
	"github.com/icehunter/conduit/internal/tool"
)

func resultText(r tool.Result) string {
	var sb strings.Builder
	for _, b := range r.Content {
		sb.WriteString(b.Text)
	}
	return sb.String()
}

// After OAuth the tool reconnects the server. It used to pass an empty cwd,
// which leaves project-scoped servers (.mcp.json) out of the config load: the
// server was deleted and reported "not found" right after a successful
// authorization, and the result claimed its tools were available regardless.
func TestReconnectAfterAuth(t *testing.T) {
	tests := []struct {
		name         string
		stillDenied  bool // server keeps answering 401 after "OAuth"
		dropConfig   bool // server removed from .mcp.json before the reconnect
		wantStatus   mcp.ServerStatus
		wantInResult []string
	}{
		{
			name:         "connects with the session's project config",
			wantStatus:   mcp.StatusConnected,
			wantInResult: []string{"is connected", "2 tools", "mcp__jira__", "ToolSearch"},
		},
		{
			name:         "still rejected is reported, not claimed as available",
			stillDenied:  true,
			wantStatus:   mcp.StatusNeedsAuth,
			wantInResult: []string{"needs-auth", "retry"},
		},
		{
			name:         "missing from config is reported",
			dropConfig:   true,
			wantStatus:   "",
			wantInResult: []string{"reconnecting failed", "not found"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd := mcptest.IsolateConfig(t)
			srv := mcptest.NewServer(t, mcp.ToolDef{Name: "search"}, mcp.ToolDef{Name: "create"})
			srv.SetUnauthorized(true)
			mcptest.WriteProjectServers(t, cwd, map[string]mcp.ServerConfig{"jira": {Type: "http", URL: srv.URL}})
			mcptest.ApproveProjectServers(t, cwd, "jira")

			mgr := mcp.NewManager()
			if err := mgr.ConnectAll(context.Background(), cwd, true); err != nil {
				t.Fatal(err)
			}
			if got, _ := mgr.Server("jira"); got == nil || got.Status != mcp.StatusNeedsAuth {
				t.Fatalf("setup: jira = %+v, want needs-auth", got)
			}

			srv.SetUnauthorized(tt.stillDenied)
			if tt.dropConfig {
				mcptest.WriteProjectServers(t, cwd, map[string]mcp.ServerConfig{})
			}
			at := New(mgr, "jira", srv.URL).(*authTool)
			text := resultText(at.reconnect())

			got, ok := mgr.Server("jira")
			switch {
			case tt.wantStatus == "" && ok:
				t.Errorf("jira still listed as %q", got.Status)
			case tt.wantStatus != "" && (!ok || got.Status != tt.wantStatus):
				t.Errorf("jira = %+v (listed %v), want %q", got, ok, tt.wantStatus)
			}
			for _, want := range tt.wantInResult {
				if !strings.Contains(text, want) {
					t.Errorf("result %q does not mention %q", text, want)
				}
			}
		})
	}
}

func TestIsAuthTool(t *testing.T) {
	mgr := mcp.NewManager()
	if !IsAuthTool(New(mgr, "web", "https://example.invalid")) {
		t.Error("IsAuthTool(New(...)) = false")
	}
	if IsAuthTool(nil) {
		t.Error("IsAuthTool(nil) = true")
	}
}

func TestAuthToolName(t *testing.T) {
	if got := New(mcp.NewManager(), "my-server", "https://x").Name(); got != "mcp__my_server__authenticate" {
		t.Errorf("Name = %q", got)
	}
}
