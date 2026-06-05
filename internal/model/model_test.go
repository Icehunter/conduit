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

func TestUsableContext(t *testing.T) {
	tests := []struct {
		name           string
		model          string
		reservedOutput int
		wantMin        int
	}{
		{"default model", "claude-haiku-4", 0, 100000},
		// sonnet-4 without suffix now defaults to 200K; usable = 200K - 20K reserve = 180K.
		{"sonnet default (200K)", "claude-sonnet-4-latest", 0, 100000},
		// sonnet-4 with [1m] suffix opts into 1M; usable = 1M - 20K reserve = 980K.
		{"1M opt-in model", "claude-sonnet-4-6[1m]", 0, 900000},
		{"custom reserved", "claude-haiku-4", 50000, 100000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UsableContext(tt.model, tt.reservedOutput)
			if got < tt.wantMin {
				t.Errorf("UsableContext() = %d, want >= %d", got, tt.wantMin)
			}
		})
	}
}

func TestCheckOverflow(t *testing.T) {
	usable := 100000 // 100K usable context

	tests := []struct {
		name   string
		tokens int
		want   OverflowLevel
	}{
		{"below micro threshold", 70000, OverflowNone},
		{"at micro threshold (80%)", 80000, OverflowMicro},
		{"between micro and full", 90000, OverflowMicro},
		{"at full threshold (95%)", 95000, OverflowFull},
		{"above full threshold", 99000, OverflowFull},
		{"at 100%", 100000, OverflowCritical},
		{"above 100%", 110000, OverflowCritical},
		{"zero tokens", 0, OverflowNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckOverflowForUsable(usable, tt.tokens)
			if got != tt.want {
				t.Errorf("CheckOverflowForUsable(%d, %d) = %d, want %d", usable, tt.tokens, got, tt.want)
			}
		})
	}
}

func TestContextWindowFor(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		// All models default to 200K; 1M requires explicit [1m] suffix.
		{"claude-haiku-4", ContextWindowDefault},
		{"claude-sonnet-4-latest", ContextWindowDefault},
		{"claude-sonnet-4-6", ContextWindowDefault},
		{"claude-opus-4-7", ContextWindowDefault},
		// Explicit opt-in via [1m] suffix (case-insensitive).
		{"some-model[1m]", ContextWindow1M},
		{"claude-sonnet-4-6[1m]", ContextWindow1M},
		{"claude-opus-4-7[1m]", ContextWindow1M},
		{"CLAUDE-SONNET-4-6[1M]", ContextWindow1M},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			got := ContextWindowFor(tt.model)
			if got != tt.want {
				t.Errorf("ContextWindowFor(%q) = %d, want %d", tt.model, got, tt.want)
			}
		})
	}
}

func TestStripSuffix(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no suffix", "claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"1m suffix lowercase", "claude-sonnet-4-6[1m]", "claude-sonnet-4-6"},
		{"1m suffix uppercase", "claude-sonnet-4-6[1M]", "claude-sonnet-4-6"},
		{"1m suffix mixed case", "claude-opus-4-8[1M]", "claude-opus-4-8"},
		{"empty string", "", ""},
		{"just suffix", "[1m]", ""},
		{"unrelated suffix", "claude-haiku-4-5-20251001", "claude-haiku-4-5-20251001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripSuffix(tt.input)
			if got != tt.want {
				t.Errorf("StripSuffix(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
