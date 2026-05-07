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

	cwd          string
	trustedRoots []string

	allow []string
	deny  []string
	ask   []string

	// sessionAllow holds rules added via "always allow" prompts this session.
	sessionAllow []string
}

// New constructs a Gate with the given settings.
// cwd is the working directory for implicit-trust path checks.
// trustedRoots is the list of trusted ancestor paths (from globalconfig.TrustedAncestors).
func New(cwd string, trustedRoots []string, mode Mode, allow, deny, ask []string) *Gate {
	return &Gate{
		mode:         mode,
		cwd:          cwd,
		trustedRoots: trustedRoots,
		allow:        allow,
		deny:         deny,
		ask:          ask,
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

// sanitizeCWD replaces path separators and colons with dashes, matching
// memdir.sanitizePath. Inlined here to avoid an import cycle.
func sanitizeCWD(p string) string {
	return strings.NewReplacer(
		"/", "-",
		"\\", "-",
		":", "-",
	).Replace(p)
}

// isImplicitlyTrustedPath returns true for file operations on conduit's own
// per-project storage directories. These paths are picked by conduit itself
// (never by the model), so prompting for them is meaningless.
func isImplicitlyTrustedPath(toolName, toolInput, cwd string) bool {
	switch toolName {
	case "Read", "Write", "Edit", "NotebookEdit":
	default:
		return false
	}
	if toolInput == "" || cwd == "" {
		return false
	}
	p := toSlash(filepath.Clean(toolInput))
	sanitized := sanitizeCWD(cwd)
	conduitPrefix := toSlash(filepath.Join(settings.ConduitDir(), "projects", sanitized))
	claudePrefix := toSlash(filepath.Join(settings.ClaudeDir(), "projects", sanitized))
	for _, prefix := range []string{conduitPrefix, claudePrefix} {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

// toolIsReadOnly reports whether the tool (and given input) is considered
// a read-only, non-mutating operation.
func toolIsReadOnly(toolName, toolInput string) bool {
	switch toolName {
	case "Read", "Glob", "Grep", "LS", "LSP",
		"WebSearch", "WebFetch", "ToolSearch",
		"TaskList", "TaskGet", "TaskOutput":
		return true
	case "Bash":
		return isReadOnlyBashInspection(toolInput)
	}
	return false
}

// toolIsEdit reports whether the tool performs file-editing operations.
func toolIsEdit(toolName string) bool {
	switch toolName {
	case "Read", "Edit", "Write", "NotebookEdit":
		return true
	}
	return false
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
	cwd := g.cwd
	trustedRoots := g.trustedRoots
	allow := g.allow
	deny := g.deny
	ask := g.ask
	sessionAllow := g.sessionAllow
	g.mu.RUnlock()

	// bypassPermissions skips all checks.
	if mode == ModeBypassPermissions {
		return DecisionAllow
	}

	// Implicit trust: conduit's own per-project storage never prompts.
	if isImplicitlyTrustedPath(toolName, toolInput, cwd) {
		return DecisionAllow
	}

	// Reads inside a trusted ancestor directory never prompt.
	if toolIsReadOnly(toolName, toolInput) && toolName != "Bash" {
		normInput := toSlash(filepath.Clean(toolInput))
		for _, root := range trustedRoots {
			normRoot := toSlash(filepath.Clean(root))
			if normInput == normRoot || strings.HasPrefix(normInput, normRoot+"/") {
				return DecisionAllow
			}
		}
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
	case ModeBypassPermissions:
		return DecisionAllow
	case ModeAcceptEdits:
		if toolIsEdit(toolName) {
			return DecisionAllow
		}
		return DecisionAsk
	case ModePlan:
		if toolIsReadOnly(toolName, toolInput) {
			return DecisionAllow
		}
		return DecisionAsk
	case ModeDefault:
		if toolIsReadOnly(toolName, toolInput) {
			return DecisionAllow
		}
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
	// For path-based tools, normalise separators to / before matching.
	// Use unconditional backslash replacement so Windows paths are handled
	// correctly on all platforms (filepath.ToSlash is a no-op on non-Windows).
	if isPathBasedTool(toolName) {
		pattern = toSlash(pattern)
		toolInput = toSlash(toolInput)
	}
	return matchPattern(pattern, toolInput)
}

// isPathBasedTool reports whether the tool operates on filesystem paths.
func isPathBasedTool(name string) bool {
	switch strings.ToLower(name) {
	case "read", "edit", "write", "notebookedit", "glob", "grep", "ls":
		return true
	}
	return false
}

// isWindowsDriveLetter reports whether b is an ASCII letter (a-z or A-Z).
func isWindowsDriveLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// toSlash converts all backslashes to forward slashes unconditionally.
// Unlike filepath.ToSlash this works on non-Windows when handling
// Windows-originated paths (e.g. in tests or cross-platform tooling).
func toSlash(s string) string {
	return strings.ReplaceAll(s, "\\", "/")
}

// matchPattern matches an input string against a rule pattern.
// Supports exact match, prefix (trailing " *"), and simple glob (*).
func matchPattern(pattern, input string) bool {
	// TS uses "//path" as canonical form for absolute paths (double leading slash).
	// Strip the extra leading slash so patterns match real filesystem paths.
	if strings.HasPrefix(pattern, "//") {
		pattern = pattern[1:]
		// After stripping, "/C:/..." is a Windows drive path stored with a
		// spurious leading slash.  Remove it so it matches "C:/..." paths.
		if len(pattern) >= 3 && pattern[0] == '/' && isWindowsDriveLetter(pattern[1]) && pattern[2] == ':' {
			pattern = pattern[1:]
		}
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
		// -delete, -exec*, -ok* execute or mutate; -fprint* write to files.
		return !containsAnyArg(fields[1:],
			"-delete", "-exec", "-execdir", "-ok", "-okdir",
			"-fprint", "-fprint0", "-fprintf", "-printf")
	case "sed":
		// -i / --in-place edits files in place; `e` flag in s/// executes shell.
		// Treat any sed command as non-readonly to be safe (detecting s///e
		// inline is too fragile without a full parser).
		return false
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

// bashCommandPrefix returns "cmd subcmd" for known multi-subcommand tools, or
// just "cmd" for any other binary that has at least one argument. Returns ""
// only when the command is a bare binary with no arguments.
func bashCommandPrefix(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return ""
	}
	bin := fields[0]
	if idx := strings.LastIndexByte(bin, '/'); idx >= 0 {
		bin = bin[idx+1:]
	}
	// Tools with well-known subcommands: use "bin subcmd" form.
	subcmdTools := map[string]bool{
		"git": true, "npm": true, "yarn": true, "pnpm": true,
		"cargo": true, "go": true, "gh": true, "docker": true,
		"kubectl": true, "terraform": true, "aws": true, "make": true,
	}
	if subcmdTools[bin] {
		// First arg that doesn't start with "-" is the subcommand.
		for _, f := range fields[1:] {
			if !strings.HasPrefix(f, "-") {
				return bin + " " + f
			}
		}
	}
	// For all other binaries with at least one argument, use bin-only prefix.
	// SuggestRule turns this into "Bash(bin:*)".
	return bin
}

// PersistAllow writes rule to Conduit's project-local settings file.
// Conduit reads Claude project settings for compatibility, but new runtime
// approvals are persisted under <cwd>/.conduit/settings.local.json.
func PersistAllow(rule, cwd string) error {
	return settings.SaveConduitProjectPermissionAllow(cwd, rule)
}
