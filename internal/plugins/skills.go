package plugins

import (
	"strings"

	"github.com/icehunter/conduit/internal/skills"
	"github.com/icehunter/conduit/internal/tools/skilltool"
)

// SkillLoader implements skilltool.Loader backed by loaded plugins plus
// built-in bundled skills.
type SkillLoader struct {
	commands     []CommandDef
	pluginSkills []SkillDef
	bundled      []skilltool.Command
}

// NewSkillLoader builds a SkillLoader from loaded plugins, FS-discovered skills,
// and bundled skills. cwd is the working directory used for FS skill discovery;
// pass "" to skip the project-local ~/.claude/skills search path.
func NewSkillLoader(ps []*Plugin, cwd string) *SkillLoader {
	var cmds []CommandDef
	var pluginSkills []SkillDef
	for _, p := range ps {
		cmds = append(cmds, p.Commands...)
		pluginSkills = append(pluginSkills, p.Skills...)
	}
	bundled := skills.Bundled()
	// Merge FS-discovered skills. Plugin skills take priority over FS skills so
	// plugins can override user-local skill definitions. FS skills override bundled.
	fsSkills := skills.LoadFS(cwd)
	bundled = mergeFSIntoBundled(fsSkills, bundled)
	return &SkillLoader{commands: cmds, pluginSkills: pluginSkills, bundled: bundled}
}

// mergeFSIntoBundled merges FS-discovered skills into the bundled list.
// FS skills with the same QualifiedName as a bundled skill replace it; new
// names are prepended so FS skills appear before the built-ins in listings.
func mergeFSIntoBundled(fs []skilltool.Command, bundled []skilltool.Command) []skilltool.Command {
	if len(fs) == 0 {
		return bundled
	}
	// Index bundled by name for fast lookup.
	idx := make(map[string]int, len(bundled))
	for i, b := range bundled {
		idx[strings.ToLower(b.QualifiedName)] = i
	}
	out := make([]skilltool.Command, len(bundled))
	copy(out, bundled)
	var prepend []skilltool.Command
	for _, fsk := range fs {
		key := strings.ToLower(fsk.QualifiedName)
		if i, exists := idx[key]; exists {
			out[i] = fsk // override in-place
		} else {
			prepend = append(prepend, fsk)
		}
	}
	if len(prepend) > 0 {
		out = append(prepend, out...)
	}
	return out
}

// FindCommand looks up a command by name. Accepts:
//   - "pluginName:commandName" (qualified)
//   - "commandName" (bare — matches the first command with that base name)
//   - "/commandName" or "/pluginName:commandName" (leading slash stripped)
//
// Search order: plugin commands → plugin skills → bundled skills.
func (l *SkillLoader) FindCommand(name string) *skilltool.Command {
	name = strings.TrimPrefix(name, "/")
	for _, cmd := range l.commands {
		if strings.EqualFold(cmd.QualifiedName, name) || strings.EqualFold(cmd.Name, name) {
			return &skilltool.Command{
				QualifiedName: cmd.QualifiedName,
				Description:   cmd.Description,
				Body:          cmd.Body,
				Tools:         cmd.AllowedTools,
			}
		}
	}
	for _, sk := range l.pluginSkills {
		if strings.EqualFold(sk.QualifiedName, name) || strings.EqualFold(sk.Name, name) {
			return &skilltool.Command{
				QualifiedName: sk.QualifiedName,
				Description:   sk.Description,
				Body:          sk.Body,
				Tools:         sk.Tools,
			}
		}
	}
	// Fall back to bundled skills.
	for i := range l.bundled {
		if strings.EqualFold(l.bundled[i].QualifiedName, name) {
			return &l.bundled[i]
		}
	}
	return nil
}

// BundledCommands returns the built-in skills for system prompt listing.
func (l *SkillLoader) BundledCommands() []skilltool.Command {
	return l.bundled
}

// PluginSkills returns the plugin-sourced skills for system prompt listing.
func (l *SkillLoader) PluginSkills() []skilltool.Command {
	out := make([]skilltool.Command, len(l.pluginSkills))
	for i, sk := range l.pluginSkills {
		out[i] = skilltool.Command{
			QualifiedName: sk.QualifiedName,
			Description:   sk.Description,
			Body:          sk.Body,
			Tools:         sk.Tools,
		}
	}
	return out
}
