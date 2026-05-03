// Package model centralises model selection for the agent loop.
//
// Priority order (mirrors getRuntimeMainLoopModel in the TS source):
//  1. Runtime /model override (highest)
//  2. ANTHROPIC_MODEL env var
//  3. CLAUDE_CODE_MODEL env var
//  4. settings.json "model" field (via SetDefault at startup)
//  5. Default constant
//
// The default matches the model shipped in Claude Code 2.1.126.
package model

import (
	"os"
	"sync/atomic"
)

// runtimeOverride holds a model name set via /model at runtime.
// Uses atomic pointer so it's safe to read from concurrent goroutines.
var runtimeOverride atomic.Pointer[string]

// settingsDefault holds the model from settings.json, set once at startup.
var settingsDefault atomic.Pointer[string]

// SetOverride sets the runtime model override (from /model slash command).
func SetOverride(name string) {
	runtimeOverride.Store(&name)
}

// ClearOverride removes the runtime override (for testing).
func ClearOverride() {
	runtimeOverride.Store(nil)
}

// SetDefault sets the model from settings.json. Called once at startup before
// any goroutines read Resolve(). Lower priority than env vars.
func SetDefault(name string) {
	settingsDefault.Store(&name)
}

// Default is the hardcoded fallback model. Matches Claude Code 2.1.126.
const Default = "claude-opus-4-7"

// Fast is the faster/cheaper model used when /fast is active.
// Mirrors getSmallFastModel() — Sonnet for fast responses.
const Fast = "claude-sonnet-4-6"

// MaxTokens is the default max_tokens value for /v1/messages.
// Real CC uses 16000 for the main loop; we match that.
const MaxTokens = 16000

// ThinkingBudgets maps effort levels to token budgets.
// Mirrors the thinking budget constants from the real CLI.
var ThinkingBudgets = map[string]int{
	"low":    1000,
	"normal": 0,
	"high":   8000,
	"max":    16000,
}

// Resolve returns the model name to use, applying priority order.
func Resolve() string {
	if p := runtimeOverride.Load(); p != nil && *p != "" {
		return *p
	}
	if m := os.Getenv("ANTHROPIC_MODEL"); m != "" {
		return m
	}
	if m := os.Getenv("CLAUDE_CODE_MODEL"); m != "" {
		return m
	}
	if p := settingsDefault.Load(); p != nil && *p != "" {
		return *p
	}
	return Default
}
