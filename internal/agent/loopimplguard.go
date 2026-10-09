package agent

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/shellsafe"
	"github.com/icehunter/conduit/internal/tool"
)

// implGuardPassthrough lists tools that are not read-only by their own
// IsReadOnly but change no project file, so the guard lets them run. Every
// other tool that is not read-only is denied, which covers MCP write tools,
// EnterWorktree/ExitWorktree (a worktree escapes the recorded paths),
// SkillManage writes, REPL, Workflow, Config writes, MCP authenticate and any
// tool added later.
//
// Decisions:
//   - Task: sub-agents share the guard, so they are bound by it too.
//   - TaskCreate, TaskUpdate, TaskOutput, TaskStop, TodoWrite: in-memory task
//     bookkeeping; TaskStop only kills a background job.
//   - KillShell, KillBash: legacy names of TaskStop; they only stop a process
//     conduit started.
//   - RecordDecision: appends to the user-level decision log under ~/.conduit.
//   - AskUserQuestion, EnterPlanMode, ExitPlanMode, SendMessage: pure
//     conversation or session state.
//   - LocalImplement, CodingTask: the implementer itself.
var implGuardPassthrough = map[string]bool{
	"Task": true, "TaskCreate": true, "TaskUpdate": true, "TaskOutput": true, "TaskStop": true,
	"TodoWrite": true, "RecordDecision": true, "KillShell": true, "KillBash": true,
	"AskUserQuestion": true, "EnterPlanMode": true, "ExitPlanMode": true, "SendMessage": true,
	"LocalImplement": true, "CodingTask": true,
}

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
// It is an allowlist: a tool that is not read-only is denied unless it is
// listed in implGuardPassthrough, is an edit tool aimed at a file the
// implementer recorded, or is a Bash/Shell command that
// shellsafe.AllowedUnderImplGuard accepts.
func (l *Loop) implGuardDenial(t tool.Tool, toolName string, rawInput json.RawMessage, input map[string]any, cwd string) string {
	active, label := l.cfg.ImplGuard.Active()
	if !active {
		return ""
	}
	switch toolName {
	case "Bash", "Shell":
		cmd, _ := input["command"].(string)
		allowed := shellsafe.AllowedUnderImplGuard(cmd)
		if toolName == "Shell" {
			allowed = shellsafe.AllowedUnderImplGuardPowerShell(cmd)
		}
		if allowed {
			return ""
		}
		return fmt.Sprintf("%s denied: the implement role (%s) owns code changes and this command is not a read-only command, test, build or lint run. Use LocalImplement or CodingTask for changes.", toolName, label)
	}
	targets, isWrite := implWriteTargets(toolName, input, cwd)
	if isWrite {
		return l.implGuardCheckTargets(toolName, label, targets, cwd)
	}
	switch {
	case toolName == "AstGrep":
		return "" // not a write per implWriteTargets: a search or dry-run preview
	case implGuardPassthrough[toolName]:
		return ""
	case isRawImplementTool(toolName):
	case t.IsReadOnly(rawInput):
		return ""
	}
	return fmt.Sprintf("%s denied: the implement role (%s) owns file changes; this tool can modify files outside the implementer. Use LocalImplement or CodingTask.", toolName, label)
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
			return fmt.Sprintf("%s denied: the implement role (%s) owns code changes. %s was not written by the implementer. Use LocalImplement or CodingTask to change it; you may only edit files the implementer already touched.", toolName, label, shown)
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
