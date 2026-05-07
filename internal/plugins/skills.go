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

// NewSkillLoader builds a SkillLoader from loaded plugins and bundled skills.
func NewSkillLoader(ps []*Plugin) *SkillLoader {
	var cmds []CommandDef
	var pluginSkills []SkillDef
	for _, p := range ps {
		cmds = append(cmds, p.Commands...)
		pluginSkills = append(pluginSkills, p.Skills...)
	}
	return &SkillLoader{commands: cmds, pluginSkills: pluginSkills, bundled: skills.Bundled()}
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
