package settings

import (
	"os"
	"testing"
)

func TestApplyEnv_SetsVars(t *testing.T) {
	env := map[string]string{
		"CONDUIT_TEST_VAR_A": "value_a",
		"CONDUIT_TEST_VAR_B": "value_b",
	}
	// Ensure they're not already set.
	os.Unsetenv("CONDUIT_TEST_VAR_A")
	os.Unsetenv("CONDUIT_TEST_VAR_B")

	cleanup := ApplyEnv(env)
	defer cleanup()

	if got := os.Getenv("CONDUIT_TEST_VAR_A"); got != "value_a" {
		t.Errorf("CONDUIT_TEST_VAR_A = %q; want value_a", got)
	}
	if got := os.Getenv("CONDUIT_TEST_VAR_B"); got != "value_b" {
		t.Errorf("CONDUIT_TEST_VAR_B = %q; want value_b", got)
	}

	cleanup()

	if got := os.Getenv("CONDUIT_TEST_VAR_A"); got != "" {
		t.Errorf("after cleanup, CONDUIT_TEST_VAR_A = %q; want empty", got)
	}
}

func TestApplyEnv_NilMap(t *testing.T) {
	cleanup := ApplyEnv(nil)
	cleanup() // must not panic
}

func TestApplyEnv_EmptyMap(t *testing.T) {
	cleanup := ApplyEnv(map[string]string{})
	cleanup() // must not panic
}

func TestApplyEnv_CleanupRestoresPrevious(t *testing.T) {
	key := "CONDUIT_TEST_RESTORE_VAR"
	os.Setenv(key, "original")
	defer os.Unsetenv(key)

	cleanup := ApplyEnv(map[string]string{key: "override"})

	if got := os.Getenv(key); got != "override" {
		t.Errorf("during apply: %q; want override", got)
	}

	cleanup()

	if got := os.Getenv(key); got != "original" {
		t.Errorf("after cleanup: %q; want original", got)
	}
}

func TestClaudeDir_XDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// On non-Linux this test still runs but XDG won't affect claudeDir().
	// We just verify it doesn't panic.
	got := claudeDir()
	_ = got // result varies by platform
}

func TestClaudeDir_Default(t *testing.T) {
	// Without XDG, should return something under home.
	t.Setenv("XDG_CONFIG_HOME", "")
	got := claudeDir()
	if got == "" {
		t.Error("claudeDir() should return a non-empty path")
	}
}
