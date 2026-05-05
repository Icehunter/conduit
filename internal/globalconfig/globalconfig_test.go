package globalconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_Empty(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Projects == nil {
		t.Error("Projects map should be initialized")
	}
}

func TestSetGetTrusted(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_SANDBOXED", "") // ensure not bypassed

	cwd := t.TempDir()
	trusted, err := IsTrusted(cwd)
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if trusted {
		t.Fatal("new dir should not be trusted")
	}

	if err := SetTrusted(cwd); err != nil {
		t.Fatalf("SetTrusted: %v", err)
	}

	trusted, err = IsTrusted(cwd)
	if err != nil {
		t.Fatalf("IsTrusted after set: %v", err)
	}
	if !trusted {
		t.Error("dir should be trusted after SetTrusted")
	}
}

func TestIsTrusted_AncestorWalk(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_SANDBOXED", "")

	parent := t.TempDir()
	child := filepath.Join(parent, "sub", "project")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	// Untrusted by default.
	trusted, err := IsTrusted(child)
	if err != nil {
		t.Fatal(err)
	}
	if trusted {
		t.Fatal("child should not be trusted before parent is trusted")
	}

	// Trust parent → child should inherit.
	if err := SetTrusted(parent); err != nil {
		t.Fatal(err)
	}
	trusted, err = IsTrusted(child)
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if !trusted {
		t.Error("child should inherit trust from parent")
	}

	// Sibling that is NOT a child of parent should not be trusted.
	sibling := t.TempDir()
	trusted, err = IsTrusted(sibling)
	if err != nil {
		t.Fatal(err)
	}
	if trusted {
		t.Error("sibling dir should not inherit parent trust")
	}
}

func TestIsTrusted_SandboxedEnvBypass(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_SANDBOXED", "1")

	cwd := t.TempDir()
	trusted, err := IsTrusted(cwd)
	if err != nil {
		t.Fatalf("IsTrusted: %v", err)
	}
	if !trusted {
		t.Error("CLAUDE_CODE_SANDBOXED should bypass trust requirement")
	}
}

func TestRoundTrip_Persistence(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CODE_SANDBOXED", "")

	cwd := t.TempDir()
	if err := SetTrusted(cwd); err != nil {
		t.Fatal(err)
	}

	// Second IsTrusted call re-reads the file.
	trusted, err := IsTrusted(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if !trusted {
		t.Error("trust should persist across Load calls")
	}
}

func TestLoad_CorruptFile_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	// Write garbage to the config file.
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte("not json{{"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with corrupt file: %v", err)
	}
	if cfg.Projects == nil {
		t.Error("corrupt file should produce empty Projects map")
	}
}

func TestIncrementStartups(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	IncrementStartups()
	IncrementStartups()
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NumStartups != 2 {
		t.Errorf("NumStartups = %d; want 2", cfg.NumStartups)
	}
}

func TestIncrementStartups_PreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, ".claude.json")
	original := []byte(`{
  "mcpServers": {"global": {"command": "node"}},
  "custom": {"nested": true},
  "numStartups": 7,
  "projects": {
    "/tmp/project": {
      "mcpServers": {"local": {"command": "python"}},
      "disabledMcpServers": ["old"]
    }
  }
}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	IncrementStartups()

	var raw map[string]json.RawMessage
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["mcpServers"]; !ok {
		t.Fatal("mcpServers was removed")
	}
	if _, ok := raw["custom"]; !ok {
		t.Fatal("custom field was removed")
	}
	var count int
	if err := json.Unmarshal(raw["numStartups"], &count); err != nil {
		t.Fatal(err)
	}
	if count != 8 {
		t.Fatalf("numStartups = %d; want 8", count)
	}
}

func TestIncrementStartups_DoesNotOverwriteCorruptFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, ".claude.json")
	before := []byte("not json{{")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}

	IncrementStartups()

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("corrupt file was overwritten: %q", after)
	}
}

func TestSetTrusted_PreservesUnknownProjectFields(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("CLAUDE_CODE_SANDBOXED", "")
	cwd := filepath.Join(dir, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude.json")
	initial := `{
  "topLevel": "keep",
  "projects": {
    "` + filepath.ToSlash(cwd) + `": {
      "mcpServers": {"local": {"command": "node"}},
      "disabledMcpServers": ["srv"]
    }
  }
}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SetTrusted(cwd); err != nil {
		t.Fatal(err)
	}

	var raw map[string]json.RawMessage
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["topLevel"]; !ok {
		t.Fatal("top-level field was removed")
	}
	var projects map[string]map[string]json.RawMessage
	if err := json.Unmarshal(raw["projects"], &projects); err != nil {
		t.Fatal(err)
	}
	project := projects[cwd]
	if _, ok := project["mcpServers"]; !ok {
		t.Fatal("project mcpServers was removed")
	}
	if _, ok := project["disabledMcpServers"]; !ok {
		t.Fatal("project disabledMcpServers was removed")
	}
	var trusted bool
	if err := json.Unmarshal(project["hasTrustDialogAccepted"], &trusted); err != nil {
		t.Fatal(err)
	}
	if !trusted {
		t.Fatal("hasTrustDialogAccepted was not set")
	}
}
