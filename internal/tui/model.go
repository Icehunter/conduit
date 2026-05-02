// Package tui implements the Bubble Tea TUI for claude-go.
//
// Rendering: inline mode, no alt-screen. Completed messages are printed via
// tea.Println into the normal scrollback. Only the input box + status live-
// render at the bottom. On exit a summary line is printed — same pattern as
// Claude Code (Ink).
package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/icehunter/claude-go/internal/agent"
	"github.com/icehunter/claude-go/internal/api"
)

// Role identifies message sender.
type Role int

const (
	RoleUser Role = iota
	RoleAssistant
	RoleTool
	RoleError
	RoleSystem
)

// Message is one display entry.
type Message struct {
	Role     Role
	Content  string
	ToolName string
}

type (
	agentMsg     struct{ event agent.LoopEvent }
	agentDoneMsg struct {
		history []api.Message
		msgs    []Message // completed messages to flush to scrollback
		err     error
	}
	cancelMsg  struct{ cancel context.CancelFunc }
	clearFlash struct{}
)

// Config is passed in from main.
type Config struct {
	Version   string
	ModelName string
	Loop      *agent.Loop
	Program   **tea.Program
}

// Model is the Bubble Tea model.
type Model struct {
	cfg     Config
	history []api.Message

	input   textarea.Model
	spinner spinner.Model

	width int

	running    bool
	cancelTurn context.CancelFunc
	streaming  string      // live partial assistant text
	liveMsgs   []Message   // in-progress turn messages (for tool labels)
	liveMu     sync.Mutex  // guards liveMsgs/streaming during agent goroutine

	totalInputTokens int
	costUSD          float64
	turns            int

	flashMsg string
}

// New builds the initial model.
func New(cfg Config) Model {
	ta := textarea.New()
	ta.Placeholder = "Message claude-go  (Enter ↵ send · Shift+Enter newline)"
	ta.Focus()
	ta.SetHeight(1)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.KeyMap.InsertNewline.SetKeys("shift+enter")

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = styleSpinner

	return Model{cfg: cfg, input: ta, spinner: sp}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.input.SetWidth(m.width - 4)
		return m, nil

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
		m = m.applyEvent(msg.event)
		return m, nil

	case agentDoneMsg:
		m.running = false
		m.cancelTurn = nil
		m.streaming = ""
		m.liveMsgs = nil

		var printCmds []tea.Cmd
		for _, pm := range msg.msgs {
			printCmds = append(printCmds, tea.Println(renderMessage(pm, m.width)))
		}
		if msg.err != nil && msg.err != context.Canceled {
			printCmds = append(printCmds, tea.Println(styleErrorText.Render("✗ "+msg.err.Error())))
		} else if msg.err == nil {
			m.history = msg.history
			m.tallyTokens()
			m.turns++
		}

		m.input.Focus()
		return m, tea.Batch(append(cmds, printCmds...)...)

	case spinner.TickMsg:
		var spCmd tea.Cmd
		m.spinner, spCmd = m.spinner.Update(msg)
		cmds = append(cmds, spCmd)

	case clearFlash:
		m.flashMsg = ""
		return m, nil
	}

	var taCmd tea.Cmd
	m.input, taCmd = m.input.Update(msg)
	cmds = append(cmds, taCmd)
	return m, tea.Batch(cmds...)
}

func (m Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.running && m.cancelTurn != nil {
			m.cancelTurn()
			return m, tea.Println(styleSystemText.Render("· Interrupted."))
		}
		return m, tea.Sequence(tea.Println(m.exitSummary()), tea.Quit)

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
			return m, tea.Sequence(tea.Println(m.exitSummary()), tea.Quit)
		case "/clear":
			m.history = nil
			m.turns = 0
			m.totalInputTokens = 0
			m.costUSD = 0
			m.input.Reset()
			return m, tea.Println(styleSystemText.Render("· Conversation cleared."))
		}

		m.input.Reset()
		m.running = true
		m.streaming = ""
		m.liveMsgs = nil

		userMsg := Message{Role: RoleUser, Content: text}
		m.history = append(m.history, api.Message{
			Role:    "user",
			Content: []api.ContentBlock{{Type: "text", Text: text}},
		})

		prog := *m.cfg.Program
		histCopy := make([]api.Message, len(m.history))
		copy(histCopy, m.history)

		// Print user message immediately to scrollback, then start agent.
		return m, tea.Sequence(
			tea.Println(renderMessage(userMsg, m.width)),
			func() tea.Msg {
				ctx, cancel := context.WithCancel(context.Background())
				prog.Send(cancelMsg{cancel: cancel})

				// Track messages built during this turn.
				var mu sync.Mutex
				var turnMsgs []Message
				var streaming string

				newHist, err := m.cfg.Loop.Run(ctx, histCopy, func(ev agent.LoopEvent) {
					prog.Send(agentMsg{event: ev})
					// Mirror event into local state for final flush.
					mu.Lock()
					defer mu.Unlock()
					switch ev.Type {
					case agent.EventText:
						streaming += ev.Text
					case agent.EventToolUse:
						if streaming != "" {
							turnMsgs = append(turnMsgs, Message{Role: RoleAssistant, Content: streaming})
							streaming = ""
						}
						turnMsgs = append(turnMsgs, Message{Role: RoleTool, ToolName: ev.ToolName, Content: "done"})
					case agent.EventToolResult:
						// Update last tool message with result.
						for i := len(turnMsgs) - 1; i >= 0; i-- {
							if turnMsgs[i].Role == RoleTool {
								content := ev.ResultText
								if len(content) > 120 {
									content = content[:120] + "…"
								}
								turnMsgs[i].Content = content
								if ev.IsError {
									turnMsgs[i].Role = RoleError
								}
								break
							}
						}
					}
				})

				mu.Lock()
				if streaming != "" {
					turnMsgs = append(turnMsgs, Message{Role: RoleAssistant, Content: streaming})
				}
				finalMsgs := turnMsgs
				mu.Unlock()

				return agentDoneMsg{history: newHist, msgs: finalMsgs, err: err}
			},
		)

	case "ctrl+y":
		for i := len(m.history) - 1; i >= 0; i-- {
			if m.history[i].Role != "assistant" {
				continue
			}
			for _, b := range m.history[i].Content {
				blocks := extractCodeBlocks(b.Text)
				if len(blocks) > 0 {
					copyToClipboard(blocks[len(blocks)-1].code)
					m.flashMsg = "✓ Copied"
					return m, tea.Tick(2*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} })
				}
			}
		}
		m.flashMsg = "No code found"
		return m, tea.Tick(time.Second, func(_ time.Time) tea.Msg { return clearFlash{} })
	}

	return m, nil
}

func (m Model) applyEvent(ev agent.LoopEvent) Model {
	switch ev.Type {
	case agent.EventText:
		m.streaming += ev.Text
	case agent.EventToolUse:
		if m.streaming != "" {
			m.liveMsgs = append(m.liveMsgs, Message{Role: RoleAssistant, Content: m.streaming})
			m.streaming = ""
		}
		m.liveMsgs = append(m.liveMsgs, Message{Role: RoleTool, ToolName: ev.ToolName, Content: "running…"})
	case agent.EventToolResult:
		for i := len(m.liveMsgs) - 1; i >= 0; i-- {
			if m.liveMsgs[i].Role == RoleTool {
				content := ev.ResultText
				if len(content) > 120 {
					content = content[:120] + "…"
				}
				m.liveMsgs[i].Content = content
				if ev.IsError {
					m.liveMsgs[i].Role = RoleError
				}
				break
			}
		}
	}
	return m
}

// View renders only the live bottom chrome: spinner preview + input + status.
func (m Model) View() string {
	w := m.width
	if w == 0 {
		w = 80
	}
	var lines []string

	switch {
	case m.flashMsg != "":
		lines = append(lines, styleStatusAccent.Render(m.flashMsg))
	case m.running:
		// Live streaming preview: last few lines of the current response.
		if m.streaming != "" {
			streamLines := strings.Split(strings.TrimRight(m.streaming, "\n"), "\n")
			if len(streamLines) > 2 {
				streamLines = streamLines[len(streamLines)-2:]
			}
			for _, sl := range streamLines {
				lines = append(lines, styleAssistantText.Width(w).Render(sl))
			}
		}
		lines = append(lines, m.spinner.View()+" "+styleStatus.Render("Thinking…"))
	default:
		lines = append(lines, "")
	}

	bStyle := styleInputBorder
	if !m.running {
		bStyle = styleInputBorderActive
	}
	lines = append(lines, bStyle.Width(w-2).Render(m.input.View()))
	lines = append(lines, m.statusBar(w))
	return strings.Join(lines, "\n")
}

func (m Model) statusBar(w int) string {
	edge := strings.Repeat(" ", outerPad)
	left := edge + styleStatusAccent.Render("claude-go")
	sep := styleStatus.Render(" | ")

	var midParts []string
	midParts = append(midParts, styleStatusModel.Render(shortModelName(m.cfg.ModelName)))
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
	mid := strings.Join(midParts, sep)
	right := styleStatus.Render("^Y copy  ^C exit  /clear") + edge

	space := w - lipgloss.Width(left) - lipgloss.Width(mid) - lipgloss.Width(right)
	if space < 1 {
		space = 1
	}
	lp := space / 2
	return left + strings.Repeat(" ", lp) + mid + strings.Repeat(" ", space-lp) + right
}

func (m Model) exitSummary() string {
	cost := ""
	if m.costUSD > 0 {
		cost = fmt.Sprintf(" · $%.2f", m.costUSD)
	}
	return styleStatus.Render(fmt.Sprintf(
		"─── Session ended  %d %s · %s%s ───",
		m.turns, plural(m.turns, "turn"),
		shortModelName(m.cfg.ModelName), cost,
	))
}

func (m *Model) tallyTokens() {
	total := 0
	for _, msg := range m.history {
		for _, b := range msg.Content {
			total += len([]rune(b.Text)) / 4
		}
	}
	m.totalInputTokens = total
	m.costUSD = float64(total) * 45.0 / 1_000_000
}

func shortModelName(name string) string {
	name = strings.TrimPrefix(name, "claude-")
	i := strings.Index(name, "-")
	if i < 0 {
		return capitalize(name)
	}
	return capitalize(name[:i]) + " " + strings.ReplaceAll(name[i+1:], "-", ".")
}

func capitalize(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
