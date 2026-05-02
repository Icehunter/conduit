// Package model centralises model selection for the agent loop.
//
// Priority order (mirrors getRuntimeMainLoopModel in the TS source):
//  1. ANTHROPIC_MODEL env var (explicit override)
//  2. CLAUDE_CODE_MODEL env var (alias)
//  3. DefaultModel constant
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

// SetOverride sets the runtime model override (from /model slash command).
func SetOverride(name string) {
	runtimeOverride.Store(&name)
}

// ClearOverride removes the runtime override (for testing).
func ClearOverride() {
	runtimeOverride.Store(nil)
}

// Default is the model used when no override is set. Matches the model
// Claude Code 2.1.126 ships with.
const Default = "claude-opus-4-7"

// MaxTokens is the default max_tokens value for /v1/messages.
// Real CC uses 16000 for the main loop; we match that.
const MaxTokens = 16000

// Resolve returns the model name to use, respecting environment overrides
// and the runtime /model override (highest priority after env).
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
	return Default
}
