package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
)

// runLoopCmd returns the command that runs the agent loop over a copy of
// m.history, forwarding its events to the program and finishing with an
// agentDoneMsg for turnID.
//
// A compaction is written to the session here, on the loop goroutine and
// before its event is sent. Update therefore sees the event only after the
// file holds the compacted history, and a /resume after a crash restores the
// summary and its verbatim tail instead of the pre-compaction conversation.
//
// A turn the user started by typing passes that prompt, and only such a turn
// runs UserPromptSubmit hooks (on prompt.Raw). Turns the TUI starts itself,
// such as injected notifications, pass none.
func (m Model) runLoopCmd(ctx context.Context, turnID int, prompt ...agent.UserPrompt) tea.Cmd {
	var typed agent.UserPrompt
	if len(prompt) > 0 {
		typed = prompt[0]
	}
	prog := *m.cfg.Program
	lp := m.cfg.Loop
	hist := make([]api.Message, len(m.history))
	copy(hist, m.history)
	var usage api.Usage
	contextInputTokens := 0
	handler := m.loopEventHandler(prog.Send, &usage, &contextInputTokens)
	return tea.Batch(setWindowTitleCmd("conduit · working"), func() tea.Msg {
		newHist, err := lp.RunPrompt(ctx, typed, hist, handler)
		return agentDoneMsg{turnID: turnID, history: newHist, err: err, cancelled: ctx.Err() != nil, usage: usage, contextInputTokens: contextInputTokens}
	})
}

// loopEventHandler returns the callback that receives the loop's events on its
// goroutine. It totals usage into usage and contextInputTokens, persists a
// compaction, and forwards every event through send.
func (m Model) loopEventHandler(send func(tea.Msg), usage *api.Usage, contextInputTokens *int) func(agent.LoopEvent) {
	sess := m.cfg.Session
	annotate := m.annotateTurnProvider
	// onDisk is how many messages of the loop's current history the session
	// file already holds; a compaction block re-appends the tail, and the
	// search indexer must skip only the copies of messages it has seen.
	onDisk := m.persistedCount
	return func(ev agent.LoopEvent) {
		switch ev.Type {
		case agent.EventUsage:
			*usage = accumulateUsage(*usage, ev.Usage)
			*contextInputTokens = maxPromptInputTokens(*contextInputTokens, ev.Usage)
		case agent.EventCompacted:
			if sess != nil {
				persisted, tailLen := persistableCompaction(annotate(ev.History), ev.CompactTailLen)
				copies := tailCopies(ev.CompactPreLen, onDisk, tailLen)
				if err := sess.AppendCompaction(ev.CompactSummary, persisted, copies); err != nil {
					send(compactSaveFailedMsg{err: err, summary: ev.CompactSummary, history: persisted})
				}
				onDisk = len(persisted)
			}
		}
		send(agentMsg{event: ev})
	}
}

// compactSaveFailedMsg reports that appending a compaction to the session
// failed, with what Update needs to try rewriting the file instead.
type compactSaveFailedMsg struct {
	err     error
	summary string
	history []api.Message
}

// persistableCompaction is the part of a compaction's history that goes to the
// session file. A trailing user message is the prompt (or tool result) the
// next request still has to answer; it reaches the file with the turn's own
// messages, like any other, so writing it here would let a failed turn leave it
// behind, and its replacement skipped. tailLen is adjusted to match.
func persistableCompaction(history []api.Message, tailLen int) ([]api.Message, int) {
	n := len(history)
	if n > 1 && history[n-1].Role == "user" {
		if tailLen > 0 {
			tailLen--
		}
		return history[:n-1], tailLen
	}
	return history, tailLen
}

// tailCopies is how many of the tailLen verbatim tail messages of a compaction
// were already in the session file. preLen is the length of the history that
// was compacted, of which the first onDisk messages are on disk; the tail is
// its last tailLen messages. The rest reach the file for the first time inside
// the compaction block, which is where the search indexer must find them.
func tailCopies(preLen, onDisk, tailLen int) int {
	return max(0, min(tailLen, onDisk-(preLen-tailLen)))
}
