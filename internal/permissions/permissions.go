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
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/shellsafe"
)

// Mode is the active permission mode.
type Mode string

const (
	ModeDefault           Mode = "default"
	ModeAcceptEdits       Mode = "acceptEdits"
	ModeAcceptEditsLive   Mode = "acceptEditsLive" // acceptEdits + mid-turn hunk review
	ModeBypassPermissions Mode = "bypassPermissions"
	ModePlan              Mode = "plan"
	ModeCouncil           Mode = "council"
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
	// maxMode is the permission ceiling established when a gate is cloned from a
	// parent. SetMode will not raise the mode above this ceiling, preventing
	// sub-agents from escalating beyond the permission level of their parent.
	maxMode    Mode
	hasMaxMode bool

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

// modePermissiveness returns a numeric level for a Mode used to enforce
// ceiling checks on cloned gates. Higher values are more permissive.
func modePermissiveness(m Mode) int {
	switch m {
	case ModeCouncil:
		return 0
	case ModePlan:
		return 1
	case ModeDefault:
		return 2
	case ModeAcceptEditsLive:
		return 3
	case ModeAcceptEdits:
		return 4
	case ModeBypassPermissions:
		return 5
	default:
		return 2
	}
}

// SetMode changes the active permission mode. If the gate was cloned from a
// parent (hasMaxMode == true) the mode is clamped to the parent's ceiling so
// sub-agents cannot escalate beyond the permission level they were spawned with.
func (g *Gate) SetMode(m Mode) {
	g.mu.Lock()
	if g.hasMaxMode && modePermissiveness(m) > modePermissiveness(g.maxMode) {
		m = g.maxMode
	}
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

// Clone returns a deep copy of the Gate — all rules and current mode are
// inherited but future mutations (SetMode, AllowForSession) on either side
// are independent. The clone records the parent's current mode as a ceiling so
// sub-agents cannot escalate beyond the permission level they were spawned with.
func (g *Gate) Clone() *Gate {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return &Gate{
		mode:         g.mode,
		maxMode:      g.mode,
		hasMaxMode:   true,
		cwd:          g.cwd,
		trustedRoots: append([]string(nil), g.trustedRoots...),
		allow:        append([]string(nil), g.allow...),
		deny:         append([]string(nil), g.deny...),
		ask:          append([]string(nil), g.ask...),
		sessionAllow: append([]string(nil), g.sessionAllow...),
	}
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
		"TaskList", "TaskGet", "TaskOutput", "BashOutput":
		return true
	case "Bash":
		return shellsafe.IsReadOnly(toolInput)
	}
	return false
}

// toolIsEdit reports whether the tool performs file-editing operations.
func toolIsEdit(toolName string) bool {
	switch toolName {
	case "Read", "Edit", "Write", "NotebookEdit", "HashEdit", "MultiEdit":
		return true
	}
	return false
}

// Protected write targets: files and directories whose contents later run
// code or change what the agent runs (git hooks and config, shell startup
// files, editor tasks, MCP config, agent/command definitions, settings).
var (
	protectedFiles = map[string]bool{
		".gitconfig": true, ".gitmodules": true, ".bashrc": true,
		".bash_profile": true, ".zshrc": true, ".zshenv": true,
		".zprofile": true, ".profile": true, ".ripgreprc": true,
		".mcp.json": true, ".claude.json": true,
	}
	protectedDirs = map[string]bool{
		".git": true, ".vscode": true, ".idea": true,
		".husky": true, ".githooks": true,
	}
	// protectedConfigRoots are config directories whose children below are
	// protected: agent and command definitions at any depth, and a fixed set
	// of top-level files.
	protectedConfigRoots = map[string]struct {
		subdirs map[string]bool
		files   map[string]bool
	}{
		".claude":  {subdirs: map[string]bool{"commands": true, "agents": true}},
		".conduit": {subdirs: map[string]bool{"commands": true, "agents": true}, files: map[string]bool{"conduit.json": true, "mcp.json": true}},
	}
	// shortNamesApply gates the Windows 8.3 short-name rejection.
	shortNamesApply = runtime.GOOS == "windows"
	shortNameRe     = regexp.MustCompile(`~\d`)
)

// toolIsWrite reports whether the tool writes the file named by its input.
func toolIsWrite(toolName string) bool {
	switch toolName {
	case "Write", "Edit", "MultiEdit", "NotebookEdit", "HashEdit",
		// AstGrep (rewrite) and SkillManage (create/update/promote) only
		// carry a write target in their permission input; read-only calls
		// carry none, so the protected-path check never fires for them.
		"AstGrep", "SkillManage":
		return true
	}
	return false
}

// normComponent lowercases a path component and trims the trailing dots and
// spaces Windows ignores, so ".git." and ".git " name the same directory.
func normComponent(c string) string {
	return strings.TrimRight(strings.ToLower(c), ". ")
}

// matchesProtected reports whether the cleaned slash path hits a protected
// location. Matching is on case-insensitive path components.
func matchesProtected(slashPath string) bool {
	parts := strings.Split(slashPath, "/")
	for i, raw := range parts {
		part := normComponent(raw)
		if shortNamesApply && shortNameRe.MatchString(part) {
			return true
		}
		last := i == len(parts)-1
		if protectedDirs[part] || (last && protectedFiles[part]) {
			return true
		}
		if part == ".config" && i+1 < len(parts) && normComponent(parts[i+1]) == "git" {
			return true
		}
		root, ok := protectedConfigRoots[part]
		if !ok || i+1 >= len(parts) {
			continue
		}
		next := normComponent(parts[i+1])
		if root.subdirs[next] {
			return true
		}
		if i+2 == len(parts) && (root.files[next] ||
			(strings.HasPrefix(next, "settings") && strings.HasSuffix(next, ".json"))) {
			return true
		}
	}
	return false
}

// absFor resolves path against cwd and cleans it.
func absFor(cwd, path string) string {
	if !filepath.IsAbs(path) && cwd != "" {
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path)
}

// resolveSymlinks evaluates symlinks in path. When the leaf does not exist yet
// it resolves the deepest existing parent and re-appends the remainder.
func resolveSymlinks(path string) string {
	var rest []string
	p := path
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				r = filepath.Join(r, rest[i])
			}
			return r
		}
		parent := filepath.Dir(p)
		if parent == p {
			return path
		}
		rest = append(rest, filepath.Base(p))
		p = parent
	}
}

// isProtectedWritePath reports whether path (resolved against cwd) targets a
// protected location, checking both the literal path and its symlink-resolved
// form so a symlinked directory cannot hide a protected one.
func isProtectedWritePath(cwd, path string) bool {
	if path == "" {
		return false
	}
	abs := absFor(cwd, path)
	if dotDotAfterSymlink(cwd, path) {
		return true
	}
	if matchesProtected(toSlash(filepath.Clean(path))) || matchesProtected(toSlash(abs)) {
		return true
	}
	return matchesProtected(toSlash(resolveSymlinks(abs)))
}

// dotDotAfterSymlink reports whether the raw (uncleaned) path has a ".."
// component directly after a symlinked component. The OS resolves "link/.."
// through the link while a lexical clean drops both, so the gate and the
// filesystem would disagree about the target; such writes always prompt.
func dotDotAfterSymlink(cwd, path string) bool {
	raw := path
	if !filepath.IsAbs(raw) && cwd != "" {
		raw = cwd + string(filepath.Separator) + raw
	}
	cur := string(filepath.Separator)
	if vol := filepath.VolumeName(raw); vol != "" {
		cur = vol + cur
	}
	prevLink := false
	for _, part := range strings.FieldsFunc(raw[len(filepath.VolumeName(raw)):], func(r rune) bool { return r == '/' || r == filepath.Separator }) {
		switch part {
		case ".":
			continue
		case "..":
			if prevLink {
				return true
			}
			cur = filepath.Dir(cur)
			continue
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		prevLink = err == nil && fi.Mode()&os.ModeSymlink != 0
	}
	return false
}

// IsProtectedPath reports whether a write to path (resolved against cwd)
// targets a protected location. It is the path-only form of IsProtectedWrite,
// for callers that must pick the write target out of several candidates.
func IsProtectedPath(cwd, path string) bool { return isProtectedWritePath(cwd, path) }

// IsProtectedWrite reports whether toolName writing toolInput targets a
// protected path. Callers use it to keep the prompt even when a hook approved.
func (g *Gate) IsProtectedWrite(toolName, toolInput string) bool {
	g.mu.RLock()
	cwd := g.cwd
	g.mu.RUnlock()
	return toolIsWrite(toolName) && isProtectedWritePath(cwd, toolInput)
}

// underAny reports whether the symlink-resolved path lies inside any of roots
// (also resolved).
func underAny(cwd, path string, roots []string) bool {
	rp := toSlash(resolveSymlinks(absFor(cwd, path)))
	for _, root := range roots {
		if root == "" {
			continue
		}
		rr := toSlash(resolveSymlinks(absFor(cwd, root)))
		if rp == rr || strings.HasPrefix(rp, strings.TrimSuffix(rr, "/")+"/") {
			return true
		}
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

	// For Bash, decompose the command so rules are matched against each command
	// that actually runs. Matching the raw string alone is not a boundary: a
	// denied program hidden after `&&` never matches a prefix rule, and a leading
	// space is enough to dodge one. Empty for non-Bash or unparseable input, in
	// which case matching falls back to the raw string only.
	segments := bashSegments(toolName, toolInput)

	// Deny list is checked first (highest priority), before any trust shortcut
	// below, so a deny rule beats implicit trust and trusted roots. Any segment
	// is enough.
	for _, rule := range deny {
		if matchRuleAnySegment(rule, toolName, toolInput, segments) {
			return DecisionDeny
		}
	}

	// Writes to config files that later execute code (git config, shell rc
	// files, editor tasks, agent/command definitions, settings) are never
	// auto-approved by a mode, trusted root, implicit trust or allow rule.
	if toolIsWrite(toolName) && isProtectedWritePath(cwd, toolInput) {
		if mode == ModePlan || mode == ModeCouncil {
			return DecisionDeny
		}
		return DecisionAsk
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

	// Plan and council modes are intentionally restrictive phases — allow-lists
	// and ask-lists are skipped so that pre-existing "always allow" rules cannot
	// silently promote writes past the mode boundary. The mode itself is the
	// decision for non-read-only tools.
	if mode == ModePlan || mode == ModeCouncil {
		if toolIsReadOnly(toolName, toolInput) {
			return DecisionAllow
		}
		if mode == ModeCouncil {
			return DecisionDeny
		}
		return DecisionAsk
	}

	// Allow lists. Session-level ("always allow" prompts) and settings rules are
	// considered together so a command can be approved by a mix of both.
	if allowsInput(toolName, toolInput, segments, sessionAllow, allow) {
		return DecisionAllow
	}

	// Ask list — forces prompt even if mode would normally allow. Any segment.
	for _, rule := range ask {
		if matchRuleAnySegment(rule, toolName, toolInput, segments) {
			return DecisionAsk
		}
	}

	// Default behaviour by mode.
	switch mode {
	case ModeBypassPermissions:
		return DecisionAllow
	case ModeAcceptEdits, ModeAcceptEditsLive:
		if toolIsEdit(toolName) {
			// Edits outside the working directory and trusted roots still
			// prompt; read-only Read keeps its pre-existing allow.
			if !toolIsWrite(toolName) || underAny(cwd, toolInput, append([]string{cwd}, trustedRoots...)) {
				return DecisionAllow
			}
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

// MatchesAskRule reports whether an explicit ask rule matches the call. A
// PreToolUse hook's "allow" does not override one: the user asked to be
// prompted for exactly this.
func (g *Gate) MatchesAskRule(toolName, toolInput string) bool {
	g.mu.RLock()
	ask := g.ask
	g.mu.RUnlock()
	if len(ask) == 0 {
		return false
	}
	segments := bashSegments(toolName, toolInput)
	for _, rule := range ask {
		if matchRuleAnySegment(rule, toolName, toolInput, segments) {
			return true
		}
	}
	return false
}

// bashSegments returns the individual commands a Bash invocation would run.
// Returns nil for non-Bash tools and for input that does not parse, so callers
// fall back to matching the raw string rather than matching nothing.
func bashSegments(toolName, toolInput string) []string {
	if !strings.EqualFold(toolName, "Bash") {
		return nil
	}
	segs, ok := shellsafe.SplitForPermissions(toolInput)
	if !ok {
		return nil
	}
	return segs
}

// matchRuleAnySegment reports whether rule matches the raw input or any single
// command within it. Used for deny and ask, where one match is enough.
func matchRuleAnySegment(rule, toolName, toolInput string, segments []string) bool {
	if matchRule(rule, toolName, toolInput) {
		return true
	}
	for _, seg := range segments {
		if matchRule(rule, toolName, seg) {
			return true
		}
	}
	return false
}

// allowsInput reports whether the allow lists approve this call.
//
// For a compound Bash command every segment must be allowed. Approving the
// whole command because its first word matched a prefix rule is an escalation:
// `Bash(echo:*)` would otherwise auto-approve `echo hi && curl evil`.
//
// The one shortcut is a wildcard-free rule that spells out the entire command
// verbatim, so a deliberate rule like `Bash(git add -A && git commit)` keeps
// working. It is compared against the untouched input — not the StripLeadingCd
// form — because otherwise `Bash(make test)` would approve
// `cd $(curl evil) && make test`, whose substitution runs before make does.
func allowsInput(toolName, toolInput string, segments []string, lists ...[]string) bool {
	anyRule := func(f func(rule string) bool) bool {
		for _, list := range lists {
			for _, rule := range list {
				if f(rule) {
					return true
				}
			}
		}
		return false
	}

	// Non-Bash tools, unparseable input, and single commands keep the original
	// whole-string semantics — there is nothing to escalate across.
	if len(segments) <= 1 {
		return anyRule(func(rule string) bool { return matchRule(rule, toolName, toolInput) })
	}

	if anyRule(func(rule string) bool { return exactRuleMatchesWholeInput(rule, toolName, toolInput) }) {
		return true
	}
	for _, seg := range segments {
		if isBenignCd(seg) {
			continue
		}
		if !anyRule(func(rule string) bool { return matchRule(rule, toolName, seg) }) {
			return false
		}
	}
	return true
}

// exactRuleMatchesWholeInput reports whether a wildcard-free rule names the
// entire command verbatim.
func exactRuleMatchesWholeInput(rule, toolName, toolInput string) bool {
	paren := strings.IndexByte(rule, '(')
	if paren < 0 || !strings.HasSuffix(rule, ")") {
		return false
	}
	if !strings.EqualFold(rule[:paren], toolName) {
		return false
	}
	pattern := rule[paren+1 : len(rule)-1]
	if strings.Contains(pattern, "*") {
		return false
	}
	return pattern == toolInput
}

// isBenignCd reports whether a segment is a plain directory change, which
// matchRule already treats as normalization rather than an action (see
// shellsafe.StripLeadingCd). A cd whose argument contains a substitution is not
// benign — that substitution executes.
func isBenignCd(seg string) bool {
	if seg != "cd" && !strings.HasPrefix(seg, "cd ") {
		return false
	}
	return !strings.Contains(seg, "$(") && !strings.Contains(seg, "`") && !strings.Contains(seg, "<(")
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
			return shellsafe.IsReadOnly(toolInput)
		}
		if normalized, ok := shellsafe.StripLeadingCd(toolInput); ok {
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
		if shellsafe.IsReadOnly(toolInput) {
			return "Bash(readonly:*)"
		}
		if normalized, ok := shellsafe.StripLeadingCd(toolInput); ok {
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
