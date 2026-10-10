package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/attach"
)

// Prompts that do not go through Loop.RunPrompt still owe the user their
// UserPromptSubmit hooks: steering typed while a turn runs, and prompts sent to
// the council or a local MCP provider. They are screened in a command, since
// hooks run subprocesses and Update must not block.

// screenRoute is where a screened prompt goes once the hooks let it through.
type screenRoute int

const (
	routeSteer   screenRoute = iota // injected into the running turn
	routeCouncil                    // council debate
	routeMCP                        // local MCP provider
)

// promptScreenedMsg is the result of screenPromptCmd.
type promptScreenedMsg struct {
	route screenRoute
	raw   string // the typed text, paste placeholders expanded
	text  string // the typed text after any updatedPrompt
	// hookContext is the hook's additionalContext as system-reminder blocks,
	// joined; only the council and MCP routes use it, since steering carries
	// the blocks in content.
	hookContext string
	// content is the steering message after the hooks, context included.
	content []api.ContentBlock
	events  []agent.LoopEvent
	err     error
	// images and pdfs are the attachments steering consumed, to restore on a block.
	images []*attach.Image
	pdfs   []*attach.PDF
}

// screenedPrompt remembers a prompt the hooks accepted while its submit is
// replayed, so the replay neither screens it again nor loses a rewrite.
type screenedPrompt struct{ raw, text, hookContext string }

// busy reports whether the model is occupied with a turn or about to be: a
// running turn, a prompt waiting on its hooks, an accepted prompt being
// resubmitted, or a synthetic Enter in flight. Nothing may start a turn of its
// own (an injected wake) until it clears, or it would swallow the pending
// submit.
func (m Model) busy() bool {
	return m.running || m.screening || m.screened != nil || m.replayingSubmit
}

// replaySubmit marks a synthetic Enter in flight and returns the command that
// sends it; the "enter" key case clears the mark.
func (m *Model) replaySubmit() tea.Cmd {
	m.replayingSubmit = true
	return func() tea.Msg { return tea.KeyPressMsg{Code: tea.KeyEnter} }
}

// promptHooksActive reports whether submitting typed text would run
// UserPromptSubmit hooks.
func (m Model) promptHooksActive() bool {
	return m.cfg.Loop != nil && m.cfg.Loop.HasPromptHooks()
}

// screenPromptCmd runs the hooks over raw. content is the message that would
// be sent (steering) and is returned adjusted; for the other routes it is the
// typed text alone.
func (m Model) screenPromptCmd(route screenRoute, raw string, content []api.ContentBlock, typedBlock int, images []*attach.Image, pdfs []*attach.PDF) tea.Cmd {
	lp := m.cfg.Loop
	return func() tea.Msg {
		var events []agent.LoopEvent
		out, err := lp.ScreenPrompt(context.Background(), agent.UserPrompt{Raw: raw, Block: typedBlock, Typed: true}, content, func(ev agent.LoopEvent) {
			events = append(events, ev)
		})
		msg := promptScreenedMsg{route: route, raw: raw, text: raw, content: out, events: events, err: err, images: images, pdfs: pdfs}
		if err == nil {
			// A rewrite replaces the typed block in place, so the same index
			// holds the new text.
			if i := typedBlock - 1; i >= 0 && i < len(out) {
				msg.text = out[i].Text
			}
			// ScreenPrompt only ever appends blocks after the content it was given.
			added := out[min(len(content), len(out)):]
			extra := make([]string, 0, len(added))
			for _, b := range added {
				extra = append(extra, b.Text)
			}
			msg.hookContext = strings.Join(extra, "\n\n")
		}
		return msg
	}
}

// handlePromptScreened applies the hooks' verdict on a prompt screened outside
// the agent loop.
func (m Model) handlePromptScreened(msg promptScreenedMsg) (Model, tea.Cmd) {
	var cmds []tea.Cmd
	m.screening = false
	for _, ev := range msg.events {
		m = m.applyAgentEvent(ev)
		if ev.Type == agent.EventSessionTitle {
			cmds = append(cmds, m.renameSessionCmd(ev.Notice))
		}
	}
	if msg.err != nil {
		var row string
		var blocked *agent.PromptBlockedError
		if errors.As(msg.err, &blocked) {
			row = blockedRow(blocked)
		} else {
			row = msg.err.Error()
		}
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: row})
		if msg.route == routeSteer {
			// Steering left the input at once; give the text and attachments back.
			m.restoreInput(msg.raw)
			m.pendingImages = append(m.pendingImages, msg.images...)
			m.pendingPDFs = append(m.pendingPDFs, msg.pdfs...)
		}
		m.refreshViewport()
		return m, tea.Batch(cmds...)
	}

	if msg.route == routeSteer {
		if m.running && m.cfg.SteerContent != nil {
			m.cfg.SteerContent(msg.content)
			m.messages = append(m.messages, userRow(msg.raw, msg.images, msg.pdfs))
			m.flashMsg = "[steering → next tool round]"
			cmds = append(cmds, tea.Tick(2*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} }))
			m.refreshViewport()
			m.vp.GotoBottom()
			return m, tea.Batch(cmds...)
		}
		if m.busy() {
			// Something else took the model while the hooks ran; the screened
			// message waits as an ordinary queued one.
			m.pendingMessages = append(m.pendingMessages, queuedMessage{text: msg.text, images: msg.images, pdfs: msg.pdfs})
			m.flashMsg = fmt.Sprintf("[queued — %d pending]", len(m.pendingMessages))
			m.refreshViewport()
			return m, tea.Batch(cmds...)
		}
		// The turn ended while the hooks ran. The message is already screened
		// (its context and images included), so it starts the next turn as is.
		m.messages = append(m.messages, userRow(msg.raw, msg.images, msg.pdfs))
		m.history = append(m.history, api.Message{Role: "user", Content: msg.content})
		m.promptRow = 0
		var turn tea.Cmd
		m, turn = m.beginTurn()
		cmds = append(cmds, turn)
		return m, tea.Batch(cmds...)
	}

	// Council and MCP prompts: replay the submit, which now skips screening.
	m.screened = &screenedPrompt{raw: msg.raw, text: msg.text, hookContext: msg.hookContext}
	cmds = append(cmds, m.replaySubmit())
	return m, tea.Batch(cmds...)
}

// beginTurn starts an agent turn over m.history as it stands, with no typed
// prompt: the last message was screened already, or never needed screening.
func (m Model) beginTurn() (Model, tea.Cmd) {
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
