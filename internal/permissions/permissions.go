// Package permissions implements Claude Code's permission gate.
//
// Mirrors src/utils/permissions/permissions.ts and shellRuleMatching.ts.
//
// A permission rule is a string like:
//
//	"Bash"              — allow/deny all Bash calls
//	"Bash(git log)"     — exact command match
//	"Bash(git log *)"   — prefix match (trailing space + * = prefix)
//	"Edit"              — all Edit calls
//	"Edit(/path/*)"     — path prefix match
package permissions

import (
	"path/filepath"
	"strings"
	"sync"

	"github.com/icehunter/conduit/internal/settings"
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
	// AskUserQuestion is always auto-approved: it's the agent asking the user
	// a question, not invoking an action that needs permission.
	if toolName == "AskUserQuestion" {
		return DecisionAllow
	}

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
//
//	"ToolName"              — matches any call to ToolName
//	"ToolName(exact)"       — exact input match
//	"ToolName(prefix *)"    — input starts with "prefix " (space before *)
//	"ToolName(glob*)"       — input starts with "glob" (wildcard anywhere)
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
	if strings.EqualFold(toolName, "Bash") {
		if pattern == "readonly:*" {
			return isReadOnlyBashInspection(toolInput)
		}
		if normalized, ok := normalizeBashPermissionInput(toolInput); ok {
			return matchPattern(pattern, normalized)
		}
	}
	return matchPattern(pattern, toolInput)
}

// matchPattern matches an input string against a rule pattern.
// Supports exact match, prefix (trailing " *"), and simple glob (*).
func matchPattern(pattern, input string) bool {
	// TS uses "//path" as canonical form for absolute paths (double leading slash).
	// Strip the extra leading slash so patterns match real filesystem paths.
	if strings.HasPrefix(pattern, "//") {
		pattern = pattern[1:]
	}

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

// matchGlob does simple glob matching supporting * (any sequence) and ** (any depth).
// Uses the two-pointer/checkpoint algorithm: O(n·m) with no recursion, safe against
// adversarial patterns with many wildcards.
func matchGlob(pattern, input string) bool {
	// Fast path: trailing /** means "this directory and anything under it".
	if strings.HasSuffix(pattern, "/**") {
		prefix := pattern[:len(pattern)-3]
		return input == prefix || strings.HasPrefix(input, prefix+"/")
	}
	// Fast path: trailing /* means "anything directly under this directory".
	if strings.HasSuffix(pattern, "/*") {
		prefix := pattern[:len(pattern)-2]
		rest := strings.TrimPrefix(input, prefix+"/")
		return rest != input && !strings.Contains(rest, "/")
	}

	// General matching: two-pointer with a star checkpoint.
	// Whenever we see a '*' in the pattern we record (starPos, matchPos) and
	// advance. On mismatch we backtrack to starPos+1 and retry from matchPos+1.
	p, i := 0, 0
	starPos, matchPos := -1, 0
	for i < len(input) {
		if p < len(pattern) && pattern[p] == '*' {
			starPos = p
			matchPos = i
			p++
		} else if p < len(pattern) && pattern[p] == input[i] {
			p++
			i++
		} else if starPos >= 0 {
			// Backtrack: the previous '*' consumes one more character.
			p = starPos + 1
			matchPos++
			i = matchPos
		} else {
			return false
		}
	}
	// Consume any trailing stars.
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// SuggestRule returns the broad rule string to use for "always allow".
// Mirrors TS suggestionForPrefix / createReadRuleSuggestion:
//   - Read/Edit/Write: directory-level  Read(//dir/**)
//   - Bash: prefix wildcard  Bash(cmd subcmd:*)  or exact  Bash(full command)
//   - others: tool name only
func SuggestRule(toolName, toolInput string) string {
	switch toolName {
	case "Read", "Edit", "Write":
		if toolInput == "" {
			return toolName
		}
		// Clean before computing dir to prevent path traversal in allow rules.
		// e.g. "/home/user/../../etc/passwd" → "/etc" rather than a rule that
		// allows access to the filesystem root via "..".
		cleaned := filepath.ToSlash(filepath.Clean(toolInput))
		dir := filepath.ToSlash(filepath.Dir(cleaned))
		if dir == "" || dir == "." || dir == "/" {
			return toolName
		}
		// Prepend extra / for absolute paths matching TS "//{path}/**" pattern.
		if filepath.IsAbs(toolInput) || strings.HasPrefix(cleaned, "/") {
			return toolName + "(/" + dir + "/**)"
		}
		return toolName + "(" + dir + "/**)"

	case "Bash":
		if isReadOnlyBashInspection(toolInput) {
			return "Bash(readonly:*)"
		}
		if normalized, ok := normalizeBashPermissionInput(toolInput); ok {
			toolInput = normalized
		}
		// Try to extract "cmd subcmd" prefix → "Bash(cmd subcmd:*)"
		if prefix := bashCommandPrefix(toolInput); prefix != "" {
			return "Bash(" + prefix + ":*)"
		}
		if toolInput != "" {
			return "Bash(" + toolInput + ")"
		}
		return "Bash"

	default:
		if toolInput == "" {
			return toolName
		}
		return toolName + "(" + toolInput + ")"
	}
}

func normalizeBashPermissionInput(cmd string) (string, bool) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", false
	}
	parts, ok := splitShellChain(cmd)
	if !ok || len(parts) < 2 {
		return "", false
	}
	first := strings.TrimSpace(parts[0])
	fields := strings.Fields(first)
	if len(fields) != 2 || fields[0] != "cd" {
		return "", false
	}
	return strings.Join(parts[1:], " && "), true
}

func isReadOnlyBashInspection(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return false
	}
	if normalized, ok := normalizeBashPermissionInput(cmd); ok {
		cmd = normalized
	}
	parts, ok := splitShellChain(cmd)
	if !ok || len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !isReadOnlySimpleCommand(strings.TrimSpace(part)) {
			return false
		}
	}
	return true
}

func splitShellChain(cmd string) ([]string, bool) {
	var parts []string
	start := 0
	sq, dq, esc := false, false, false
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if esc {
			esc = false
			continue
		}
		if c == '\\' {
			esc = true
			continue
		}
		if sq {
			if c == '\'' {
				sq = false
			}
			continue
		}
		if c == '\'' && !dq {
			sq = true
			continue
		}
		if c == '"' {
			dq = !dq
			continue
		}
		if dq {
			if c == '$' && i+1 < len(cmd) && cmd[i+1] == '(' {
				return nil, false
			}
			if c == '`' {
				return nil, false
			}
			continue
		}
		switch c {
		case '\n', ';', '`', '>':
			return nil, false
		case '$':
			if i+1 < len(cmd) && cmd[i+1] == '(' {
				return nil, false
			}
		case '<':
			if i+1 < len(cmd) && cmd[i+1] == '<' {
				return nil, false
			}
		case '&':
			if i+1 < len(cmd) && cmd[i+1] == '&' {
				parts = append(parts, strings.TrimSpace(cmd[start:i]))
				i++
				start = i + 1
			} else {
				return nil, false
			}
		case '|':
			if i+1 < len(cmd) && cmd[i+1] == '|' {
				return nil, false
			}
			parts = append(parts, strings.TrimSpace(cmd[start:i]))
			start = i + 1
		}
	}
	parts = append(parts, strings.TrimSpace(cmd[start:]))
	for _, part := range parts {
		if part == "" {
			return nil, false
		}
	}
	return parts, true
}

func isReadOnlySimpleCommand(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	bin := filepath.Base(fields[0])
	switch bin {
	case "ls", "ll", "la", "dir", "cat", "bat", "less", "more", "head", "tail",
		"echo", "printf", "pwd", "which", "type", "whereis", "wc", "du", "df",
		"stat", "file", "uname", "hostname", "whoami", "id", "date", "uptime",
		"ps", "env", "printenv", "diff", "cmp", "grep", "egrep", "fgrep", "rg",
		"ag", "sort", "uniq", "cut", "awk", "jq", "yq":
		return true
	case "find", "fd":
		return !containsAnyArg(fields[1:], "-delete", "-exec", "-execdir", "-ok", "-okdir")
	case "sed":
		return !containsAnyArg(fields[1:], "-i", "--in-place")
	case "git":
		return len(fields) >= 2 && map[string]bool{
			"log": true, "status": true, "diff": true, "show": true, "blame": true,
			"branch": true, "tag": true, "remote": true, "stash": true,
			"describe": true, "rev-parse": true, "ls-files": true,
			"shortlog": true, "reflog": true, "config": true,
		}[fields[1]]
	case "go":
		return len(fields) >= 2 && map[string]bool{"version": true, "env": true, "list": true, "doc": true, "vet": true}[fields[1]]
	case "npm":
		return len(fields) >= 2 && map[string]bool{"list": true, "ls": true, "outdated": true, "audit": true, "info": true, "view": true}[fields[1]]
	}
	return false
}

func containsAnyArg(fields []string, values ...string) bool {
	for _, field := range fields {
		for _, value := range values {
			if field == value || strings.HasPrefix(field, value+"=") {
				return true
			}
		}
	}
	return false
}

// bashCommandPrefix returns "cmd subcmd" if the command has a recognisable
// subcommand (git status → "git status", npm install → "npm install").
// Returns "" if no subcommand pattern is found.
func bashCommandPrefix(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return ""
	}
	bin := fields[0]
	if idx := strings.LastIndexByte(bin, '/'); idx >= 0 {
		bin = bin[idx+1:]
	}
	// Tools with well-known subcommands.
	subcmdTools := map[string]bool{
		"git": true, "npm": true, "yarn": true, "pnpm": true,
		"cargo": true, "go": true, "gh": true, "docker": true,
		"kubectl": true, "terraform": true, "aws": true, "make": true,
	}
	if !subcmdTools[bin] {
		return ""
	}
	// First arg that doesn't start with "-" is the subcommand.
	for _, f := range fields[1:] {
		if !strings.HasPrefix(f, "-") {
			return bin + " " + f
		}
	}
	return ""
}

// PersistAllow writes rule to Conduit's project-local settings file.
// Conduit reads Claude project settings for compatibility, but new runtime
// approvals are persisted under <cwd>/.conduit/settings.local.json.
func PersistAllow(rule, cwd string) error {
	return settings.SaveConduitProjectPermissionAllow(cwd, rule)
}
