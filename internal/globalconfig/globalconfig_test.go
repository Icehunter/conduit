package globalconfig

import (
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
