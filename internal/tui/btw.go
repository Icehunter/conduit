package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/commands"
)

// btwTimeout bounds a /btw side question.
const btwTimeout = 2 * time.Minute

// btwDoneMsg carries the answer to a /btw side question.
type btwDoneMsg struct {
	question string
	answer   string
	err      error
}

// applyBtwResult handles the "btw" command result: it asks the question on a
// copy of the conversation and leaves the main history, and any running turn,
// alone.
func (m Model) applyBtwResult(res commands.Result) (Model, tea.Cmd) {
	question := strings.TrimSpace(res.Text)
	if m.cfg.Loop == nil {
		m.messages = append(m.messages, Message{Role: RoleError, Content: "/btw unavailable: no agent loop."})
		m.refreshViewport()
		return m, nil
	}
	loop := m.cfg.Loop
	hist := append([]api.Message(nil), m.history...)
	m.messages = append(m.messages, Message{Role: RoleSystem, Content: btwPending(question)})
	m.refreshViewport()
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), btwTimeout)
		defer cancel()
		ans, err := loop.SideQuestion(ctx, hist, question)
		return btwDoneMsg{question: question, answer: ans, err: err}
	}
}

// btwPending is the placeholder row shown while a side question is in flight.
func btwPending(question string) string { return "/btw " + question + "\nThinking…" }

// handleBtwDone replaces the pending row with the side answer, or appends one
// when the pending row is gone (e.g. after /clear).
func (m Model) handleBtwDone(msg btwDoneMsg) Model {
	row := Message{Role: RoleSystem, Content: "/btw " + msg.question + "\n" + msg.answer}
	if msg.err != nil {
		row = Message{Role: RoleError, Content: "/btw failed: " + msg.err.Error()}
	}
	pending := btwPending(msg.question)
	replaced := false
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Role == RoleSystem && m.messages[i].Content == pending {
			m.messages[i] = row
			replaced = true
			break
		}
	}
	if !replaced {
		m.messages = append(m.messages, row)
	}
	m.refreshViewport()
	return m
}
