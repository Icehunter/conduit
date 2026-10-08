package tui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/bgjobs"
	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/coordinator"
)

// injectedWakeMsg tells an idle session that messages were queued with
// Loop.InjectMessage (a background command or workflow finished) and a turn
// should start for them. A running turn picks the queue up at its next
// delivery point, or when it ends.
type injectedWakeMsg struct{}

// activeProgram is the running TUI program, set for the life of Run.
var activeProgram atomic.Pointer[tea.Program]

// Wake nudges the running TUI to deliver any messages queued on its loop. It
// is safe to call from any goroutine and does nothing when no TUI is running.
func Wake() {
	if p := activeProgram.Load(); p != nil {
		p.Send(injectedWakeMsg{})
	}
}

// bashesViewMsg carries the rendered output of `/bashes view`.
type bashesViewMsg struct{ res commands.Result }

// bashesViewCmd reads a job's output tail off the update loop.
func bashesViewCmd(id string) tea.Cmd {
	return func() tea.Msg { return bashesViewMsg{res: commands.BashesView(bgjobs.Default, id)} }
}

// startInjectedTurn starts a turn whose user message is the loop's queued
// notifications. While the model is busy with anything (m.running is also set
// by /compact, local MCP calls, council and shell mode, none of which reach
// handleAgentDone) it only records wakePending; Update re-checks once the
// busy state clears.
func (m Model) startInjectedTurn() (Model, tea.Cmd) {
	lp := m.cfg.Loop
	if lp == nil || m.noAuth || m.cfg.Program == nil || !lp.HasInjected() {
		return m, nil
	}
	if m.busy() {
		m.wakePending = true
		return m, nil
	}
	// With an MCP provider active, turns go through runLocalCall rather than
	// the loop, so the loop's queue cannot be delivered here; it stays queued
	// for the next loop turn.
	if _, usingMCPProvider := m.activeMCPProvider(); usingMCPProvider {
		return m, nil
	}
	texts := lp.DrainInjected()
	if len(texts) == 0 {
		return m, nil
	}
	m.dismissWelcome()
	blocks := make([]api.ContentBlock, 0, len(texts))
	for _, text := range texts {
		blocks = append(blocks, api.ContentBlock{Type: "text", Text: text})
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: injectedRow(text)})
	}
	m.history = append(m.history, api.Message{Role: "user", Content: blocks})
	m.running = true
	m.cancelled = false
	m.streaming = ""
	m.thinking = ""
	m.captureTurnProvider()
	m.apiRetryStatus = ""
	m.turnStarted = time.Now()
	m.refreshViewport()
	m.vp.GotoBottom()
	m.turnID++
	m.attemptSet = false
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	return m, m.runLoopCmd(ctx, m.turnID)
}

// injectedRow is the one-line transcript row for a queued message: the
// summary of a task notification, else the message's first line.
func injectedRow(text string) string {
	if coordinator.IsTaskNotification(text) {
		if _, rest, ok := strings.Cut(text, "<summary>"); ok {
			if summary, _, ok := strings.Cut(rest, "</summary>"); ok {
				return fmt.Sprintf("● %s", unescapeXML(summary))
			}
		}
	}
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if r := []rune(line); len(r) > 120 {
		line = string(r[:119]) + "…"
	}
	return "● " + line
}

func unescapeXML(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&amp;", "&").Replace(s)
}
