// Package skillmanagetool implements the SkillManage tool — allows background
// review forks to create, view, update, patch, list, and promote skill files,
// write references/ files, and inspect or roll back recorded edits.
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
	"time"

	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/skills/skilllint"
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

// WithActor sets the ledger actor credited for writes made by this tool
// instance when the call context carries none. The default is "agent" for
// agent-provenance instances and "user" otherwise.
func WithActor(actor string) Option {
	return func(t *Tool) { t.actor = actor }
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
	// actor is the fallback ledger actor; see WithActor.
	actor string
}

// New returns a SkillManage tool.
// cwd is the project working directory; pass "" to resolve via os.Getwd().
func New(cwd string, opts ...Option) *Tool {
	t := &Tool{cwd: cwd}
	for _, o := range opts {
		o(t)
	}
	if t.actor == "" {
		t.actor = skillusage.ActorUser
		if t.agentProvenance {
			t.actor = skillusage.ActorAgent
		}
	}
	return t
}

// Name implements tool.Tool.
func (*Tool) Name() string { return "SkillManage" }

// Description implements tool.Tool.
func (*Tool) Description() string {
	return "Create, view, update, patch, list, or promote skill files (SKILL.md) in project, global-conduit, or global scope. " +
		"Use action=\"list\" to see available skills, action=\"view\" to read one, " +
		"action=\"create\" to write a new skill, action=\"update\" to overwrite SKILL.md wholesale, " +
		"action=\"patch\" to replace one string (old_string/new_string) or one markdown section (section/new_string) without rewriting the file, " +
		"action=\"write_file\" / action=\"remove_file\" to manage supporting files under references/, " +
		"action=\"promote\" to move a skill from project scope to global-conduit scope, " +
		"action=\"history\" to list recorded edits and action=\"rollback\" (id) to undo one. " +
		"SKILL.md must start with YAML frontmatter containing name and description; writes that break that are rejected, and authoring-convention warnings are returned with the result. " +
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
				"enum": ["create", "view", "update", "patch", "write_file", "remove_file", "list", "promote", "history", "rollback"],
				"description": "The operation to perform."
			},
			"name": {
				"type": "string",
				"description": "Skill name (directory name). Required for create/view/update/patch/write_file/remove_file/promote; optional filter for history."
			},
			"content": {
				"type": "string",
				"description": "Full SKILL.md content for create and update; file content for write_file."
			},
			"old_string": {
				"type": "string",
				"description": "patch: exact text to replace. Must match once unless replace_all is true."
			},
			"new_string": {
				"type": "string",
				"description": "patch: replacement text (may be empty to delete). With section, the new body of that section."
			},
			"replace_all": {
				"type": "boolean",
				"description": "patch: replace every occurrence of old_string."
			},
			"section": {
				"type": "string",
				"description": "patch: heading text (without leading #) of the markdown section whose body new_string replaces. Use instead of old_string."
			},
			"file_path": {
				"type": "string",
				"description": "Skill-relative file: references/<file> for write_file/remove_file; for patch, SKILL.md (default) or a references/ file."
			},
			"id": {
				"type": "string",
				"description": "rollback: ledger entry id from action=history."
			},
			"scope": {
				"type": "string",
				"enum": ["project", "global", "global-conduit"],
				"description": "Where to write the skill. \"project\" = <cwd>/.claude/skills/ (use ONLY for skills specific to this repo's file layout, build commands, or conventions). \"global-conduit\" = ~/.conduit/skills/ (use for general, reusable skills that work across projects — prefer this when unsure). \"global\" = ~/.claude/skills/ (Claude Code compatibility). create defaults to \"project\"; other writing actions search project then global-conduit when omitted (an error if both have the name) and need scope=\"global\" for ~/.claude/skills."
			}
		},
		"required": ["action"]
	}`)
}

// IsReadOnly returns true only for list, view and history actions.
func (*Tool) IsReadOnly(raw json.RawMessage) bool {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return false
	}
	return readOnlyAction(in.Action)
}

// IsConcurrencySafe reports whether the operation is safe to run concurrently.
func (*Tool) IsConcurrencySafe(raw json.RawMessage) bool {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return false
	}
	return readOnlyAction(in.Action)
}

func readOnlyAction(a string) bool { return a == "list" || a == "view" || a == "history" }

// Input is the typed view of the JSON input.
type Input struct {
	Action     string  `json:"action"`
	Name       string  `json:"name,omitempty"`
	Content    string  `json:"content,omitempty"`
	Scope      string  `json:"scope,omitempty"`
	OldString  string  `json:"old_string,omitempty"`
	NewString  *string `json:"new_string,omitempty"`
	ReplaceAll bool    `json:"replace_all,omitempty"`
	Section    string  `json:"section,omitempty"`
	FilePath   string  `json:"file_path,omitempty"`
	ID         string  `json:"id,omitempty"`
}

func errf(format string, a ...any) (tool.Result, error) {
	return tool.ErrorResult("skillmanagetool: " + fmt.Sprintf(format, a...)), nil
}

// Execute runs the requested action.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return errf("invalid input: %v", err)
	}

	select {
	case <-ctx.Done():
		return errf("cancelled")
	default:
	}

	needsName := map[string]bool{"view": true, "create": true, "update": true, "patch": true, "write_file": true, "remove_file": true, "promote": true}
	if needsName[in.Action] {
		if strings.TrimSpace(in.Name) == "" {
			return errf("\"name\" is required for %s", in.Action)
		}
		if err := skilllint.ValidName(in.Name); err != nil {
			return errf("%v", err)
		}
	}
	actor := skillusage.ActorFrom(ctx, t.actor)

	switch in.Action {
	case "list":
		return t.list(ctx)
	case "view":
		return t.view(ctx, in.Name)
	case "create":
		if in.Content == "" {
			return errf("\"content\" is required for create")
		}
		return t.create(actor, in.Name, in.Content, in.Scope)
	case "update":
		if in.Content == "" {
			return errf("\"content\" is required for update")
		}
		return t.update(actor, in.Name, in.Content, in.Scope)
	case "patch":
		return t.patch(actor, in)
	case "write_file":
		if in.Content == "" {
			return errf("\"content\" is required for write_file")
		}
		return t.writeFile(actor, in)
	case "remove_file":
		return t.removeFile(actor, in)
	case "promote":
		return t.promote(actor, in.Name)
	case "history":
		return t.history(in.Name)
	case "rollback":
		if strings.TrimSpace(in.ID) == "" {
			return errf("\"id\" is required for rollback (see action=history)")
		}
		return t.rollback(actor, strings.TrimSpace(in.ID))
	default:
		return errf("unknown action %q (must be create|view|update|patch|write_file|remove_file|list|promote|history|rollback)", in.Action)
	}
}

// TargetPath returns the SKILL.md path an action would write, for permission
// checks. cwd is the project directory ("" means the process working
// directory). Read-only and unknown actions return "". Promote returns its
// destination, the path it creates.
func TargetPath(cwd, action, name, scope string) string {
	t := &Tool{cwd: cwd}
	switch action {
	case "create", "update":
		return filepath.Join(t.scopeDir(scope), name, skillFileName)
	case "promote":
		return filepath.Join(conduitGlobalSkillsDir(), name, skillFileName)
	}
	return ""
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
			if !de.IsDir() || strings.HasPrefix(de.Name(), ".") {
				continue
			}
			skillPath := filepath.Join(dir, de.Name(), skillFileName)
			entries = append(entries, entry{name: de.Name(), desc: skillDescription(skillPath), scope: scope})
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

// skillDescription returns the frontmatter description of the SKILL.md at
// path, falling back to its first non-empty line.
func skillDescription(path string) string {
	if data, err := os.ReadFile(path); err == nil { //nolint:gosec // skill path under a skills dir
		if fm, _, ok := skilllint.Parse(string(data)); ok && fm["description"] != "" {
			return fm["description"]
		}
	}
	return firstLine(path)
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
			return errf("view: cannot read %s: %v", path, err)
		}
	}
	return errf("skill %q not found in project, global-conduit, or global scope", name)
}

// locate resolves the directory of an existing skill for a mutating action.
// With an explicit scope only that scope is searched. Without one, project and
// global-conduit are searched, and a name present in both is an error: the
// caller must say which. The Claude-compatible global scope is never searched
// implicitly; it needs scope="global".
func (t *Tool) locate(name, scope string) (string, error) {
	has := func(dir string) bool {
		fi, err := os.Stat(filepath.Join(dir, name, skillFileName))
		return err == nil && !fi.IsDir()
	}
	if scope != "" {
		if d := t.scopeDir(scope); has(d) {
			return filepath.Join(d, name), nil
		}
		return "", fmt.Errorf("skill %q not found in scope %q", name, scope)
	}
	var found []string
	var scopes []string
	for _, c := range []struct{ scope, dir string }{{"project", t.projectSkillsDir()}, {"global-conduit", conduitGlobalSkillsDir()}} {
		if has(c.dir) {
			found = append(found, filepath.Join(c.dir, name))
			scopes = append(scopes, c.scope)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("skill %q not found in project or global-conduit scope (pass scope=\"global\" for ~/.claude/skills)", name)
	case 1:
		return found[0], nil
	}
	return "", fmt.Errorf("skill %q exists in several scopes (%s); pass scope to choose one", name, strings.Join(scopes, ", "))
}

// lintRules returns the rules currently flagged for the skill in dir.
func lintRules(dir string) map[string]bool {
	rules := map[string]bool{}
	if data, err := os.ReadFile(filepath.Join(dir, skillFileName)); err == nil { //nolint:gosec // skill path under a skills dir
		for _, f := range skilllint.Lint(string(data), dir) {
			rules[f.Rule] = true
		}
	}
	return rules
}

// introducedWarnings lints the skill in dir and reports only findings whose
// rule was not already flagged in before, so a write reports what it caused
// rather than the skill's standing issues.
func introducedWarnings(dir string, before map[string]bool) string {
	data, err := os.ReadFile(filepath.Join(dir, skillFileName)) //nolint:gosec // skill path under a skills dir
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for _, f := range skilllint.Lint(string(data), dir) {
		if !before[f.Rule] {
			fmt.Fprintf(&sb, "\n  - %s", f)
		}
	}
	if sb.Len() == 0 {
		return ""
	}
	return "\n\nLint warnings (advisory, the write succeeded):" + sb.String()
}

// writeSkillFile writes data to path (creating parents) inside the ledger.
func writeSkillFile(actor, op, name, path string, data []byte) error {
	return skillusage.TrackEdit(actor, op, name, path, func() error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("cannot create directory %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fmt.Errorf("cannot write %s: %w", path, err)
		}
		return nil
	})
}

// create writes a new SKILL.md. Errors if the skill already exists.
func (t *Tool) create(actor, name, content, scope string) (tool.Result, error) {
	resolvedScope := scope
	if resolvedScope == "" {
		resolvedScope = "project"
	}
	dir := t.scopeDir(scope)
	skillDir := filepath.Join(dir, name)
	path := filepath.Join(skillDir, skillFileName)

	if err := skilllint.Validate(content); err != nil {
		return errf("create: %v", err)
	}
	if _, err := os.Stat(path); err == nil {
		return errf("skill %q already exists at %s; use action=\"update\" to overwrite", name, path)
	}
	before := lintRules(skillDir)
	if err := writeSkillFile(actor, skillusage.OpCreate, name, path, []byte(content)); err != nil {
		return errf("create: %v", err)
	}

	// Best-effort telemetry.
	skillusage.RecordCreate(name, resolvedScope, t.agentProvenance)

	return tool.TextResult(fmt.Sprintf("Created skill %q at %s%s", name, path, introducedWarnings(skillDir, before))), nil
}

// update overwrites an existing skill's SKILL.md. Errors if the skill does not exist.
func (t *Tool) update(actor, name, content, scope string) (tool.Result, error) {
	skillDir, err := t.locate(name, scope)
	if err != nil {
		return errf("update: %v; use action=\"create\" to create a new skill", err)
	}
	path := filepath.Join(skillDir, skillFileName)
	if err := skilllint.Validate(content); err != nil {
		return errf("update: %v", err)
	}
	before := lintRules(skillDir)
	if err := writeSkillFile(actor, skillusage.OpUpdate, name, path, []byte(content)); err != nil {
		return errf("update: %v", err)
	}

	// Best-effort telemetry.
	skillusage.BumpPatch(name)

	return tool.TextResult(fmt.Sprintf("Updated skill %q at %s%s", name, path, introducedWarnings(skillDir, before))), nil
}

// resolveFile maps a skill-relative file_path (default SKILL.md) to an absolute
// path, enforcing that anything other than SKILL.md lives under references/.
func resolveFile(skillDir, rel string) (string, error) {
	if rel == "" || rel == skillFileName {
		return filepath.Join(skillDir, skillFileName), nil
	}
	if err := skilllint.ValidSupportPath(rel); err != nil {
		return "", err
	}
	return filepath.Join(skillDir, filepath.FromSlash(strings.TrimPrefix(filepath.ToSlash(rel), "./"))), nil
}

// patch edits an existing skill file in place: replace a string or a markdown
// section instead of resending the whole file.
func (t *Tool) patch(actor string, in Input) (tool.Result, error) {
	if in.NewString == nil {
		return errf("\"new_string\" is required for patch")
	}
	if (in.OldString == "") == (in.Section == "") {
		return errf("patch needs exactly one of \"old_string\" or \"section\"")
	}
	skillDir, err := t.locate(in.Name, in.Scope)
	if err != nil {
		return errf("patch: %v", err)
	}
	path, err := resolveFile(skillDir, in.FilePath)
	if err != nil {
		return errf("patch: %v", err)
	}
	data, err := os.ReadFile(path) //nolint:gosec // path confined to the skill dir by resolveFile
	if err != nil {
		return errf("patch: cannot read %s: %v", path, err)
	}
	var updated string
	if in.OldString != "" {
		updated, err = patchString(string(data), in.OldString, *in.NewString, in.ReplaceAll)
	} else {
		updated, err = patchSection(string(data), in.Section, *in.NewString)
	}
	if err != nil {
		return errf("patch: %v", err)
	}
	if updated == string(data) {
		return errf("patch: replacement leaves %s unchanged", filepath.Base(path))
	}
	isSkillMD := filepath.Base(path) == skillFileName && filepath.Dir(path) == skillDir
	if isSkillMD {
		if err := skilllint.Validate(updated); err != nil {
			return errf("patch: result would be invalid, nothing written: %v", err)
		}
	} else if len(updated) > skilllint.MaxSupportFileBytes {
		return errf("patch: result is %d bytes (limit %d), nothing written", len(updated), skilllint.MaxSupportFileBytes)
	}
	before := lintRules(skillDir)
	if err := writeSkillFile(actor, skillusage.OpPatch, in.Name, path, []byte(updated)); err != nil {
		return errf("patch: %v", err)
	}
	skillusage.BumpPatch(in.Name)
	return tool.TextResult(fmt.Sprintf("Patched %s in skill %q%s", filepath.Base(path), in.Name, introducedWarnings(skillDir, before))), nil
}

// patchString replaces old with repl in text. It fails, with a hint the model
// can act on, when old is absent or ambiguous.
func patchString(text, old, repl string, all bool) (string, error) {
	n := strings.Count(text, old)
	switch {
	case n == 0:
		hint := ""
		for _, l := range strings.Split(old, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				if strings.Contains(text, l) {
					hint = fmt.Sprintf(" (the line %q exists, so a surrounding line or whitespace differs)", truncate(l, 80))
				}
				break
			}
		}
		return "", fmt.Errorf("old_string not found%s; use action=\"view\" and copy the exact text", hint)
	case n > 1 && !all:
		return "", fmt.Errorf("old_string matches %d times; add surrounding context to make it unique or set replace_all", n)
	}
	if all {
		return strings.ReplaceAll(text, old, repl), nil
	}
	return strings.Replace(text, old, repl, 1), nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}

type heading struct {
	level int
	text  string
	line  int // index into lines
}

// markdownHeadings lists ATX headings outside fenced code blocks.
func markdownHeadings(lines []string) []heading {
	var hs []heading
	fence := false
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "```") {
			fence = !fence
			continue
		}
		if fence || !strings.HasPrefix(l, "#") {
			continue
		}
		level := len(l) - len(strings.TrimLeft(l, "#"))
		if level > 6 || len(l) == level || l[level] != ' ' {
			continue
		}
		hs = append(hs, heading{level, strings.TrimSpace(l[level:]), i})
	}
	return hs
}

// patchSection replaces the body of the markdown section titled title (up to
// the next heading of the same or higher level) with body.
func patchSection(text, title, body string) (string, error) {
	title = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(title), "#"))
	lines := strings.Split(text, "\n")
	hs := markdownHeadings(lines)
	idx := -1
	for i, h := range hs {
		if strings.EqualFold(h.text, title) {
			if idx >= 0 {
				return "", fmt.Errorf("section %q is ambiguous (multiple headings match); use old_string instead", title)
			}
			idx = i
		}
	}
	if idx < 0 {
		names := make([]string, 0, len(hs))
		for _, h := range hs {
			names = append(names, h.text)
		}
		return "", fmt.Errorf("section %q not found; headings are: %s", title, strings.Join(names, ", "))
	}
	end := len(lines)
	for _, h := range hs[idx+1:] {
		if h.level <= hs[idx].level {
			end = h.line
			break
		}
	}
	out := append([]string{}, lines[:hs[idx].line+1]...)
	if b := strings.Trim(body, "\n"); b != "" {
		out = append(out, strings.Split(b, "\n")...)
	}
	if end < len(lines) || strings.HasSuffix(text, "\n") {
		out = append(out, "")
	}
	out = append(out, lines[end:]...)
	return strings.Join(out, "\n"), nil
}

// writeFile writes a supporting file under references/.
func (t *Tool) writeFile(actor string, in Input) (tool.Result, error) {
	if err := skilllint.ValidSupportPath(in.FilePath); err != nil {
		return errf("write_file: %v", err)
	}
	if len(in.Content) > skilllint.MaxSupportFileBytes {
		return errf("write_file: content is %d bytes (limit %d)", len(in.Content), skilllint.MaxSupportFileBytes)
	}
	skillDir, err := t.locate(in.Name, in.Scope)
	if err != nil {
		return errf("write_file: %v", err)
	}
	path, err := resolveFile(skillDir, in.FilePath)
	if err != nil {
		return errf("write_file: %v", err)
	}
	if fi, err := os.Lstat(path); err == nil && !fi.Mode().IsRegular() {
		return errf("write_file: %s exists and is not a regular file", in.FilePath)
	}
	before := lintRules(skillDir)
	if err := writeSkillFile(actor, skillusage.OpWriteFile, in.Name, path, []byte(in.Content)); err != nil {
		return errf("write_file: %v", err)
	}
	skillusage.BumpPatch(in.Name)
	return tool.TextResult(fmt.Sprintf("Wrote %s in skill %q%s", filepath.ToSlash(in.FilePath), in.Name, introducedWarnings(skillDir, before))), nil
}

// removeFile deletes a supporting file under references/.
func (t *Tool) removeFile(actor string, in Input) (tool.Result, error) {
	if err := skilllint.ValidSupportPath(in.FilePath); err != nil {
		return errf("remove_file: %v", err)
	}
	skillDir, err := t.locate(in.Name, in.Scope)
	if err != nil {
		return errf("remove_file: %v", err)
	}
	path, err := resolveFile(skillDir, in.FilePath)
	if err != nil {
		return errf("remove_file: %v", err)
	}
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		return errf("remove_file: %s is not an existing file in skill %q", in.FilePath, in.Name)
	}
	err = skillusage.TrackEdit(actor, skillusage.OpRemoveFile, in.Name, path, func() error { return os.Remove(path) })
	if err != nil {
		return errf("remove_file: %v", err)
	}
	skillusage.BumpPatch(in.Name)
	return tool.TextResult(fmt.Sprintf("Removed %s from skill %q", filepath.ToSlash(in.FilePath), in.Name)), nil
}

// promote moves a skill from project scope to global-conduit scope.
func (t *Tool) promote(actor, name string) (tool.Result, error) {
	src := filepath.Join(t.projectSkillsDir(), name)
	dst := filepath.Join(conduitGlobalSkillsDir(), name)

	// Skill must exist in project scope.
	if _, err := os.Stat(filepath.Join(src, skillFileName)); os.IsNotExist(err) {
		return errf("skill %q not found in project scope", name)
	}
	// Must not already exist at destination.
	if _, err := os.Stat(dst); err == nil {
		return errf("skill %q already exists at global-conduit scope %s; resolve conflict first", name, dst)
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return errf("promote: cannot create destination dir: %v", err)
	}
	if err := os.Rename(src, dst); err != nil {
		return errf("promote: rename failed: %v", err)
	}

	// Best-effort telemetry — update scope on existing record or log if it fails.
	skillusage.UpdateScope(name, "global-conduit")
	skillusage.RecordInfo(actor, skillusage.OpPromote, name, dst, "moved from "+src)

	return tool.TextResult(fmt.Sprintf("Promoted skill %q from project scope to global-conduit (%s)", name, dst)), nil
}

const historyLimit = 20

// history lists recent recorded edits, newest first, optionally for one skill.
func (t *Tool) history(name string) (tool.Result, error) {
	entries := skillusage.LedgerList(name, historyLimit)
	if len(entries) == 0 {
		return tool.TextResult("No recorded skill edits."), nil
	}
	var sb strings.Builder
	sb.WriteString("Recent skill edits (newest first; roll back one with action=\"rollback\" id=<id>):\n\n")
	for _, e := range entries {
		rb := ""
		if e.Reversible {
			rb = " [reversible]"
		}
		fmt.Fprintf(&sb, "  %s  %s  %-8s %-11s %s%s\n", e.ID, e.Time.Local().Format(time.DateTime), e.Actor, e.Op, e.Skill, rb)
	}
	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}

// rollback undoes one recorded edit.
func (t *Tool) rollback(actor, id string) (tool.Result, error) {
	e, err := skillusage.LedgerRollback(id, actor)
	if err != nil {
		return errf("rollback: %v", err)
	}
	return tool.TextResult(fmt.Sprintf("Rolled back edit %s (%s %s by %s) on %s", e.ID, e.Op, e.Skill, e.Actor, e.Path)), nil
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
