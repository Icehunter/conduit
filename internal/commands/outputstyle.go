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

			// No arg → open picker. The picker dispatches `/output-style <name>`
			// on Enter, which falls into the lookup branch below.
			if name == "" {
				if len(styles) == 0 {
					return Result{Type: "text", Text: "No output styles found.\n\nPlace *.md files in .claude/output-styles/ or ~/.claude/output-styles/"}
				}
				values := make([]string, len(styles))
				labels := make([]string, len(styles))
				for i, s := range styles {
					values[i] = s.Name
					labels[i] = s.Name
					if s.Description != "" {
						labels[i] = fmt.Sprintf("%-20s %s", s.Name, s.Description)
					}
				}
				return pickerResult("output-style", "Pick an output style", "", values, labels)
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
