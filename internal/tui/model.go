// Package tui implements the Bubble Tea TUI for claude-go.
package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/icehunter/claude-go/internal/agent"
	"github.com/icehunter/claude-go/internal/api"
)

// Layout constants — all in terminal rows/cols.
const (
	// inputRows is how many text rows the textarea shows.
	// 1 = single-line feel; Shift+Enter expands it naturally.
	inputRows = 1
	// inputBorderRows: top + bottom border = 2, inner padding = 0
	inputBorderRows = 2
	// statusRows: one line at the very bottom
	statusRows = 1
	// spinnerRows: one line above the input
	spinnerRows = 1
	// totalChrome = everything that isn't the message viewport
	totalChrome = inputRows + inputBorderRows + statusRows + spinnerRows
)

// Role identifies who sent a message.
type Role int

const (
	RoleUser Role = iota
	RoleAssistant
	RoleTool
	RoleError
	RoleSystem
)

// Message is one entry in the displayed conversation.
type Message struct {
	Role     Role
	Content  string
	ToolName string
}

type (
	agentMsg     struct{ event agent.LoopEvent }
	agentDoneMsg struct {
		history []api.Message
		err     error
	}
	cancelMsg struct{ cancel context.CancelFunc }
)

// Config is passed from main to the TUI.
type Config struct {
	Version   string
	ModelName string
	Loop      *agent.Loop
	Program   **tea.Program
}

// Model is the Bubble Tea model.
type Model struct {
	cfg      Config
	messages []Message
	history  []api.Message

	input   textarea.Model
	vp      viewport.Model
	spinner spinner.Model

	width  int
	height int

	running    bool
	cancelTurn context.CancelFunc
	streaming  string

	vpReady bool
}

// New builds the initial Model.
func New(cfg Config) Model {
	ta := textarea.New()
	ta.Placeholder = "Message claude-go  (Enter ↵ send · Shift+Enter newline)"
	ta.Focus()
	ta.SetHeight(inputRows)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	// Remove the default newline binding; remap to shift+enter.
	ta.KeyMap.InsertNewline.SetKeys("shift+enter")

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = styleSpinner

	return Model{cfg: cfg, input: ta, spinner: sp}
}

// Init starts the blink + spinner tick.
func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick)
}

// Update is the Elm update function.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m = m.applyLayout()
		return m, nil

	case tea.KeyMsg:
		m2, cmd := m.handleKey(msg)
		m = m2
		if cmd != nil {
			cmds = append(cmds, cmd)
		}

	case cancelMsg:
		m.cancelTurn = msg.cancel

	case agentMsg:
		m = m.applyAgentEvent(msg.event)
		return m, nil

	case agentDoneMsg:
		m.running = false
		m.cancelTurn = nil
		if m.streaming != "" {
			m.messages = append(m.messages, Message{Role: RoleAssistant, Content: m.streaming})
			m.streaming = ""
		}
		if msg.err != nil && msg.err != context.Canceled {
			m.messages = append(m.messages, Message{Role: RoleError, Content: msg.err.Error()})
		} else if msg.err == nil {
			m.history = msg.history
		}
		m.refreshViewport()
		m.vp.GotoBottom()
		m.input.Focus()
		return m, nil

	case spinner.TickMsg:
		var spCmd tea.Cmd
		m.spinner, spCmd = m.spinner.Update(msg)
		cmds = append(cmds, spCmd)
	}

	var taCmd, vpCmd tea.Cmd
	m.input, taCmd = m.input.Update(msg)
	m.vp, vpCmd = m.vp.Update(msg)
	cmds = append(cmds, taCmd, vpCmd)
	return m, tea.Batch(cmds...)
}

func (m Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.running && m.cancelTurn != nil {
			m.cancelTurn()
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Interrupted."})
			m.refreshViewport()
			return m, nil
		}
		return m, tea.Quit

	case "enter":
		if m.running {
			return m, nil
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		switch text {
		case "/exit", "/quit":
			return m, tea.Quit
		case "/clear":
			m.messages = nil
			m.history = nil
			m.input.Reset()
			m.refreshViewport()
			return m, nil
		}

		m.input.Reset()
		m.messages = append(m.messages, Message{Role: RoleUser, Content: text})
		m.history = append(m.history, api.Message{
			Role:    "user",
			Content: []api.ContentBlock{{Type: "text", Text: text}},
		})
		m.running = true
		m.streaming = ""
		m.refreshViewport()
		m.vp.GotoBottom()

		prog := *m.cfg.Program
		histCopy := make([]api.Message, len(m.history))
		copy(histCopy, m.history)

		return m, func() tea.Msg {
			ctx, cancel := context.WithCancel(context.Background())
			prog.Send(cancelMsg{cancel: cancel})
			newHist, err := m.cfg.Loop.Run(ctx, histCopy, func(ev agent.LoopEvent) {
				prog.Send(agentMsg{event: ev})
			})
			return agentDoneMsg{history: newHist, err: err}
		}
	}
	return m, nil
}

func (m Model) applyAgentEvent(ev agent.LoopEvent) Model {
	switch ev.Type {
	case agent.EventText:
		m.streaming += ev.Text
		m.refreshViewport()
		m.vp.GotoBottom()

	case agent.EventToolUse:
		if m.streaming != "" {
			m.messages = append(m.messages, Message{Role: RoleAssistant, Content: m.streaming})
			m.streaming = ""
		}
		m.messages = append(m.messages, Message{
			Role: RoleTool, ToolName: ev.ToolName, Content: "running…",
		})
		m.refreshViewport()

	case agent.EventToolResult:
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role == RoleTool && m.messages[i].Content == "running…" {
				content := ev.ResultText
				if len(content) > 200 {
					content = content[:200] + "…"
				}
				m.messages[i].Content = content
				if ev.IsError {
					m.messages[i].Role = RoleError
				}
				break
			}
		}
		m.refreshViewport()
	}
	return m
}

// applyLayout recalculates component sizes on window resize.
func (m Model) applyLayout() Model {
	vpHeight := m.height - totalChrome
	if vpHeight < 3 {
		vpHeight = 3
	}
	// Inner width for textarea: full width minus border(2) minus padding(2)
	innerW := m.width - 4
	if innerW < 10 {
		innerW = 10
	}

	if !m.vpReady {
		m.vp = viewport.New(m.width, vpHeight)
		m.vpReady = true
	} else {
		m.vp.Width = m.width
		m.vp.Height = vpHeight
	}

	m.input.SetWidth(innerW)
	m.refreshViewport()
	return m
}

// refreshViewport rebuilds the viewport content string.
func (m *Model) refreshViewport() {
	if !m.vpReady {
		return
	}
	w := m.vp.Width
	if w < 20 {
		w = 80
	}
	var sb strings.Builder
	for i, msg := range m.messages {
		if i > 0 {
			sb.WriteString(separator(w))
			sb.WriteByte('\n')
		}
		sb.WriteString(renderMessage(msg, w))
		sb.WriteByte('\n')
	}
	if m.streaming != "" {
		if len(m.messages) > 0 {
			sb.WriteString(separator(w))
			sb.WriteByte('\n')
		}
		sb.WriteString(renderMessage(Message{Role: RoleAssistant, Content: m.streaming}, w))
		sb.WriteByte('\n')
	}
	m.vp.SetContent(sb.String())
}

// View renders the full TUI frame.
func (m Model) View() string {
	if !m.vpReady {
		return "Loading…\n"
	}

	// ── viewport ────────────────────────────────────────────────────────
	vp := m.vp.View()

	// ── spinner row (always present to keep layout stable) ──────────────
	var spinRow string
	if m.running {
		spinRow = m.spinner.View() + " " + styleStatus.Render("Thinking…")
	} else {
		// blank placeholder keeps layout from jumping
		spinRow = " "
	}

	// ── input box ────────────────────────────────────────────────────────
	bStyle := styleInputBorder
	if !m.running {
		bStyle = styleInputBorderActive
	}
	inputBox := bStyle.Width(m.width - 2).Render(m.input.View())

	// ── status bar ───────────────────────────────────────────────────────
	left := styleStatus.Render("claude-go " + m.cfg.Version)
	mid := styleStatusModel.Render(m.cfg.ModelName)
	right := styleStatus.Render("^C interrupt · /clear · /exit")
	// right-align: pad left side to push right to edge
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(mid) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	statusBar := left +
		strings.Repeat(" ", gap/2) + mid +
		strings.Repeat(" ", gap-gap/2) + right

	return lipgloss.JoinVertical(lipgloss.Left, vp, spinRow, inputBox, statusBar)
}
