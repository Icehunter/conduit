package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/hooks"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/rtk"
	"github.com/icehunter/conduit/internal/tools/bashtool"
)

// Shell mode: a prompt that starts with "!" runs the rest as a shell command
// instead of going to the model. The command goes through the same permission
// gate as a model-issued Bash call, its output is shown as a Bash tool row,
// and both the command and its output are appended to the conversation so the
// next prompt can refer to them. Unlike Claude Code, the model is not queried
// automatically afterwards.

// shellDoneMsg reports a finished (or refused) shell-mode command.
type shellDoneMsg struct {
	id        string // matches the transcript row's ToolID
	turnID    int    // the turn the command ran as
	command   string
	output    string
	isErr     bool
	denied    bool
	cancelled bool
	took      time.Duration
}

// shellHookMsg carries the PreToolUse hook outcome for a shell-mode command.
type shellHookMsg struct {
	id       string
	turnID   int
	command  string
	blocked  bool
	reason   string
	approved bool
	ask      bool   // a hook asked for a prompt even if the rules allow
	updated  string // a hook-rewritten command, empty when unchanged
}

// shellCommand returns the command for a "!" prompt. ok is false when text is
// not a shell-mode prompt.
func shellCommand(text string) (cmd string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(text), "!")
	if !found {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// runShell executes command with the Bash tool and returns its text output.
func (m Model) runShell(ctx context.Context, command string) (string, bool) {
	if m.cfg.RunShell != nil {
		return m.cfg.RunShell(ctx, command)
	}
	raw, _ := json.Marshal(map[string]string{"command": command})
	res, err := bashtool.New(nil).Execute(ctx, raw)
	if err != nil {
		return err.Error(), true
	}
	var sb strings.Builder
	for _, b := range res.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return sb.String(), res.IsError
}

// startShellMode handles a submitted "!command" as a turn of its own: the
// model shows as running (spinner), Esc cancels it, and submits made meanwhile
// are queued. The caller has already cleared the input.
func (m Model) startShellMode(command string) (Model, tea.Cmd) {
	m.shellSeq++
	id := fmt.Sprintf("shell-%d", m.shellSeq)
	input, _ := json.Marshal(map[string]string{"command": command})
	m.messages = append(m.messages,
		Message{Role: RoleUser, Content: "!" + command},
		Message{Role: RoleTool, ToolName: "Bash", ToolID: id, ToolInput: string(input), Content: "running…", ToolStarted: time.Now()},
	)
	m.turnID++
	m.running = true
	m.cancelled = false
	m.turnStarted = time.Now()
	m.shellID = id
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelTurn = cancel
	m.shellCtx = ctx
	m.refreshViewport()
	m.vp.GotoBottom()

	turnID := m.turnID
	if m.cfg.Hooks == nil || len(m.cfg.Hooks.PreToolUse) == 0 {
		return m.shellAuthorize(shellHookMsg{id: id, turnID: turnID, command: command})
	}
	hooksCfg := m.cfg.Hooks.PreToolUse
	sessionID := ""
	if m.cfg.Session != nil {
		sessionID = m.cfg.Session.ID
	}
	return m, func() tea.Msg {
		r := hooks.RunPreToolUse(ctx, hooksCfg, sessionID, "Bash", map[string]any{"command": command})
		msg := shellHookMsg{id: id, turnID: turnID, command: command, blocked: r.Blocked, reason: r.Reason, approved: r.Approved && !r.Ask, ask: r.Ask}
		if c, ok := r.UpdatedInput["command"].(string); ok && strings.TrimSpace(c) != "" {
			msg.updated = c
		}
		return msg
	}
}

// shellAuthorize applies the hook result and the permission gate, then runs
// the command. It mirrors the agent path: a blocking hook refuses, an
// approving hook skips the prompt, otherwise the gate decides.
func (m Model) shellAuthorize(h shellHookMsg) (Model, tea.Cmd) {
	if h.id != m.shellID || h.turnID != m.turnID {
		return m, nil // cancelled or superseded while the hook ran
	}
	ctx := m.shellCtx
	command := h.command
	if h.updated != "" {
		command = h.updated
	}
	refuse := func(text string) tea.Cmd {
		return func() tea.Msg {
			return shellDoneMsg{id: h.id, turnID: h.turnID, command: command, output: text, isErr: true, denied: true}
		}
	}
	run := func() tea.Msg {
		start := time.Now()
		out, isErr := m.runShell(ctx, command)
		return shellDoneMsg{id: h.id, turnID: h.turnID, command: command, output: out, isErr: isErr, cancelled: ctx.Err() != nil, took: time.Since(start)}
	}
	if h.blocked {
		reason := h.reason
		if reason == "" {
			reason = "blocked by PreToolUse hook"
		}
		return m, refuse("Command blocked by hook: " + reason)
	}
	// The gate always runs, on the command that will actually execute, so deny
	// rules win. A hook "ask" escalates an allow to a prompt; a hook approval
	// only skips a plain ask prompt, never an explicit ask rule or a protected
	// path. This matches the agent path in looptools.go.
	decision := permissions.DecisionAllow
	if m.cfg.Gate != nil {
		decision = m.cfg.Gate.Check("Bash", command)
		if decision == permissions.DecisionAllow && h.ask {
			decision = permissions.DecisionAsk
		}
		if decision == permissions.DecisionAsk && h.approved && !h.ask &&
			!m.cfg.Gate.MatchesAskRule("Bash", command) && !m.cfg.Gate.IsProtectedWrite("Bash", command) {
			decision = permissions.DecisionAllow
		}
	}
	switch decision {
	case permissions.DecisionDeny:
		return m, refuse("Command denied by permission rules.")
	case permissions.DecisionAsk:
		reply := make(chan permissionReply, 1)
		prompt := m.newPermissionPrompt(permissionAskMsg{toolName: "Bash", toolInput: command, reply: reply})
		prompt.onceOnly = true
		m.permPrompt = prompt
		m.refreshViewport()
		return m, func() tea.Msg {
			select {
			case r := <-reply:
				if !r.allow {
					return refuse("Command denied.")()
				}
				return run()
			case <-ctx.Done():
				return shellDoneMsg{id: h.id, turnID: h.turnID, command: h.command, output: "Command cancelled.", isErr: true, denied: true, cancelled: true}
			}
		}
	}
	return m, run
}

// applyShellDone fills in the transcript row and finishes the turn. The
// exchange joins the conversation history only if it completed as the current
// turn (not cancelled, not refused, not superseded), so it can never
// interleave with a model turn's messages.
func (m Model) applyShellDone(msg shellDoneMsg) (Model, tea.Cmd) {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Role == RoleTool && m.messages[i].ToolID == msg.id {
			out := strings.TrimSpace(msg.output)
			if out == "" {
				out = "(no output)"
			}
			if msg.cancelled && out == "(no output)" {
				out = "Command cancelled."
			}
			m.messages[i].Content = out
			m.messages[i].ToolError = msg.isErr || msg.cancelled
			m.messages[i].ToolDuration = max(msg.took, time.Millisecond)
			break
		}
	}
	current := msg.id == m.shellID && msg.turnID == m.turnID
	if msg.id == m.shellID {
		m.shellID = ""
		m.shellCtx = nil
	}
	if !current {
		m.refreshViewport()
		return m, nil
	}
	m.running = false
	m.cancelTurn = nil
	if !msg.denied && !msg.cancelled {
		output := msg.output
		command := msg.command
		if m.cfg.RedactSecrets == nil || m.cfg.RedactSecrets() {
			output, _ = rtk.RedactSecrets(output)
			command, _ = rtk.RedactSecrets(command)
		}
		m.history = append(m.history, api.Message{
			Role: "user",
			Content: []api.ContentBlock{{
				Type: "text",
				Text: "<bash-input>" + command + "</bash-input>\n<bash-stdout>" + output + "</bash-stdout>",
			}},
		})
		m.persistNewMessages(m.history)
	}
	m.refreshViewport()
	m, cmd, _ := m.drainPending()
	return m, cmd
}
