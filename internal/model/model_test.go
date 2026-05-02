package model

import (
	"os"
	"testing"
)

func TestResolve_Default(t *testing.T) {
	os.Unsetenv("ANTHROPIC_MODEL")
	os.Unsetenv("CLAUDE_CODE_MODEL")
	if got := Resolve(); got != Default {
		t.Errorf("Resolve() = %q, want %q", got, Default)
	}
}

func TestResolve_AnthropicModelEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "claude-custom")
	os.Unsetenv("CLAUDE_CODE_MODEL")
	if got := Resolve(); got != "claude-custom" {
		t.Errorf("Resolve() = %q, want claude-custom", got)
	}
}

func TestResolve_ClaudeCodeModelEnv(t *testing.T) {
	os.Unsetenv("ANTHROPIC_MODEL")
	t.Setenv("CLAUDE_CODE_MODEL", "claude-other")
	if got := Resolve(); got != "claude-other" {
		t.Errorf("Resolve() = %q, want claude-other", got)
	}
}

func TestResolve_AnthropicModelTakesPrecedence(t *testing.T) {
	t.Setenv("ANTHROPIC_MODEL", "model-a")
	t.Setenv("CLAUDE_CODE_MODEL", "model-b")
	if got := Resolve(); got != "model-a" {
		t.Errorf("Resolve() = %q, want model-a (ANTHROPIC_MODEL should win)", got)
	}
}

func TestDefault_IsOpus(t *testing.T) {
	if Default != "claude-opus-4-7" {
		t.Errorf("Default = %q, want claude-opus-4-7", Default)
	}
}
