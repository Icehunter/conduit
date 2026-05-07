package mcp

import (
	"testing"
)

func TestLoadConfigsPicksUpClaudeJSON(t *testing.T) {
	// Smoke test: LoadConfigs must not error on a valid cwd.
	_, err := LoadConfigs("/tmp", true)
	if err != nil {
		t.Fatalf("LoadConfigs: %v", err)
	}
}
