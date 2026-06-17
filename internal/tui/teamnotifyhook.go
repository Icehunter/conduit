package tui

import "github.com/icehunter/conduit/internal/agent"

// TeammateNotifyHook carries the live-event streaming callback wired by Run()
// after the Bubble Tea program starts. mainrepl creates one, passes it via
// RunOptions.TeammateNotify, and the SpawnTeammate closure captures it to
// stream per-event updates into the TUI's team panes.
type TeammateNotifyHook struct {
	// Send forwards a loop event from the named teammate to the TUI.
	// Set by Run() after prog.Start(). Nil before that — callers must guard.
	Send func(name string, ev agent.LoopEvent)
}
