package mcp

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNormalizeServerName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"my-server", "my_server__"},
		{"github", "github__"},
		{"my.server!", "my_server___"},
	}
	for _, tt := range tests {
		got := NormalizeServerName(tt.in)
		if got != tt.want {
			t.Errorf("NormalizeServerName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLoadConfigsNoError(t *testing.T) {
	// LoadConfigs must never return an error regardless of whether config files exist.
	// Global ~/.claude.json is always read if present, so we only assert no error.
	_, err := LoadConfigs("/tmp/definitely-nonexistent-8675309")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMcpJSONParse(t *testing.T) {
	raw := `{"mcpServers":{"test":{"command":"echo","args":["hello"]}}}`
	var cfg McpJSON
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	srv, ok := cfg.McpServers["test"]
	if !ok {
		t.Fatal("expected 'test' server")
	}
	if srv.Command != "echo" {
		t.Errorf("command = %q, want %q", srv.Command, "echo")
	}
	if len(srv.Args) != 1 || srv.Args[0] != "hello" {
		t.Errorf("args = %v, want [hello]", srv.Args)
	}
}

func TestClaudeJSONParse(t *testing.T) {
	raw := `{
		"mcpServers": {"global-srv": {"type": "stdio", "command": "go", "args": ["run", "."]}},
		"projects": {
			"/my/project": {"mcpServers": {"proj-srv": {"type": "sse", "url": "http://localhost:3000"}}}
		}
	}`
	var cfg claudeJSON
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.McpServers["global-srv"]; !ok {
		t.Error("expected global-srv")
	}
	proj := cfg.Projects["/my/project"]
	if _, ok := proj.McpServers["proj-srv"]; !ok {
		t.Error("expected proj-srv")
	}
}

func TestManagerConnectAllNoError(t *testing.T) {
	// Manager must not error even when servers fail to connect.
	m := NewManager()
	err := m.ConnectAll(context.Background(), "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Servers may exist (from ~/.claude.json) or not — both are valid.
	t.Logf("servers found: %d", len(m.Servers()))
}
