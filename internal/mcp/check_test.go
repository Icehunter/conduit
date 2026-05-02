package mcp

import (
	"testing"
)

func TestLoadConfigsPicksUpClaudeJSON(t *testing.T) {
	// Should find qwen-router from ~/.claude.json global mcpServers
	configs, err := LoadConfigs("/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if srv, ok := configs["qwen-router"]; !ok {
		t.Error("expected qwen-router from ~/.claude.json global mcpServers")
	} else {
		t.Logf("qwen-router: command=%s args=%v", srv.Command, srv.Args)
	}
}
