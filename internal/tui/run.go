package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/icehunter/claude-go/internal/agent"
)

// Run starts the full-screen TUI and blocks until the user exits.
// Uses alt-screen so the session doesn't appear in the terminal's scrollback.
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
		// WithoutSignalHandler lets us manage cleanup ourselves so the alt-screen
		// is always restored even on abnormal exit paths.
		tea.WithoutSignalHandler(),
	)

	// Ensure alt-screen is exited and cursor is restored even if we crash.
	defer func() {
		// These are the ANSI sequences Bubble Tea uses internally.
		// Writing them explicitly guarantees the terminal is restored
		// if tea.Program.Run() exits without cleanup.
		fmt.Fprint(os.Stdout, "\x1b[?1049l") // exit alt-screen
		fmt.Fprint(os.Stdout, "\x1b[?25h")   // show cursor
	}()

	_, err := prog.Run()
	return err
}
