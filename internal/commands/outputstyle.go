package commands

import (
	"fmt"
	"strings"

	"github.com/icehunter/conduit/internal/outputstyles"
)

// RegisterOutputStyleCommand adds /output-style to list and activate output styles.
func RegisterOutputStyleCommand(r *Registry, cwd string) {
	r.Register(Command{
		Name:        "output-style",
		Description: "List or activate an output style. Usage: /output-style [name]",
		Handler: func(args string) Result {
			styles, err := outputstyles.LoadAll(cwd)
			if err != nil {
				return Result{Type: "error", Text: fmt.Sprintf("output-style: %v", err)}
			}

			name := strings.TrimSpace(args)

			// No arg → list available styles.
			if name == "" {
				if len(styles) == 0 {
					return Result{Type: "text", Text: "No output styles found.\n\nPlace *.md files in .claude/output-styles/ or ~/.claude/output-styles/"}
				}
				var sb strings.Builder
				sb.WriteString("Available output styles:\n\n")
				for _, s := range styles {
					sb.WriteString(fmt.Sprintf("  %-20s %s\n", s.Name, s.Description))
				}
				sb.WriteString("\nUsage: /output-style <name>  to activate a style")
				return Result{Type: "text", Text: sb.String()}
			}

			// Find the requested style.
			var found *outputstyles.Style
			for i := range styles {
				if strings.EqualFold(styles[i].Name, name) {
					found = &styles[i]
					break
				}
			}
			if found == nil {
				return Result{Type: "error", Text: fmt.Sprintf("output-style: %q not found. Use /output-style to list available styles.", name)}
			}

			// Return as output-style result — the TUI/loop injects the prompt.
			return Result{Type: "output-style", Text: found.Prompt, Model: found.Name}
		},
	})
}
