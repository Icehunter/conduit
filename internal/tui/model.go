// Package tui implements the Bubble Tea TUI for claude-go.
//
// M3 scope: scrollable message history, multi-line input, spinner during
// model response, status line, Ctrl+C to interrupt (not exit), /exit /clear.
// Vim mode, keybinding config, and advanced scroll land in M5.
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/icehunter/claude-go/internal/agent"
	"github.com/icehunter/claude-go/internal/api"
)

// Role identifies who sent a message.
type Role int

const (
	RoleUser      Role = iota
	RoleAssistant Role = iota
	RoleTool      Role = iota
	RoleError     Role = iota
	RoleSystem    Role = iota
)

// Message is one entry in the displayed conversation.
type Message struct {
	Role     Role
	Content  string
	ToolName string // only for RoleTool
}

// Internal Bubble Tea messages.
type (
	agentMsg     struct{ event agent.LoopEvent }
	agentDoneMsg struct {
		history []api.Message
		err     error
	}
	cancelMsg struct{ cancel context.CancelFunc }
)

// Config holds the TUI configuration passed in from main.
type Config struct {
	Version   string
	ModelName string
	Loop      *agent.Loop
	Program   **tea.Program // pointer set by Run so the agent goroutine can Send
}

// Model is the Bubble Tea model.
type Model struct {
	cfg      Config
	messages []Message     // display history
	history  []api.Message // API conversation history

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

// New creates a new TUI Model.
func New(cfg Config) Model {
	ta := textarea.New()
	ta.Placeholder = "Type a message… (Enter to send, Shift+Enter for newline)"
	ta.Focus()
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.KeyMap.InsertNewline.SetKeys("shift+enter")

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = styleSpinner

	return Model{
		cfg:     cfg,
		input:   ta,
		spinner: sp,
	}
}

// Init starts the spinner and textarea blink.
func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick)
}

// Update handles all messages.
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
		m = m.handleAgentEvent(msg.event)
		// Don't pass agent messages to textarea/viewport — they're internal.
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

	// Propagate to sub-components.
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

		// Submit user message, start agent turn.
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
		historyCopy := make([]api.Message, len(m.history))
		copy(historyCopy, m.history)

		return m, func() tea.Msg {
			ctx, cancel := context.WithCancel(context.Background())
			prog.Send(cancelMsg{cancel: cancel})
			newHistory, err := m.cfg.Loop.Run(ctx, historyCopy, func(ev agent.LoopEvent) {
				prog.Send(agentMsg{event: ev})
			})
			return agentDoneMsg{history: newHistory, err: err}
		}
	}

	return m, nil
}

func (m Model) handleAgentEvent(ev agent.LoopEvent) Model {
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
			Role:     RoleTool,
			ToolName: ev.ToolName,
			Content:  "running…",
		})
		m.refreshViewport()

	case agent.EventToolResult:
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role == RoleTool && m.messages[i].Content == "running…" {
				content := ev.ResultText
				if len(content) > 300 {
					content = content[:300] + "…"
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

// applyLayout recalculates sizes after a window resize.
func (m Model) applyLayout() Model {
	inputHeight := 5 // textarea rows + border
	statusHeight := 1
	vpHeight := m.height - inputHeight - statusHeight - 1
	if vpHeight < 3 {
		vpHeight = 3
	}

	if !m.vpReady {
		m.vp = viewport.New(m.width, vpHeight)
		m.vp.SetContent("")
		m.vpReady = true
	} else {
		m.vp.Width = m.width
		m.vp.Height = vpHeight
	}

	m.input.SetWidth(m.width - 6) // account for box border + padding
	m.refreshViewport()
	return m
}

// refreshViewport re-renders all messages into the viewport content.
func (m *Model) refreshViewport() {
	if !m.vpReady {
		return
	}
	w := m.vp.Width
	if w < 10 {
		w = 80
	}
	var sb strings.Builder
	for _, msg := range m.messages {
		sb.WriteString(renderMessage(msg, w))
		sb.WriteString("\n\n")
	}
	if m.streaming != "" {
		sb.WriteString(renderMessage(Message{Role: RoleAssistant, Content: m.streaming}, w))
		sb.WriteString("\n")
	}
	m.vp.SetContent(sb.String())
}

// View renders the complete TUI.
func (m Model) View() string {
	if !m.vpReady {
		return "Initializing…\n"
	}

	// Message viewport.
	vpView := m.vp.View()

	// Spinner / blank line.
	var spinLine string
	if m.running {
		spinLine = m.spinner.View() + " " + styleStatusLine.Render("Claude is thinking…")
	}

	// Input box.
	boxStyle := styleInputBox
	if !m.running {
		boxStyle = styleInputBoxActive
	}
	inputView := boxStyle.Width(m.width - 4).Render(m.input.View())

	// Status line.
	status := fmt.Sprintf("claude-go %s  model: %s  Ctrl+C: interrupt/exit  /clear /exit",
		m.cfg.Version, m.cfg.ModelName)
	statusLine := styleStatusLine.Width(m.width).Render(status)

	return lipgloss.JoinVertical(lipgloss.Left,
		vpView,
		spinLine,
		inputView,
		statusLine,
	)
}
