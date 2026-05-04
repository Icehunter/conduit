package commands

import (
	"fmt"
	"strings"

	"github.com/icehunter/conduit/internal/plugins"
)

// RegisterSkillsCommand registers /skills — lists available skills from installed plugins.
func RegisterSkillsCommand(r *Registry, ps []*plugins.Plugin) {
	r.Register(Command{
		Name:        "skills",
		Description: "List available skills from installed plugins",
		Handler: func(args string) Result {
			if len(ps) == 0 {
				return Result{Type: "text", Text: "No plugins installed. Use /plugin to browse and install plugins."}
			}
			var sb strings.Builder
			fmt.Fprintf(&sb, "Available skills (%d installed plugins):\n\n", len(ps))
			total := 0
			for _, p := range ps {
				if len(p.Commands) == 0 {
					continue
				}
				fmt.Fprintf(&sb, "**%s** (%d skill%s):\n", p.Manifest.Name, len(p.Commands), pluralS(len(p.Commands)))
				for _, cmd := range p.Commands {
					desc := cmd.Description
					if len([]rune(desc)) > 70 {
						desc = string([]rune(desc)[:69]) + "…"
					}
					fmt.Fprintf(&sb, "  /%s", cmd.QualifiedName)
					if desc != "" {
						sb.WriteString(" — " + desc)
					}
					sb.WriteByte('\n')
					total++
				}
				sb.WriteByte('\n')
			}
			fmt.Fprintf(&sb, "Total: %d skills. The model will use these automatically when relevant.", total)
			return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
		},
	})
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
