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
	prog = tea.NewProgram(
		m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err := prog.Run()
	return err
}
