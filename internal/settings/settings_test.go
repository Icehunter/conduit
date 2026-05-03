package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeSettings(t *testing.T, dir, name string, s Settings) {
	t.Helper()
	data, _ := json.Marshal(s)
	_ = os.MkdirAll(filepath.Join(dir, ".claude"), 0755)
	_ = os.WriteFile(filepath.Join(dir, ".claude", name), data, 0644)
}

// projectPaths returns only the project-level settings paths (no user home).
func projectPaths(dir string) []string {
	return []string{
		filepath.Join(dir, ".claude", "settings.json"),
		filepath.Join(dir, ".claude", "settings.local.json"),
	}
}

func TestApproveMcpjsonServer_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := ApproveMcpjsonServer("foo", "yes"); err != nil {
		t.Fatalf("approve yes: %v", err)
	}
	merged, err := loadPaths([]string{UserSettingsPath()})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !contains(merged.EnabledMcpjsonServers, "foo") {
		t.Errorf("expected 'foo' in EnabledMcpjsonServers; got %v", merged.EnabledMcpjsonServers)
	}

	// "no" moves it to disabled and removes from enabled.
	if err := ApproveMcpjsonServer("foo", "no"); err != nil {
		t.Fatalf("approve no: %v", err)
	}
	merged, _ = loadPaths([]string{UserSettingsPath()})
	if contains(merged.EnabledMcpjsonServers, "foo") {
		t.Errorf("'foo' should have been removed from enabled; got %v", merged.EnabledMcpjsonServers)
	}
	if !contains(merged.DisabledMcpjsonServers, "foo") {
		t.Errorf("expected 'foo' in DisabledMcpjsonServers; got %v", merged.DisabledMcpjsonServers)
	}
}

func TestApproveMcpjsonServer_YesAllSetsFlag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := ApproveMcpjsonServer("bar", "yes_all"); err != nil {
		t.Fatalf("approve yes_all: %v", err)
	}
	merged, _ := loadPaths([]string{UserSettingsPath()})
	if !merged.EnableAllProjectMcpServers {
		t.Errorf("EnableAllProjectMcpServers should be true after yes_all")
	}
	if !contains(merged.EnabledMcpjsonServers, "bar") {
		t.Errorf("'bar' should also be in EnabledMcpjsonServers; got %v", merged.EnabledMcpjsonServers)
	}
}

func TestApproveMcpjsonServer_PreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// Pre-populate settings with an unrelated key.
	_ = SaveRawKey("model", "claude-sonnet-4-6")

	if err := ApproveMcpjsonServer("baz", "yes"); err != nil {
		t.Fatalf("approve: %v", err)
	}

	data, _ := os.ReadFile(UserSettingsPath())
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	if _, ok := raw["model"]; !ok {
		t.Errorf("model key was clobbered by ApproveMcpjsonServer; raw=%s", data)
	}
	if _, ok := raw["enabledMcpjsonServers"]; !ok {
		t.Errorf("enabledMcpjsonServers missing; raw=%s", data)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestLoad_Empty(t *testing.T) {
	dir := t.TempDir()
	m, err := loadPaths(projectPaths(dir))
	if err != nil {
		t.Fatal(err)
	}
	if m.DefaultMode != "default" {
		t.Errorf("DefaultMode = %q", m.DefaultMode)
	}
	if len(m.Allow) != 0 || len(m.Deny) != 0 {
		t.Error("expected empty allow/deny")
	}
}

func TestLoad_ProjectSettings(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "settings.json", Settings{
		Permissions: Permissions{
			Allow: []string{"Bash(git status)"},
			Deny:  []string{"Bash(rm *)"},
		},
	})
	m, _ := loadPaths(projectPaths(dir))
	if len(m.Allow) != 1 || m.Allow[0] != "Bash(git status)" {
		t.Errorf("allow not loaded; got %v", m.Allow)
	}
	if len(m.Deny) != 1 || m.Deny[0] != "Bash(rm *)" {
		t.Errorf("deny not loaded; got %v", m.Deny)
	}
}

func TestLoad_LocalOverridesProject(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "settings.json", Settings{
		Permissions: Permissions{DefaultMode: "acceptEdits"},
	})
	writeSettings(t, dir, "settings.local.json", Settings{
		Permissions: Permissions{DefaultMode: "bypassPermissions"},
	})
	m, _ := loadPaths(projectPaths(dir))
	if m.DefaultMode != "bypassPermissions" {
		t.Errorf("local should override project; got %q", m.DefaultMode)
	}
}

func TestLoad_MergesAllowLists(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "settings.json", Settings{
		Permissions: Permissions{Allow: []string{"Bash(git log)"}},
	})
	writeSettings(t, dir, "settings.local.json", Settings{
		Permissions: Permissions{Allow: []string{"Bash(git status)"}},
	})
	m, _ := loadPaths(projectPaths(dir))
	if len(m.Allow) != 2 {
		t.Errorf("expected 2 allow entries; got %v", m.Allow)
	}
}

func TestLoad_Hooks(t *testing.T) {
	dir := t.TempDir()
	writeSettings(t, dir, "settings.json", Settings{
		Hooks: HooksSettings{
			PreToolUse: []HookMatcher{{
				Matcher: "Bash",
				Hooks:   []Hook{{Type: "command", Command: "echo hi"}},
			}},
		},
	})
	m, _ := loadPaths(projectPaths(dir))
	if len(m.Hooks.PreToolUse) != 1 {
		t.Errorf("expected 1 PreToolUse matcher; got %v", m.Hooks.PreToolUse)
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, ".claude"), 0755)
	_ = os.WriteFile(filepath.Join(dir, ".claude", "settings.json"), []byte("{bad json}"), 0644)
	m, err := loadPaths(projectPaths(dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Allow) != 0 {
		t.Error("invalid file should be skipped")
	}
}
