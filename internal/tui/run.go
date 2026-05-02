package tui

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/icehunter/claude-go/internal/agent"
	"github.com/icehunter/claude-go/internal/api"
	"github.com/icehunter/claude-go/internal/commands"
	internalmodel "github.com/icehunter/claude-go/internal/model"
)

// altScreenEnter/Exit are the ANSI sequences for the alternate screen buffer.
const (
	altScreenEnter = "\x1b[?1049h\x1b[?25l" // enter alt-screen, hide cursor
	altScreenExit  = "\x1b[?1049l\x1b[?25h" // exit alt-screen, show cursor
	clearScreen    = "\x1b[2J\x1b[H"         // erase display + cursor home
)

// Run starts the full-screen TUI and blocks until the user exits.
func Run(version, modelName string, loop *agent.Loop, client ...*api.Client) error {
	var prog *tea.Program

	reg := commands.New()
	commands.RegisterBuiltins(reg)
	commands.RegisterModelCommand(reg,
		func() string { return internalmodel.Resolve() },
		func(name string) { loop.SetModel(name) },
	)
	commands.RegisterCompactCommand(reg)

	var apiClient *api.Client
	if len(client) > 0 {
		apiClient = client[0]
	}

	cfg := Config{
		Version:   version,
		ModelName: modelName,
		Loop:      loop,
		Program:   &prog,
		Commands:  reg,
		APIClient: apiClient,
	}

	m := New(cfg)
	prog = tea.NewProgram(
		m,
		tea.WithAltScreen(),
	)

	// Re-enter alt-screen after SIGWINCH (iTerm2 resize) so the terminal
	// doesn't leave ghost frames in the main buffer's scrollback.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			fmt.Fprint(os.Stdout, clearScreen)
		}
	}()

	// Clean exit on interrupt/term.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigs
		prog.Kill()
	}()

	_, err := prog.Run()

	// Guarantee alt-screen is exited even if Bubble Tea's cleanup was partial.
	fmt.Fprint(os.Stdout, altScreenExit)

	signal.Stop(winch)
	signal.Stop(sigs)
	close(winch)
	return err
}
