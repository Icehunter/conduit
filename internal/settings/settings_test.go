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

func TestSavePermissionsField_PreservesSiblings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// Pre-populate with a permissions block that has allow/deny and a
	// sibling top-level key.
	seed := map[string]any{
		"model": "claude-sonnet-4-6",
		"permissions": map[string]any{
			"allow": []string{"Bash(git status)"},
			"deny":  []string{"Bash(rm -rf /)"},
		},
	}
	seedData, _ := json.Marshal(seed)
	_ = os.MkdirAll(filepath.Dir(UserSettingsPath()), 0o755)
	if err := os.WriteFile(UserSettingsPath(), seedData, 0o644); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	if err := SavePermissionsField("defaultMode", "plan"); err != nil {
		t.Fatalf("SavePermissionsField: %v", err)
	}

	data, err := os.ReadFile(UserSettingsPath())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var got struct {
		Model       string `json:"model"`
		Permissions struct {
			Allow       []string `json:"allow"`
			Deny        []string `json:"deny"`
			DefaultMode string   `json:"defaultMode"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%s", err, data)
	}
	if got.Permissions.DefaultMode != "plan" {
		t.Errorf("defaultMode = %q, want plan", got.Permissions.DefaultMode)
	}
	if len(got.Permissions.Allow) != 1 || got.Permissions.Allow[0] != "Bash(git status)" {
		t.Errorf("allow clobbered: %v", got.Permissions.Allow)
	}
	if len(got.Permissions.Deny) != 1 || got.Permissions.Deny[0] != "Bash(rm -rf /)" {
		t.Errorf("deny clobbered: %v", got.Permissions.Deny)
	}
	if got.Model != "claude-sonnet-4-6" {
		t.Errorf("model clobbered: %q", got.Model)
	}
}

func TestSavePermissionsField_CreatesPermissionsObject(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := SavePermissionsField("defaultMode", "acceptEdits"); err != nil {
		t.Fatalf("SavePermissionsField: %v", err)
	}

	data, _ := os.ReadFile(UserSettingsPath())
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	perms, ok := raw["permissions"]
	if !ok {
		t.Fatalf("permissions key missing; raw=%s", data)
	}
	var p map[string]any
	_ = json.Unmarshal(perms, &p)
	if p["defaultMode"] != "acceptEdits" {
		t.Errorf("defaultMode = %v", p["defaultMode"])
	}
}

func TestSavePermissionsField_NilDeletes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := SavePermissionsField("defaultMode", "plan"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := SavePermissionsField("defaultMode", nil); err != nil {
		t.Fatalf("delete: %v", err)
	}

	data, _ := os.ReadFile(UserSettingsPath())
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	// Empty permissions object should be removed entirely.
	if _, ok := raw["permissions"]; ok {
		t.Errorf("permissions key should be removed when empty; raw=%s", data)
	}
}

func TestSavePermissionsField_EmptyFieldErrors(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := SavePermissionsField("", "plan"); err == nil {
		t.Error("expected error for empty field")
	}
}

func TestSettingsWrites_DoNotOverwriteInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	path := UserSettingsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	before := []byte(`{"model":`)
	if err := os.WriteFile(path, before, 0o644); err != nil {
		t.Fatal(err)
	}

	writes := []struct {
		name string
		fn   func() error
	}{
		{"SaveRawKey", func() error { return SaveRawKey("model", "new") }},
		{"SavePermissionsField", func() error { return SavePermissionsField("defaultMode", "plan") }},
		{"ApproveMcpjsonServer", func() error { return ApproveMcpjsonServer("srv", "yes") }},
		{"SaveOutputStyle", func() error { return SaveOutputStyle("default") }},
	}
	for _, tt := range writes {
		if err := os.WriteFile(path, before, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := tt.fn(); err == nil {
			t.Fatalf("%s should fail on invalid existing JSON", tt.name)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatalf("%s overwrote invalid JSON: %q", tt.name, after)
		}
	}
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
