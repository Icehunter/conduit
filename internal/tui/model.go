// Package tui implements the Bubble Tea TUI for claude-go.
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

// chromeHeight returns the number of terminal rows consumed by everything
// except the viewport. Called dynamically so it's always accurate.
//
//   spinner row:   1
//   input border:  1 (top) + 1 (bottom) = 2
//   input text:    1
//   status bar:    1
//   ─────────────────
//   total:         5
func chromeHeight() int { return 5 }

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

	totalInputTokens  int
	totalOutputTokens int
	costUSD           float64

	ready bool // true once we've received the first WindowSizeMsg
}

// New builds the initial Model.
func New(cfg Config) Model {
	ta := textarea.New()
	ta.Placeholder = "Message claude-go  (Enter ↵ send · Shift+Enter newline)"
	ta.Focus()
	ta.SetHeight(1)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.KeyMap.InsertNewline.SetKeys("shift+enter")
	// Remove default enter binding from the textarea — we handle it ourselves.

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
		// Don't propagate to sub-components here — applyLayout handles it.
		return m, tea.Batch(cmds...)

	case tea.KeyMsg:
		m2, cmd := m.handleKey(msg)
		m = m2
		if cmd != nil {
			cmds = append(cmds, cmd)
		}

	case cancelMsg:
		m.cancelTurn = msg.cancel
		return m, nil

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
			m.tallyTokens()
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

	// Always propagate remaining messages to sub-components.
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

// applyLayout recalculates component dimensions.
func (m Model) applyLayout() Model {
	if m.width == 0 || m.height == 0 {
		return m
	}
	vpHeight := m.height - chromeHeight()
	if vpHeight < 1 {
		vpHeight = 1
	}
	// Input inner width: full width minus left+right border (2) minus left+right padding (2).
	inputW := m.width - 4
	if inputW < 10 {
		inputW = 10
	}

	if !m.ready {
		m.vp = viewport.New(m.width, vpHeight)
		m.vp.Style = lipgloss.NewStyle() // no extra styling on the viewport itself
		m.ready = true
	} else {
		m.vp.Width = m.width
		m.vp.Height = vpHeight
	}
	m.input.SetWidth(inputW)
	m.refreshViewport()
	return m
}

// refreshViewport rebuilds the viewport content string.
func (m *Model) refreshViewport() {
	if !m.ready {
		return
	}
	w := m.vp.Width
	if w <= 0 {
		return
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
	if !m.ready {
		return "Loading…\n"
	}

	// Viewport.
	vp := m.vp.View()

	// Spinner row — always 1 line to prevent layout shift.
	var spinRow string
	if m.running {
		spinRow = m.spinner.View() + " " + styleStatus.Render("Thinking…")
	} else {
		spinRow = "" // empty but we still join it — JoinVertical handles empty strings
	}

	// Input box.
	bStyle := styleInputBorder
	if !m.running {
		bStyle = styleInputBorderActive
	}
	// Width: outer border consumes 2 cols; inner padding consumes 2 more.
	inputBox := bStyle.Width(m.width - 2).Render(m.input.View())

	// Status bar — left/right edges have outerPad spaces to align with content.
	edgePad := strings.Repeat(" ", outerPad)
	appName := edgePad + styleStatusAccent.Render("claude-go")
	modelSeg := styleStatusModel.Render(shortModelName(m.cfg.ModelName))
	barSep := styleStatus.Render(" | ")

	var midParts []string
	midParts = append(midParts, modelSeg)
	if m.totalInputTokens > 0 {
		pct := m.totalInputTokens * 100 / 200000
		if pct > 100 {
			pct = 100
		}
		midParts = append(midParts, styleStatus.Render(fmt.Sprintf("%d%% ctx", pct)))
	}
	if m.costUSD > 0 {
		midParts = append(midParts, styleStatus.Render(fmt.Sprintf("$%.2f", m.costUSD)))
	}
	mid := strings.Join(midParts, barSep)
	right := styleStatus.Render("^C interrupt  /clear  /exit") + edgePad

	leftW := lipgloss.Width(appName)
	midW := lipgloss.Width(mid)
	rightW := lipgloss.Width(right)
	space := m.width - leftW - midW - rightW
	if space < 1 {
		space = 1
	}
	lPad := space / 2
	rPad := space - lPad
	statusBar := appName + strings.Repeat(" ", lPad) + mid + strings.Repeat(" ", rPad) + right

	// JoinVertical with explicit newlines between non-empty parts.
	parts := []string{vp}
	if spinRow != "" {
		parts = append(parts, spinRow)
	} else {
		// blank line holds the space so input doesn't jump up
		parts = append(parts, "")
	}
	parts = append(parts, inputBox, statusBar)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// tallyTokens estimates token usage from conversation history.
func (m *Model) tallyTokens() {
	total := 0
	for _, msg := range m.history {
		for _, b := range msg.Content {
			total += len([]rune(b.Text)) / 4
		}
	}
	m.totalInputTokens = total
	// Opus 4.7: ~$15/$75 per M in/out, blended ~$45/M estimate.
	m.costUSD = float64(total) * 45.0 / 1_000_000
}

// shortModelName converts "claude-opus-4-7" → "Opus 4.7".
func shortModelName(name string) string {
	// Strip leading "claude-"
	name = strings.TrimPrefix(name, "claude-")
	// Split on first "-" to get family, then the rest is the version.
	// "opus-4-7" → family="opus", ver="4-7"
	idx := strings.Index(name, "-")
	if idx < 0 {
		return capitalize(name)
	}
	family := capitalize(name[:idx])
	ver := strings.ReplaceAll(name[idx+1:], "-", ".")
	return family + " " + ver
}

func capitalize(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
