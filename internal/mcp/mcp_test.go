package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/settings"
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

func TestToolNamePrefixMatchesClaudeMCPConvention(t *testing.T) {
	got := ToolNamePrefix("qwen-router")
	if got != "mcp__qwen_router__" {
		t.Fatalf("ToolNamePrefix = %q, want mcp__qwen_router__", got)
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

func TestLoadConfigsPicksUpTopLevelClaudeMcpServers(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	path := filepath.Join(dir, ".claude.json")
	if err := os.WriteFile(path, []byte(`{
  "mcpServers": {
    "qwen-router": {
      "command": "node",
      "args": ["/tmp/server.js"],
      "env": {"QWEN_MODEL": "qwen3-coder"}
    }
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := LoadConfigs("/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	cfg, ok := cfgs["qwen-router"]
	if !ok {
		t.Fatal("qwen-router server was not loaded from top-level mcpServers")
	}
	if cfg.Scope != "user" {
		t.Fatalf("scope = %q, want user", cfg.Scope)
	}
	if cfg.Command != "node" || len(cfg.Args) != 1 || cfg.Env["QWEN_MODEL"] != "qwen3-coder" {
		t.Fatalf("server config not preserved: %+v", cfg)
	}
}

func TestLoadConfigsPicksUpConduitMCPOverlay(t *testing.T) {
	claudeDir := t.TempDir()
	conduitDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)

	claudePath := filepath.Join(claudeDir, ".claude.json")
	if err := os.WriteFile(claudePath, []byte(`{
  "mcpServers": {
    "local-router": {
      "command": "node",
      "args": ["/tmp/claude-server.js"],
      "env": {"LOCAL_LLM_MODEL": "old"}
    }
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	conduitPath := filepath.Join(conduitDir, "mcp.json")
	if err := os.WriteFile(conduitPath, []byte(`{
  "mcpServers": {
    "local-router": {
      "command": "node",
      "args": ["/tmp/conduit-server.js"],
      "env": {"LOCAL_LLM_MODEL": "qwen3-coder"}
    },
    "extra-router": {
      "command": "python",
      "args": ["server.py"]
    }
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := LoadConfigs("/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	local := cfgs["local-router"]
	if local.Scope != "conduit" || local.Source != conduitPath {
		t.Fatalf("local-router source/scope = %q/%q, want conduit overlay", local.Source, local.Scope)
	}
	if len(local.Args) != 1 || local.Args[0] != "/tmp/conduit-server.js" || local.Env["LOCAL_LLM_MODEL"] != "qwen3-coder" {
		t.Fatalf("local-router was not overridden by conduit config: %+v", local)
	}
	if extra := cfgs["extra-router"]; extra.Scope != "conduit" || extra.Command != "python" {
		t.Fatalf("extra-router = %+v, want conduit python server", extra)
	}
}

func TestLoadConfigsLoadsPluginMCPWhenEnabledSettingIsMissing(t *testing.T) {
	claudeDir := t.TempDir()
	conduitDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)

	pluginDir := filepath.Join(claudeDir, "plugins", "cache", "context7")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, ".mcp.json"), []byte(`{
  "mcpServers": {
    "context7": {
      "command": "node",
      "args": ["server.js"]
    }
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	installedPath := filepath.Join(claudeDir, "plugins", "installed_plugins.json")
	if err := os.WriteFile(installedPath, []byte(fmt.Sprintf(`{
  "version": 2,
  "plugins": {
    "context7@claude-plugins-official": [
      {"scope": "user", "installPath": %q, "version": "1.0.0"}
    ]
  }
}`, pluginDir)), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := LoadConfigs("/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	cfg, ok := cfgs["plugin:context7:context7"]
	if !ok {
		t.Fatal("plugin MCP server should load when enabledPlugins entry is missing")
	}
	if cfg.Scope != "plugin" || cfg.PluginName != "context7" {
		t.Fatalf("plugin server metadata = scope %q plugin %q", cfg.Scope, cfg.PluginName)
	}
}

func TestLoadConfigsSkipsPluginMCPWhenExplicitlyDisabled(t *testing.T) {
	claudeDir := t.TempDir()
	conduitDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)

	pluginDir := filepath.Join(claudeDir, "plugins", "cache", "context7")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, ".mcp.json"), []byte(`{
  "mcpServers": {
    "context7": {
      "command": "node",
      "args": ["server.js"]
    }
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	installedPath := filepath.Join(claudeDir, "plugins", "installed_plugins.json")
	if err := os.WriteFile(installedPath, []byte(fmt.Sprintf(`{
  "version": 2,
  "plugins": {
    "context7@claude-plugins-official": [
      {"scope": "user", "installPath": %q, "version": "1.0.0"}
    ]
  }
}`, pluginDir)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(conduitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conduitDir, "conduit.json"), []byte(`{
  "schemaVersion": 1,
  "enabledPlugins": {
    "context7@claude-plugins-official": false
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfgs, err := LoadConfigs("/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfgs["plugin:context7:context7"]; ok {
		t.Fatal("plugin MCP server should not load when explicitly disabled")
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

type fakeMCPClient struct {
	called string
}

func (f *fakeMCPClient) Initialize(context.Context) (string, error) { return "", nil }
func (f *fakeMCPClient) ListTools(context.Context) ([]ToolDef, error) {
	return []ToolDef{{Name: "qwen_implement", Description: "implement with qwen"}}, nil
}
func (f *fakeMCPClient) CallTool(_ context.Context, name string, _ json.RawMessage) (CallResult, error) {
	f.called = name
	return CallResult{Content: []ContentBlock{{Type: "text", Text: "ok"}}}, nil
}
func (f *fakeMCPClient) ListResources(context.Context) ([]ResourceDef, error) { return nil, nil }
func (f *fakeMCPClient) ReadResource(context.Context, string) ([]ResourceContent, error) {
	return nil, nil
}
func (f *fakeMCPClient) Close() error { return nil }

func TestManagerAllToolsUsesMCPQualifiedNames(t *testing.T) {
	client := &fakeMCPClient{}
	m := NewManager()
	m.servers["qwen-router"] = &ConnectedServer{
		Name:   "qwen-router",
		Status: StatusConnected,
		Tools:  []ToolDef{{Name: "qwen_implement", Description: "implement with qwen"}},
		client: client,
	}

	tools := m.AllTools()
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	if tools[0].QualifiedName != "mcp__qwen_router__qwen_implement" {
		t.Fatalf("QualifiedName = %q", tools[0].QualifiedName)
	}
	if !strings.HasPrefix(tools[0].Prefix, "mcp__qwen_router__") {
		t.Fatalf("Prefix = %q", tools[0].Prefix)
	}

	result, err := m.CallTool(context.Background(), "mcp__qwen_router__qwen_implement", nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.called != "qwen_implement" {
		t.Fatalf("called MCP tool %q, want qwen_implement", client.called)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "ok" {
		t.Fatalf("result = %+v", result)
	}
}

func TestMergedStdioEnvKeepsParentEnvironment(t *testing.T) {
	t.Setenv("CONDUIT_MCP_PARENT_ENV_TEST", "parent")
	env := mergedStdioEnv(map[string]string{"QWEN_MODEL": "qwen3-coder"})
	joined := "\x00" + strings.Join(env, "\x00") + "\x00"
	if !strings.Contains(joined, "\x00CONDUIT_MCP_PARENT_ENV_TEST=parent\x00") {
		t.Fatal("merged env dropped parent environment")
	}
	if !strings.Contains(joined, "\x00QWEN_MODEL=qwen3-coder\x00") {
		t.Fatal("merged env did not include configured MCP env")
	}
}

func TestSetDisabled_PreservesConduitProjectFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", dir)
	cwd := filepath.Join(dir, "project")
	initial := `{
  "mcpServers": {"global": {"command": "node"}},
  "projects": {
    "` + filepath.ToSlash(cwd) + `": {
      "mcpServers": {"local": {"command": "python"}},
      "enabledMcpServers": ["keep-enabled"],
      "customProjectField": {"keep": true}
    }
  },
  "customTopLevel": ["keep"]
}`
	path := settings.ConduitSettingsPath()
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SetDisabled("local", cwd, true); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["mcpServers"]; !ok {
		t.Fatal("global mcpServers was removed")
	}
	if _, ok := raw["customTopLevel"]; !ok {
		t.Fatal("custom top-level field was removed")
	}
	var projects map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw["projects"], &projects); err != nil {
		t.Fatal(err)
	}
	project := projects[cwd]
	for _, key := range []string{"mcpServers", "enabledMcpServers", "customProjectField", "disabledMcpServers"} {
		if _, ok := project[key]; !ok {
			t.Fatalf("project field %q was not preserved/set", key)
		}
	}
}

func TestIsDisabled_ConduitProjectOverridesClaudeFallback(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	conduitDir := filepath.Join(dir, ".conduit")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)
	cwd := filepath.Join(dir, "project")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"projects":{"` + filepath.ToSlash(cwd) + `":{"disabledMcpServers":["srv"]}}}`
	if err := os.WriteFile(filepath.Join(claudeDir, ".claude.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if !IsDisabled("srv", cwd) {
		t.Fatal("expected Claude fallback to mark srv disabled")
	}
	if err := SetDisabled("srv", cwd, false); err != nil {
		t.Fatal(err)
	}
	state, ok, err := settings.LoadConduitProjectState(cwd)
	if err != nil || !ok || !state.DisabledMcpServersPresent {
		t.Fatalf("state = %+v ok=%v err=%v", state, ok, err)
	}
	if IsDisabled("srv", cwd) {
		t.Fatal("expected Conduit empty disabledMcpServers to override Claude fallback")
	}
}
