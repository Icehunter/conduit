// Package permissions implements Claude Code's permission gate.
//
// Mirrors src/utils/permissions/permissions.ts and shellRuleMatching.ts.
//
// A permission rule is a string like:
//   "Bash"              — allow/deny all Bash calls
//   "Bash(git log)"     — exact command match
//   "Bash(git log *)"   — prefix match (trailing space + * = prefix)
//   "Edit"              — all Edit calls
//   "Edit(/path/*)"     — path prefix match
package permissions

import (
	"strings"
	"sync"
)

// Mode is the active permission mode.
type Mode string

const (
	ModeDefault           Mode = "default"
	ModeAcceptEdits       Mode = "acceptEdits"
	ModeBypassPermissions Mode = "bypassPermissions"
	ModePlan              Mode = "plan"
)

// Decision is the result of a permission check.
type Decision int

const (
	DecisionAllow Decision = iota
	DecisionDeny
	DecisionAsk
)

// Gate holds the active permission state for a session.
type Gate struct {
	mu   sync.RWMutex
	mode Mode

	allow []string
	deny  []string
	ask   []string

	// sessionAllow holds rules added via "always allow" prompts this session.
	sessionAllow []string
}

// New constructs a Gate with the given settings.
func New(mode Mode, allow, deny, ask []string) *Gate {
	return &Gate{
		mode:  mode,
		allow: allow,
		deny:  deny,
		ask:   ask,
	}
}

// SetMode changes the active permission mode.
func (g *Gate) SetMode(m Mode) {
	g.mu.Lock()
	g.mode = m
	g.mu.Unlock()
}

// Mode returns the current permission mode.
func (g *Gate) Mode() Mode {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.mode
}

// Lists returns copies of the allow, deny, and ask rule lists (excluding session-level allow).
// This is used for display in the /permissions slash command.
func (g *Gate) Lists() (allow, deny, ask []string) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	allow = make([]string, len(g.allow))
	copy(allow, g.allow)
	deny = make([]string, len(g.deny))
	copy(deny, g.deny)
	ask = make([]string, len(g.ask))
	copy(ask, g.ask)
	return allow, deny, ask
}

// AllowForSession adds a rule to the session-level allow list.
func (g *Gate) AllowForSession(rule string) {
	g.mu.Lock()
	g.sessionAllow = append(g.sessionAllow, rule)
	g.mu.Unlock()
}

// Check determines whether toolName with toolInput may run.
// toolInput is the raw argument string (e.g. the command for Bash).
func (g *Gate) Check(toolName, toolInput string) Decision {
	g.mu.RLock()
	mode := g.mode
	allow := g.allow
	deny := g.deny
	ask := g.ask
	sessionAllow := g.sessionAllow
	g.mu.RUnlock()

	// bypassPermissions / acceptEdits modes skip all checks.
	if mode == ModeBypassPermissions || mode == ModeAcceptEdits {
		return DecisionAllow
	}

	// Deny list is checked first (highest priority).
	for _, rule := range deny {
		if matchRule(rule, toolName, toolInput) {
			return DecisionDeny
		}
	}

	// Session-level allow (from "always allow" prompts).
	for _, rule := range sessionAllow {
		if matchRule(rule, toolName, toolInput) {
			return DecisionAllow
		}
	}

	// Settings allow list.
	for _, rule := range allow {
		if matchRule(rule, toolName, toolInput) {
			return DecisionAllow
		}
	}

	// Ask list — forces prompt even if mode would normally allow.
	for _, rule := range ask {
		if matchRule(rule, toolName, toolInput) {
			return DecisionAsk
		}
	}

	// Default behaviour by mode.
	switch mode {
	case ModeDefault:
		// In default mode, read-only tools auto-allow; others ask.
		return DecisionAsk
	case ModePlan:
		return DecisionAsk
	}
	return DecisionAsk
}

// matchRule returns true if the rule matches toolName(toolInput).
//
// Rule forms:
//   "ToolName"              — matches any call to ToolName
//   "ToolName(exact)"       — exact input match
//   "ToolName(prefix *)"    — input starts with "prefix " (space before *)
//   "ToolName(glob*)"       — input starts with "glob" (wildcard anywhere)
func matchRule(rule, toolName, toolInput string) bool {
	// Split "ToolName(content)" — no parens means tool-only match.
	paren := strings.IndexByte(rule, '(')
	if paren < 0 {
		return strings.EqualFold(rule, toolName)
	}
	ruleTool := rule[:paren]
	if !strings.EqualFold(ruleTool, toolName) {
		return false
	}
	if !strings.HasSuffix(rule, ")") {
		return false
	}
	pattern := rule[paren+1 : len(rule)-1]
	return matchPattern(pattern, toolInput)
}

// matchPattern matches an input string against a rule pattern.
// Supports exact match, prefix (trailing " *"), and simple glob (*).
func matchPattern(pattern, input string) bool {
	// Legacy "prefix:*" form.
	if strings.HasSuffix(pattern, ":*") {
		prefix := pattern[:len(pattern)-2]
		return strings.HasPrefix(input, prefix)
	}

	// "prefix *" form — space before trailing star means prefix match.
	if strings.HasSuffix(pattern, " *") {
		prefix := pattern[:len(pattern)-2]
		return strings.HasPrefix(input, prefix)
	}

	// Wildcard anywhere in pattern — convert to prefix/suffix/contains check.
	if strings.Contains(pattern, "*") {
		return matchGlob(pattern, input)
	}

	// Exact match.
	return pattern == input
}

// matchGlob does simple single-wildcard glob matching.
func matchGlob(pattern, input string) bool {
	parts := strings.SplitN(pattern, "*", 2)
	if len(parts) == 1 {
		return pattern == input
	}
	left, right := parts[0], parts[1]
	if !strings.HasPrefix(input, left) {
		return false
	}
	remaining := input[len(left):]
	if right == "" {
		return true
	}
	return strings.HasSuffix(remaining, right)
}

// SuggestRule returns the recommended permission rule string for a tool call.
// Used when asking the user "always allow?".
func SuggestRule(toolName, toolInput string) string {
	if toolInput == "" {
		return toolName
	}
	return toolName + "(" + toolInput + ")"
}
