package agent

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
)

// isRawImplementTool reports whether name is an MCP implement tool
// (mcp__<server>__local_implement or any other *implement tool). Calling one
// directly would skip conduit's authorizer and write recording, so it is
// denied even when the server marks it read-only.
func isRawImplementTool(name string) bool {
	if !strings.HasPrefix(name, "mcp__") {
		return false
	}
	return strings.Contains(strings.ToLower(name[strings.LastIndex(name, "__")+2:]), "implement")
}

// implGuardDenial returns the denial message when the implement role forbids
// this tool call, or "" when it may proceed. It runs before the permission
// gate so no permission mode, bypassPermissions included, skips it.
//
// The guard covers code edits only: file-edit tools aimed at a file the
// implementer has not touched, and raw MCP implement tools. Everything else,
// Bash and other non-read-only tools included, is left to the normal
// permission gate, so the main model can still read, run commands and use git.
// The guard is inactive while the implementer is unreachable.
func (l *Loop) implGuardDenial(toolName string, input map[string]any, cwd string) string {
	active, label := l.cfg.ImplGuard.Active()
	if !active {
		return ""
	}
	if targets, isWrite := implWriteTargets(toolName, input, cwd); isWrite {
		return l.implGuardCheckTargets(toolName, label, targets, cwd)
	}
	if isRawImplementTool(toolName) {
		return fmt.Sprintf("%s denied: use LocalImplement (implement role %s); the raw implement tool skips conduit's permission checks and write recording.", toolName, label)
	}
	return ""
}

// implGuardFallbackNote returns the one-time notice for a file-edit call made
// while the implementer is unreachable, or "". It relies on implGuardDenial
// having just refreshed the guard's state for this call.
func (l *Loop) implGuardFallbackNote(toolName string, input map[string]any, cwd string) string {
	if _, isWrite := implWriteTargets(toolName, input, cwd); !isWrite {
		return ""
	}
	return l.cfg.ImplGuard.TakeFallbackNotice()
}

func (l *Loop) implGuardCheckTargets(toolName, label string, targets []string, cwd string) string {
	if len(targets) == 0 {
		targets = []string{""}
	}
	for _, p := range targets {
		abs := p
		if p != "" && !filepath.IsAbs(p) {
			abs = filepath.Join(cwdOrProcess(cwd), p)
		}
		// Conduit's own per-project storage (memory, session data) is picked
		// by conduit, never by the model, and holds no repo files.
		if p != "" && isConduitStorage(toolName, abs, cwdOrProcess(cwd)) {
			continue
		}
		if p == "" || !l.cfg.ImplGuard.Allowed(abs) {
			shown := p
			if shown == "" {
				shown = "the target path"
			}
			return fmt.Sprintf("%s denied: code edits go through LocalImplement (implement role %s). %s was not written by the implementer; use LocalImplement or CodingTask to change it. You may edit files the implementer already touched, and you may still read files, run commands and use git.", toolName, label, shown)
		}
	}
	return ""
}

// implWriteTargets returns the paths a file-writing tool call would change.
// isWrite is false for tools (and read-only modes of tools) that write
// nothing; a write whose target cannot be determined returns no paths, which
// the caller denies.
func implWriteTargets(toolName string, input map[string]any, cwd string) (paths []string, isWrite bool) {
	switch toolName {
	case "Write", "Edit", "MultiEdit", "HashEdit":
		p, _ := input["file_path"].(string)
		return nonEmpty(p), true
	case "NotebookEdit":
		p, _ := input["notebook_path"].(string)
		return nonEmpty(p), true
	case "AstGrep":
		if astGrepWriteTarget(input, cwd) == "" {
			return nil, false
		}
		if raw, ok := input["paths"].([]any); ok {
			for _, p := range raw {
				if s, ok := p.(string); ok && s != "" {
					paths = append(paths, s)
				}
			}
		}
		if len(paths) == 0 {
			paths = []string{"."}
		}
		return paths, true
	}
	return nil, false
}

func nonEmpty(p string) []string {
	if p == "" {
		return nil
	}
	return []string{p}
}

// isConduitStorage reports whether abs is inside conduit's own per-project
// storage after symlinks are resolved. A link from the storage directory into
// the repo resolves outside it and is not exempt.
func isConduitStorage(toolName, abs, cwd string) bool {
	if !permissions.IsImplicitlyTrustedPath(toolName, abs, cwd) {
		return false
	}
	resolved := resolveExisting(abs)
	for _, base := range []string{settings.ConduitDir(), settings.ClaudeDir()} {
		projects := filepath.Join(base, "projects")
		rel, err := filepath.Rel(projects, abs)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		project := filepath.Join(projects, strings.Split(rel, string(filepath.Separator))[0])
		root := resolveExisting(project)
		if resolved == root || strings.HasPrefix(resolved, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolveExisting resolves symlinks in p, or in its parent directory when p
// does not exist yet (the same rule implguard uses to compare paths).
func resolveExisting(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	dir, base := filepath.Split(p)
	if r, err := filepath.EvalSymlinks(filepath.Clean(dir)); err == nil {
		return filepath.Join(r, base)
	}
	return p
}
