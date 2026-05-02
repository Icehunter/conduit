package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/icehunter/claude-go/internal/agent"
)

// Run starts the inline TUI (no alt-screen). Messages print into the normal
// scrollback; only the input box and status line re-render at the bottom.
// On exit a summary line is left in the terminal history.
func Run(version, modelName string, loop *agent.Loop) error {
	var prog *tea.Program

	cfg := Config{
		Version:   version,
		ModelName: modelName,
		Loop:      loop,
		Program:   &prog,
	}

	m := New(cfg)
	// No WithAltScreen — inline rendering matches Claude Code's behavior.
	prog = tea.NewProgram(m)

	_, err := prog.Run()

	// Ensure cursor is visible and on a fresh line after exit.
	fmt.Fprint(os.Stdout, "\x1b[?25h\n")
	return err
}
