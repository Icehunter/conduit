package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/icehunter/claude-go/internal/agent"
)

// Run starts the full-screen TUI and blocks until the user exits.
func Run(version, modelName string, loop *agent.Loop) error {
	var prog *tea.Program

	cfg := Config{
		Version:   version,
		ModelName: modelName,
		Loop:      loop,
		Program:   &prog,
	}

	m := New(cfg)
	// No mouse capture — lets the user select/copy text normally with the
	// terminal's native selection. Mouse scrolling added in M5 when we wire
	// the viewport scroll handler.
	prog = tea.NewProgram(
		m,
		tea.WithAltScreen(),
	)
	_, err := prog.Run()
	return err
}
