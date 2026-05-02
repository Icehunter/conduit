package plugins

import (
	"strings"

	"github.com/icehunter/conduit/internal/tools/skilltool"
)

// SkillLoader implements skilltool.Loader backed by a slice of loaded plugins.
type SkillLoader struct {
	commands []CommandDef
}

// NewSkillLoader builds a SkillLoader from a set of loaded plugins.
func NewSkillLoader(ps []*Plugin) *SkillLoader {
	var cmds []CommandDef
	for _, p := range ps {
		cmds = append(cmds, p.Commands...)
	}
	return &SkillLoader{commands: cmds}
}

// FindCommand looks up a command by name. Accepts:
//   - "pluginName:commandName" (qualified)
//   - "commandName" (bare — matches the first command with that base name)
//   - "/commandName" or "/pluginName:commandName" (leading slash stripped)
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
	return nil
}
