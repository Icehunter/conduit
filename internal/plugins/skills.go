package plugins

import (
	"strings"

	"github.com/icehunter/conduit/internal/skills"
	"github.com/icehunter/conduit/internal/tools/skilltool"
)

// SkillLoader implements skilltool.Loader backed by loaded plugins plus
// built-in bundled skills.
type SkillLoader struct {
	commands []CommandDef
	bundled  []skilltool.Command
}

// NewSkillLoader builds a SkillLoader from loaded plugins and bundled skills.
func NewSkillLoader(ps []*Plugin) *SkillLoader {
	var cmds []CommandDef
	for _, p := range ps {
		cmds = append(cmds, p.Commands...)
	}
	return &SkillLoader{commands: cmds, bundled: skills.Bundled()}
}

// FindCommand looks up a command by name. Accepts:
//   - "pluginName:commandName" (qualified)
//   - "commandName" (bare — matches the first command with that base name)
//   - "/commandName" or "/pluginName:commandName" (leading slash stripped)
//
// Plugin commands take precedence over bundled skills of the same name.
func (l *SkillLoader) FindCommand(name string) *skilltool.Command {
	name = strings.TrimPrefix(name, "/")
	for _, cmd := range l.commands {
		if strings.EqualFold(cmd.QualifiedName, name) || strings.EqualFold(cmd.Name, name) {
			return &skilltool.Command{
				QualifiedName: cmd.QualifiedName,
				Body:          cmd.Body,
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
