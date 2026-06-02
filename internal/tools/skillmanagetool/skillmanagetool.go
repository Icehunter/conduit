// Package skillmanagetool implements the SkillManage tool — allows background
// review forks to create, view, update, list, and promote skill files.
//
// This tool is intentionally NOT registered in the default agent registry.
// It is only handed to background review sub-agents constructed by the
// bgreview package. Granting it to the main agent would allow unsolicited
// skill mutation, which is undesirable.
//
// Skill files are SKILL.md files under either:
//   - project scope:        <cwd>/.claude/skills/<name>/SKILL.md
//   - global scope:         ~/.claude/skills/<name>/SKILL.md
//   - global-conduit scope: ~/.conduit/skills/<name>/SKILL.md
//
// The tool mirrors the CC SkillLoader conventions — each skill lives in its
// own subdirectory whose name is the skill's canonical name.
package skillmanagetool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/skillusage"
	"github.com/icehunter/conduit/internal/tool"
)

const skillFileName = "SKILL.md"

// Option configures a Tool instance.
type Option func(*Tool)

// WithAgentProvenance marks skills created or promoted by this tool instance
// as agent-authored in the usage store. Without this option, skills are
// recorded as user-authored.
func WithAgentProvenance() Option {
	return func(t *Tool) { t.agentProvenance = true }
}

// Tool implements the SkillManage tool.
type Tool struct {
	tool.NotDeferrable
	// cwd is the working directory used to resolve project-scope skill paths.
	// When empty, os.Getwd() is called at execute time.
	cwd string
	// agentProvenance marks skills created/promoted by this instance as
	// agent-authored in the usage store.
	agentProvenance bool
}

// New returns a SkillManage tool.
// cwd is the project working directory; pass "" to resolve via os.Getwd().
func New(cwd string, opts ...Option) *Tool {
	t := &Tool{cwd: cwd}
	for _, o := range opts {
		o(t)
	}
	return t
}

// Name implements tool.Tool.
func (*Tool) Name() string { return "SkillManage" }

// Description implements tool.Tool.
func (*Tool) Description() string {
	return "Create, view, update, list, or promote skill files (SKILL.md) in project, global-conduit, or global scope. " +
		"Use action=\"list\" to see available skills, action=\"view\" to read one, " +
		"action=\"create\" to write a new skill, action=\"update\" to overwrite an existing one, " +
		"or action=\"promote\" to move a skill from project scope to global-conduit scope. " +
		"scope=\"project\" writes to <cwd>/.claude/skills/, scope=\"global-conduit\" writes to ~/.conduit/skills/, " +
		"scope=\"global\" writes to ~/.claude/skills/."
}

// InputSchema implements tool.Tool.
func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["create", "view", "update", "list", "promote"],
				"description": "The operation to perform."
			},
			"name": {
				"type": "string",
				"description": "Skill name (directory name). Required for create/view/update/promote."
			},
			"content": {
				"type": "string",
				"description": "Full SKILL.md content. Required for create and update."
			},
			"scope": {
				"type": "string",
				"enum": ["project", "global", "global-conduit"],
				"description": "Where to write the skill. \"project\" = <cwd>/.claude/skills/ (use ONLY for skills specific to this repo's file layout, build commands, or conventions). \"global-conduit\" = ~/.conduit/skills/ (use for general, reusable skills that work across projects — prefer this when unsure). \"global\" = ~/.claude/skills/ (Claude Code compatibility). Defaults to \"project\" if omitted."
			}
		},
		"required": ["action"]
	}`)
}

// IsReadOnly returns true only for list and view actions.
func (*Tool) IsReadOnly(raw json.RawMessage) bool {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return false
	}
	return in.Action == "list" || in.Action == "view"
}

// IsConcurrencySafe reports whether the operation is safe to run concurrently.
func (*Tool) IsConcurrencySafe(raw json.RawMessage) bool {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return false
	}
	return in.Action == "list" || in.Action == "view"
}

// Input is the typed view of the JSON input.
type Input struct {
	Action  string `json:"action"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content,omitempty"`
	Scope   string `json:"scope,omitempty"`
}

// Execute runs the requested action.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("skillmanagetool: invalid input: %v", err)), nil
	}

	select {
	case <-ctx.Done():
		return tool.ErrorResult("skillmanagetool: cancelled"), nil
	default:
	}

	switch in.Action {
	case "list":
		return t.list(ctx)
	case "view":
		if strings.TrimSpace(in.Name) == "" {
			return tool.ErrorResult("skillmanagetool: \"name\" is required for view"), nil
		}
		return t.view(ctx, in.Name)
	case "create":
		if strings.TrimSpace(in.Name) == "" {
			return tool.ErrorResult("skillmanagetool: \"name\" is required for create"), nil
		}
		if in.Content == "" {
			return tool.ErrorResult("skillmanagetool: \"content\" is required for create"), nil
		}
		return t.create(ctx, in.Name, in.Content, in.Scope)
	case "update":
		if strings.TrimSpace(in.Name) == "" {
			return tool.ErrorResult("skillmanagetool: \"name\" is required for update"), nil
		}
		if in.Content == "" {
			return tool.ErrorResult("skillmanagetool: \"content\" is required for update"), nil
		}
		return t.update(ctx, in.Name, in.Content, in.Scope)
	case "promote":
		if strings.TrimSpace(in.Name) == "" {
			return tool.ErrorResult("skillmanagetool: \"name\" is required for promote"), nil
		}
		return t.promote(ctx, in.Name)
	default:
		return tool.ErrorResult(fmt.Sprintf("skillmanagetool: unknown action %q (must be create|view|update|list|promote)", in.Action)), nil
	}
}

// projectSkillsDir returns the project-scoped skills directory.
func (t *Tool) projectSkillsDir() string {
	cwd := t.cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	return filepath.Join(cwd, ".claude", "skills")
}

// globalSkillsDir returns the global skills directory (~/.claude/skills/).
func globalSkillsDir() string {
	return filepath.Join(settings.ClaudeDir(), "skills")
}

// conduitGlobalSkillsDir returns the conduit global skills directory (~/.conduit/skills/).
func conduitGlobalSkillsDir() string {
	return filepath.Join(settings.ConduitDir(), "skills")
}

// scopeDir resolves the skill base directory for the given scope string.
// Defaults to project if scope is unrecognised or empty.
func (t *Tool) scopeDir(scope string) string {
	switch scope {
	case "global":
		return globalSkillsDir()
	case "global-conduit":
		return conduitGlobalSkillsDir()
	default:
		return t.projectSkillsDir()
	}
}

// list walks all skill directories and returns names + descriptions.
func (t *Tool) list(_ context.Context) (tool.Result, error) {
	type entry struct {
		name  string
		desc  string
		scope string
	}
	var entries []entry

	dirs := map[string]string{
		"global":         globalSkillsDir(),
		"global-conduit": conduitGlobalSkillsDir(),
		"project":        t.projectSkillsDir(),
	}
	for scope, dir := range dirs {
		des, err := os.ReadDir(dir)
		if err != nil {
			// Directory doesn't exist or is unreadable — skip silently.
			continue
		}
		for _, de := range des {
			if !de.IsDir() {
				continue
			}
			skillPath := filepath.Join(dir, de.Name(), skillFileName)
			desc := firstLine(skillPath)
			entries = append(entries, entry{name: de.Name(), desc: desc, scope: scope})
		}
	}

	if len(entries) == 0 {
		return tool.TextResult("No skills found in project (.claude/skills/), global-conduit (~/.conduit/skills/), or global (~/.claude/skills/) directories."), nil
	}

	var sb strings.Builder
	sb.WriteString("Available skills:\n\n")
	for _, e := range entries {
		if e.desc != "" {
			fmt.Fprintf(&sb, "  [%s] %s — %s\n", e.scope, e.name, e.desc)
		} else {
			fmt.Fprintf(&sb, "  [%s] %s\n", e.scope, e.name)
		}
	}
	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}

// view reads and returns the full SKILL.md content for the named skill.
// Searches project scope first, then global-conduit, then global.
func (t *Tool) view(_ context.Context, name string) (tool.Result, error) {
	for _, dir := range []string{t.projectSkillsDir(), conduitGlobalSkillsDir(), globalSkillsDir()} {
		path := filepath.Join(dir, name, skillFileName)
		data, err := os.ReadFile(path)
		if err == nil {
			// Best-effort telemetry.
			skillusage.BumpView(name)
			return tool.TextResult(string(data)), nil
		}
		if !os.IsNotExist(err) {
			return tool.ErrorResult(fmt.Sprintf("skillmanagetool: view: cannot read %s: %v", path, err)), nil
		}
	}
	return tool.ErrorResult(fmt.Sprintf("skillmanagetool: skill %q not found in project, global-conduit, or global scope", name)), nil
}

// create writes a new SKILL.md. Errors if the skill already exists.
func (t *Tool) create(_ context.Context, name, content, scope string) (tool.Result, error) {
	resolvedScope := scope
	if resolvedScope == "" {
		resolvedScope = "project"
	}
	dir := t.scopeDir(scope)
	skillDir := filepath.Join(dir, name)
	path := filepath.Join(skillDir, skillFileName)

	if _, err := os.Stat(path); err == nil {
		return tool.ErrorResult(fmt.Sprintf("skillmanagetool: skill %q already exists at %s; use action=\"update\" to overwrite", name, path)), nil
	}

	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return tool.ErrorResult(fmt.Sprintf("skillmanagetool: create: cannot create directory %s: %v", skillDir, err)), nil
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return tool.ErrorResult(fmt.Sprintf("skillmanagetool: create: cannot write %s: %v", path, err)), nil
	}

	// Best-effort telemetry.
	skillusage.RecordCreate(name, resolvedScope, t.agentProvenance)

	return tool.TextResult(fmt.Sprintf("Created skill %q at %s", name, path)), nil
}

// update overwrites an existing skill's SKILL.md. Errors if the skill does not exist.
func (t *Tool) update(_ context.Context, name, content, scope string) (tool.Result, error) {
	dir := t.scopeDir(scope)
	path := filepath.Join(dir, name, skillFileName)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return tool.ErrorResult(fmt.Sprintf("skillmanagetool: skill %q not found at %s; use action=\"create\" to create it", name, path)), nil
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return tool.ErrorResult(fmt.Sprintf("skillmanagetool: update: cannot write %s: %v", path, err)), nil
	}

	// Best-effort telemetry.
	skillusage.BumpPatch(name)

	return tool.TextResult(fmt.Sprintf("Updated skill %q at %s", name, path)), nil
}

// promote moves a skill from project scope to global-conduit scope.
func (t *Tool) promote(_ context.Context, name string) (tool.Result, error) {
	src := filepath.Join(t.projectSkillsDir(), name)
	dst := filepath.Join(conduitGlobalSkillsDir(), name)

	// Skill must exist in project scope.
	if _, err := os.Stat(filepath.Join(src, skillFileName)); os.IsNotExist(err) {
		return tool.ErrorResult(fmt.Sprintf("skillmanagetool: skill %q not found in project scope", name)), nil
	}
	// Must not already exist at destination.
	if _, err := os.Stat(dst); err == nil {
		return tool.ErrorResult(fmt.Sprintf("skillmanagetool: skill %q already exists at global-conduit scope %s; resolve conflict first", name, dst)), nil
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return tool.ErrorResult(fmt.Sprintf("skillmanagetool: promote: cannot create destination dir: %v", err)), nil
	}
	if err := os.Rename(src, dst); err != nil {
		return tool.ErrorResult(fmt.Sprintf("skillmanagetool: promote: rename failed: %v", err)), nil
	}

	// Best-effort telemetry — update scope on existing record or log if it fails.
	skillusage.UpdateScope(name, "global-conduit")

	return tool.TextResult(fmt.Sprintf("Promoted skill %q from project scope to global-conduit (%s)", name, dst)), nil
}

// firstLine reads the first non-empty line from path, typically the skill title.
func firstLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" {
			// Strip markdown heading prefix so the description is clean.
			line = strings.TrimLeft(line, "#")
			return strings.TrimSpace(line)
		}
	}
	return ""
}
