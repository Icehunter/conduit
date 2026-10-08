package main

import (
	"testing"

	"github.com/icehunter/conduit/internal/mcp"
)

// The MCP instructions are rebuilt into the system prompt on every Run. Map
// order would reshuffle them each time and bust the prompt cache, so the
// rendering has to be byte-stable for an unchanged set of servers.
func TestMCPInstructionsPrompt(t *testing.T) {
	tests := []struct {
		name    string
		servers map[string]*mcp.ConnectedServer
		want    string
	}{
		{"no servers", nil, ""},
		{
			name: "connected servers in name order",
			servers: map[string]*mcp.ConnectedServer{
				"zeta":  {Name: "zeta", Status: mcp.StatusConnected, Instructions: "z rules"},
				"alpha": {Name: "alpha", Status: mcp.StatusConnected, Instructions: "a rules"},
				"mid":   {Name: "mid", Status: mcp.StatusConnected, Instructions: "m rules"},
			},
			want: "## alpha\na rules\n\n## mid\nm rules\n\n## zeta\nz rules\n\n",
		},
		{
			name: "skips servers that are not connected or sent nothing",
			servers: map[string]*mcp.ConnectedServer{
				"up":      {Name: "up", Status: mcp.StatusConnected, Instructions: "use me"},
				"quiet":   {Name: "quiet", Status: mcp.StatusConnected},
				"pending": {Name: "pending", Status: mcp.StatusPending, Instructions: "not yet"},
			},
			want: "## up\nuse me\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := mcp.NewManagerWithServers(tt.servers)
			for range 5 { // map iteration order varies run to run
				if got := mcpInstructionsPrompt(mgr); got != tt.want {
					t.Fatalf("mcpInstructionsPrompt = %q, want %q", got, tt.want)
				}
			}
		})
	}
}
