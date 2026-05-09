package mcp

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// setupTestEnv isolates the test from the user's real config files by
// pointing CLAUDE_CONFIG_DIR and CONDUIT_CONFIG_DIR at fresh temp dirs.
// Returns (conduitDir, projectCwd).
func setupTestEnv(t *testing.T) (string, string) {
	t.Helper()
	claudeDir := t.TempDir()
	conduitDir := t.TempDir()
	cwd := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)
	return conduitDir, cwd
}

func TestAddServer_ProjectScopeRoundTrip(t *testing.T) {
	_, cwd := setupTestEnv(t)

	cfg := ServerConfig{Type: "http", URL: "https://mcp.atlassian.com/v1/mcp"}
	if err := AddServer("atlassian", cfg, ScopeProject, cwd); err != nil {
		t.Fatalf("AddServer: %v", err)
	}

	// File should exist at <cwd>/.mcp.json
	data, err := os.ReadFile(filepath.Join(cwd, ".mcp.json"))
	if err != nil {
		t.Fatalf("reading .mcp.json: %v", err)
	}
	var got McpJSON
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid JSON written: %v\n%s", err, data)
	}
	srv, ok := got.McpServers["atlassian"]
	if !ok {
		t.Fatalf("atlassian server missing from %s", data)
	}
	if srv.Type != "http" || srv.URL != "https://mcp.atlassian.com/v1/mcp" {
		t.Fatalf("server config not round-tripped: %+v", srv)
	}

	// LoadConfigs should also pick it up at the right scope.
	merged, err := LoadConfigs(cwd, true)
	if err != nil {
		t.Fatalf("LoadConfigs: %v", err)
	}
	loaded, ok := merged["atlassian"]
	if !ok {
		t.Fatalf("LoadConfigs did not surface the new server")
	}
	if loaded.Scope != "project" {
		t.Errorf("scope = %q, want project", loaded.Scope)
	}
}

func TestAddServer_UserScopeWritesConduitMcpJSON(t *testing.T) {
	conduitDir, cwd := setupTestEnv(t)

	cfg := ServerConfig{Command: "node", Args: []string{"/tmp/x.js"}}
	if err := AddServer("local-thing", cfg, ScopeUser, cwd); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	if _, err := os.Stat(filepath.Join(conduitDir, "mcp.json")); err != nil {
		t.Fatalf("user-scope write should land in conduit mcp.json: %v", err)
	}

	merged, _ := LoadConfigs(cwd, true)
	if loaded, ok := merged["local-thing"]; !ok {
		t.Fatal("user-scope server not loaded")
	} else if loaded.Scope != "conduit" {
		t.Errorf("scope = %q, want conduit (user-scope label)", loaded.Scope)
	}
}

func TestAddServer_LocalScopeWritesConduitJSONProjects(t *testing.T) {
	conduitDir, cwd := setupTestEnv(t)

	cfg := ServerConfig{Type: "http", URL: "https://example.com/mcp"}
	if err := AddServer("priv", cfg, ScopeLocal, cwd); err != nil {
		t.Fatalf("AddServer: %v", err)
	}

	// conduit.json should now contain projects[abs(cwd)].mcpServers.priv
	data, err := os.ReadFile(filepath.Join(conduitDir, "conduit.json"))
	if err != nil {
		t.Fatalf("conduit.json missing: %v", err)
	}
	if !contains(string(data), `"priv"`) || !contains(string(data), "https://example.com/mcp") {
		t.Fatalf("conduit.json does not contain the new server:\n%s", data)
	}

	merged, err := LoadConfigs(cwd, true)
	if err != nil {
		t.Fatalf("LoadConfigs: %v", err)
	}
	loaded, ok := merged["priv"]
	if !ok {
		t.Fatalf("local-scope server not surfaced by LoadConfigs")
	}
	if loaded.Scope != "local" {
		t.Errorf("scope = %q, want local", loaded.Scope)
	}
}

func TestAddServer_RejectsDuplicateInSameScope(t *testing.T) {
	_, cwd := setupTestEnv(t)
	cfg := ServerConfig{Type: "http", URL: "https://x.example/mcp"}
	if err := AddServer("dup", cfg, ScopeProject, cwd); err != nil {
		t.Fatalf("first AddServer: %v", err)
	}
	err := AddServer("dup", cfg, ScopeProject, cwd)
	if !errors.Is(err, ErrServerExists) {
		t.Fatalf("err = %v, want ErrServerExists", err)
	}
}

func TestAddServer_PreservesSiblingTopLevelKeys(t *testing.T) {
	_, cwd := setupTestEnv(t)
	path := filepath.Join(cwd, ".mcp.json")
	// Pre-seed file with an unrelated top-level key the user might have added.
	if err := os.WriteFile(path, []byte(`{
  "_comment": "managed by hand",
  "mcpServers": {
    "existing": {"type": "http", "url": "https://existing.example/mcp"}
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := AddServer("newone", ServerConfig{Type: "http", URL: "https://new.example/mcp"}, ScopeProject, cwd); err != nil {
		t.Fatalf("AddServer: %v", err)
	}
	out, _ := os.ReadFile(path)
	if !contains(string(out), `"_comment"`) {
		t.Errorf("sibling top-level key was not preserved:\n%s", out)
	}
	var parsed McpJSON
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output JSON invalid: %v", err)
	}
	if _, ok := parsed.McpServers["existing"]; !ok {
		t.Errorf("existing server lost during write")
	}
	if _, ok := parsed.McpServers["newone"]; !ok {
		t.Errorf("new server not added")
	}
}

func TestRemoveServer_ProjectScope(t *testing.T) {
	_, cwd := setupTestEnv(t)
	if err := AddServer("byebye", ServerConfig{Type: "http", URL: "https://x.example/mcp"}, ScopeProject, cwd); err != nil {
		t.Fatal(err)
	}
	from, err := RemoveServer("byebye", ScopeProject, cwd)
	if err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	if from != ScopeProject {
		t.Errorf("removed from %q, want project", from)
	}
	merged, _ := LoadConfigs(cwd, true)
	if _, ok := merged["byebye"]; ok {
		t.Errorf("server still loaded after removal")
	}
}

func TestRemoveServer_AnyScopeFindsAcrossFiles(t *testing.T) {
	_, cwd := setupTestEnv(t)
	if err := AddServer("multi", ServerConfig{Type: "http", URL: "https://x.example/mcp"}, ScopeUser, cwd); err != nil {
		t.Fatal(err)
	}
	from, err := RemoveServer("multi", "", cwd) // empty scope = any
	if err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	if from != ScopeUser {
		t.Errorf("removed from %q, want user", from)
	}
}

func TestRemoveServer_NotFoundReturnsSentinel(t *testing.T) {
	_, cwd := setupTestEnv(t)
	_, err := RemoveServer("ghost", ScopeProject, cwd)
	if !errors.Is(err, ErrServerNotFound) {
		t.Fatalf("err = %v, want ErrServerNotFound", err)
	}
}

func TestListConfiguredServers_AllScopes(t *testing.T) {
	_, cwd := setupTestEnv(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(AddServer("p", ServerConfig{Type: "http", URL: "https://p.example/mcp"}, ScopeProject, cwd))
	must(AddServer("u", ServerConfig{Type: "http", URL: "https://u.example/mcp"}, ScopeUser, cwd))
	must(AddServer("l", ServerConfig{Type: "http", URL: "https://l.example/mcp"}, ScopeLocal, cwd))

	rows, err := ListConfiguredServers(cwd)
	if err != nil {
		t.Fatalf("ListConfiguredServers: %v", err)
	}
	scopeByName := map[string]string{}
	for _, r := range rows {
		scopeByName[r.Name] = r.Scope
	}
	for name, want := range map[string]string{"p": ScopeProject, "u": ScopeUser, "l": ScopeLocal} {
		if got := scopeByName[name]; got != want {
			t.Errorf("server %q scope = %q, want %q", name, got, want)
		}
	}
}

func TestUserScopeOverridesProjectInLoadConfigs(t *testing.T) {
	_, cwd := setupTestEnv(t)
	// Same name, different URL — user scope should win because it's loaded last.
	if err := AddServer("ovr", ServerConfig{Type: "http", URL: "https://project.example/mcp"}, ScopeProject, cwd); err != nil {
		t.Fatal(err)
	}
	if err := AddServer("ovr", ServerConfig{Type: "http", URL: "https://user.example/mcp"}, ScopeUser, cwd); err != nil {
		t.Fatal(err)
	}
	merged, _ := LoadConfigs(cwd, true)
	got := merged["ovr"]
	if got.URL != "https://user.example/mcp" {
		t.Errorf("URL = %q, want user-scope value", got.URL)
	}
	if got.Scope != "conduit" {
		t.Errorf("scope = %q, want conduit (user-scope label)", got.Scope)
	}
}

func TestDetectTransport(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"https://mcp.example.com", "http"},
		{"http://localhost:9000", "http"},
		{"npx", "stdio"},
		{"/usr/local/bin/server", "stdio"},
		{"", "stdio"},
	}
	for _, tt := range tests {
		if got := DetectTransport(tt.in); got != tt.want {
			t.Errorf("DetectTransport(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeTransport(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"http", "http"},
		{"streamable-http", "http"},
		{"HTTP", "http"},
		{"sse", "sse"},
		{"stdio", "stdio"},
		{"", "stdio"},
	}
	for _, tt := range tests {
		if got := NormalizeTransport(tt.in); got != tt.want {
			t.Errorf("NormalizeTransport(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
