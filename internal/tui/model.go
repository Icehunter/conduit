// Package tui implements the Bubble Tea TUI for claude-go.
package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/icehunter/claude-go/internal/agent"
	"github.com/icehunter/claude-go/internal/api"
	"github.com/icehunter/claude-go/internal/commands"
	"github.com/icehunter/claude-go/internal/compact"
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
		turnID    int
		history   []api.Message
		err       error
		cancelled bool // ctx was cancelled before the loop finished
	}
	compactDoneMsg struct {
		newHistory []api.Message
		summary    string
		err        error
	}
	// loginStartMsg triggers the OAuth flow after the user picks a login method.
	loginStartMsg struct{ claudeAI bool }
	// loginDoneMsg is sent when the OAuth flow completes.
	loginDoneMsg struct{ err error }

	// permissionAskMsg is sent by the agent goroutine when a tool needs
	// interactive permission. The goroutine blocks on reply until the user
	// chooses Allow once / Always allow / Deny.
	permissionAskMsg struct {
		toolName  string
		toolInput string
		reply     chan<- permissionReply
	}
	permissionReply struct {
		allow       bool
		alwaysAllow bool // add to session allow list
	}
	clearFlash struct{}
)

// Config is passed from main to the TUI.
type Config struct {
	Version    string
	ModelName  string
	Loop       *agent.Loop
	Program    **tea.Program
	Commands   *commands.Registry
	APIClient  *api.Client
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

	running     bool
	cancelled   bool // true after Ctrl+C; cleared when next turn starts
	cancelTurn  context.CancelFunc
	streaming   string
	turnID      int // incremented each turn; agentDoneMsg with stale ID is ignored

	// slash command picker state
	cmdMatches  []commands.Command // currently matching commands
	cmdSelected int                // selected index in cmdMatches

	totalInputTokens  int
	totalOutputTokens int
	costUSD           float64

	// flashMsg is shown in the spinner row briefly (e.g. "Copied!").
	flashMsg string

	// modelName is the currently active model (can be changed via /model).
	modelName string

	// Permission prompt state — non-nil when a tool is waiting for approval.
	permPrompt *permissionPromptState

	// Login picker state — non-nil when /login is active.
	loginPrompt *loginPromptState

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

	return Model{cfg: cfg, input: ta, spinner: sp, modelName: cfg.ModelName}
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
		// Erase the entire screen and home the cursor on every resize.
		// tea.ClearScreen only clears the visible area; the explicit sequence
		// also resets the scroll region, preventing ghost chrome lines from
		// appearing in the scrollback after an iTerm2 resize.
		return m, tea.Batch(append(cmds,
			tea.ClearScreen,
			func() tea.Msg {
				// Force a full repaint by sending a no-op that triggers re-render.
				return nil
			},
		)...)

	case tea.KeyMsg:
		m2, cmd := m.handleKey(msg)
		m = m2
		if cmd != nil {
			cmds = append(cmds, cmd)
		}

	case agentMsg:
		m = m.applyAgentEvent(msg.event)
		return m, nil

	case agentDoneMsg:
		if msg.turnID != m.turnID {
			// Stale completion from a previous (interrupted) turn — discard.
			return m, nil
		}
		m.running = false
		m.cancelled = false
		m.cancelTurn = nil
		if m.streaming != "" {
			m.messages = append(m.messages, Message{Role: RoleAssistant, Content: m.streaming})
			m.streaming = ""
		}
		if msg.cancelled || isCancelError(msg.err) {
			// Context was cancelled — Ctrl+C already committed partial history.
			// Nothing to do here; never show an error bubble.
		} else if msg.err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: msg.err.Error()})
			if len(m.history) > 0 && m.history[len(m.history)-1].Role == "user" {
				m.history = m.history[:len(m.history)-1]
			}
		} else {
			m.history = msg.history
			m.tallyTokens()
		}
		m.refreshViewport()
		m.vp.GotoBottom()
		m.input.Focus()
		return m, nil

	case loginStartMsg:
		useClaudeAI := msg.claudeAI
		cfg := m.cfg
		return m, func() tea.Msg {
			err := runLoginFlow(useClaudeAI, cfg)
			return loginDoneMsg{err: err}
		}

	case loginDoneMsg:
		if msg.err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Login failed: %v", msg.err)})
		} else {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Logged in successfully."})
		}
		m.refreshViewport()
		return m, nil

	case permissionAskMsg:
		m.permPrompt = &permissionPromptState{
			toolName:  msg.toolName,
			toolInput: msg.toolInput,
			reply:     msg.reply,
			selected:  0,
		}
		m.refreshViewport()
		return m, nil

	case compactDoneMsg:
		m.running = false
		m.cancelTurn = nil
		if msg.err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Compact failed: %v", msg.err)})
		} else {
			m.history = msg.newHistory
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: fmt.Sprintf("Conversation compacted. Summary:\n\n%s", msg.summary)})
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

	case clearFlash:
		m.flashMsg = ""
		return m, nil
	}

	// Always propagate remaining messages to sub-components.
	var taCmd, vpCmd tea.Cmd
	m.input, taCmd = m.input.Update(msg)
	m.vp, vpCmd = m.vp.Update(msg)
	cmds = append(cmds, taCmd, vpCmd)

	// Recompute command picker matches after every key so the list stays live.
	if !m.running && m.cfg.Commands != nil {
		m.cmdMatches, m.cmdSelected = m.computeCommandMatches()
	}

	return m, tea.Batch(cmds...)
}

func (m Model) handleKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	// Login picker intercepts all keys when active.
	if m.loginPrompt != nil {
		return m.handleLoginKey(msg)
	}
	// Permission prompt intercepts all keys when active.
	if m.permPrompt != nil {
		return m.handlePermissionKey(msg)
	}

	switch msg.String() {
	case "up":
		if len(m.cmdMatches) > 0 {
			if m.cmdSelected > 0 {
				m.cmdSelected--
			}
			return m, nil
		}

	case "down":
		if len(m.cmdMatches) > 0 {
			if m.cmdSelected < len(m.cmdMatches)-1 {
				m.cmdSelected++
			}
			return m, nil
		}

	case "tab", "escape":
		if len(m.cmdMatches) > 0 {
			if msg.String() == "tab" {
				// Accept selected match.
				m.input.SetValue("/" + m.cmdMatches[m.cmdSelected].Name + " ")
				m.input.CursorEnd()
			}
			m.cmdMatches = nil
			m.cmdSelected = 0
			return m, nil
		}
		if msg.String() == "tab" && !m.running && m.cfg.Commands != nil {
			// Fallback tab completion when picker isn't open.
			text := m.input.Value()
			if strings.HasPrefix(text, "/") && !strings.Contains(text, " ") {
				completed := m.tabComplete(text)
				if completed != text {
					m.input.SetValue(completed)
					m.input.CursorEnd()
				}
			}
			return m, nil
		}

	case "ctrl+c":
		if m.running && m.cancelTurn != nil {
			m.cancelTurn()
			m.cancelled = true
			m.running = false
			m.cancelTurn = nil
			// Commit whatever partial response was streamed so the next turn
			// has context. Keep the user message in history too.
			if m.streaming != "" {
				m.messages = append(m.messages, Message{Role: RoleAssistant, Content: m.streaming})
				m.history = append(m.history, api.Message{
					Role:    "assistant",
					Content: []api.ContentBlock{{Type: "text", Text: m.streaming}},
				})
				m.streaming = ""
			}
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Interrupted."})
			m.refreshViewport()
			m.input.Focus()
			return m, nil
		}
		return m, tea.Quit

	case "ctrl+y":
		// Copy the raw code from the most recent assistant code block to
		// the system clipboard via OSC 52 (works in iTerm2, kitty, WezTerm).
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role == RoleAssistant {
				blocks := extractCodeBlocks(m.messages[i].Content)
				if len(blocks) > 0 {
					copyToClipboard(blocks[len(blocks)-1].code)
					m.flashMsg = "✓ Copied to clipboard"
					return m, tea.Tick(2000000000, func(_ time.Time) tea.Msg { return clearFlash{} })
				}
			}
		}
		m.flashMsg = "No code block found"
		return m, tea.Tick(1500000000, func(_ time.Time) tea.Msg { return clearFlash{} })

	case "enter":
		if m.running {
			return m, nil
		}

		// If the command picker is open, select the highlighted entry.
		if len(m.cmdMatches) > 0 {
			selected := m.cmdMatches[m.cmdSelected]
			m.cmdMatches = nil
			m.cmdSelected = 0
			m.input.SetValue("/" + selected.Name + " ")
			m.input.CursorEnd()
			return m, nil
		}

		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}

		// Dispatch slash commands before sending to the agent.
		if strings.HasPrefix(text, "/") {
			m.input.Reset()
			if m.cfg.Commands != nil {
				if res, ok := m.cfg.Commands.Dispatch(text); ok {
					return m.applyCommandResult(res)
				}
			}
			m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Unknown command: %s (try /help)", text)})
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
		m.cancelled = false
		m.streaming = ""
		m.refreshViewport()
		m.vp.GotoBottom()

		m.turnID++
		ctx, cancel := context.WithCancel(context.Background())
		m.cancelTurn = cancel
		prog := *m.cfg.Program
		histCopy := make([]api.Message, len(m.history))
		copy(histCopy, m.history)
		turnID := m.turnID

		return m, func() tea.Msg {
			newHist, err := m.cfg.Loop.Run(ctx, histCopy, func(ev agent.LoopEvent) {
				prog.Send(agentMsg{event: ev})
			})
			return agentDoneMsg{turnID: turnID, history: newHist, err: err, cancelled: ctx.Err() != nil}
		}
	}
	return m, nil
}

// renderCommandPicker renders the slash command picker dropdown.
func (m Model) renderCommandPicker() string {
	const maxItems = 8
	nameColW := 14

	// The current query (text after "/").
	query := strings.ToLower(strings.TrimPrefix(m.input.Value(), "/"))

	// Compute visible window around the selected index.
	start := m.cmdSelected - maxItems/2
	if start < 0 {
		start = 0
	}
	end := start + maxItems
	if end > len(m.cmdMatches) {
		end = len(m.cmdMatches)
		start = end - maxItems
		if start < 0 {
			start = 0
		}
	}

	descMax := m.width - nameColW - 10
	var sb strings.Builder
	for i := start; i < end; i++ {
		cmd := m.cmdMatches[i]

		// Render name with query highlighted.
		rawName := fmt.Sprintf("/%-*s", nameColW, cmd.Name)
		var namePart string
		if i == m.cmdSelected {
			namePart = highlightMatch(rawName, query, stylePickerItemSelected, stylePickerHighlight)
		} else {
			namePart = highlightMatch(rawName, query, stylePickerItem, stylePickerHighlight)
		}

		// Render description with query highlighted.
		desc := cmd.Description
		if descMax > 10 && len([]rune(desc)) > descMax {
			desc = string([]rune(desc)[:descMax]) + "…"
		}
		descPart := highlightMatch(desc, query, stylePickerDesc, stylePickerHighlight)

		sb.WriteString(namePart + "  " + descPart)
		if i < end-1 {
			sb.WriteByte('\n')
		}
	}

	return stylePickerBorder.Width(m.width - 2).Render(sb.String())
}

// highlightMatch renders s with every case-insensitive occurrence of query
// highlighted using highlightStyle, and the rest in baseStyle.
// Returns the base-styled string unchanged if query is empty.
func highlightMatch(s, query string, baseStyle, highlightStyle lipgloss.Style) string {
	if query == "" {
		return baseStyle.Render(s)
	}
	lower := strings.ToLower(s)
	var out strings.Builder
	pos := 0
	for {
		idx := strings.Index(lower[pos:], query)
		if idx < 0 {
			out.WriteString(baseStyle.Render(s[pos:]))
			break
		}
		abs := pos + idx
		if abs > pos {
			out.WriteString(baseStyle.Render(s[pos:abs]))
		}
		out.WriteString(highlightStyle.Render(s[abs : abs+len(query)]))
		pos = abs + len(query)
		if pos >= len(s) {
			break
		}
	}
	return out.String()
}

// computeCommandMatches returns commands matching the current input and resets
// the selection index if the match set changed.
func (m Model) computeCommandMatches() ([]commands.Command, int) {
	text := m.input.Value()
	if !strings.HasPrefix(text, "/") || strings.Contains(text, " ") || m.running {
		return nil, 0
	}
	query := strings.ToLower(strings.TrimPrefix(text, "/"))
	all := m.cfg.Commands.All()
	var matches []commands.Command
	for _, c := range all {
		if c.Name == "quit" {
			continue // deduplicate with exit
		}
		if strings.Contains(c.Name, query) || strings.Contains(strings.ToLower(c.Description), query) {
			matches = append(matches, c)
		}
	}
	// Preserve selection if the same set, otherwise reset.
	sel := m.cmdSelected
	if sel >= len(matches) {
		sel = 0
	}
	return matches, sel
}

// tabComplete returns the best completion for a partial slash command.
// If exactly one command matches the prefix, it returns "/<name> " (with trailing
// space so the user can immediately type args). If multiple match, it completes
// to the longest common prefix. If none match, returns input unchanged.
func (m Model) tabComplete(input string) string {
	prefix := strings.ToLower(strings.TrimPrefix(input, "/"))
	cmds := m.cfg.Commands.All()

	var matches []string
	for _, c := range cmds {
		if strings.HasPrefix(c.Name, prefix) {
			matches = append(matches, c.Name)
		}
	}
	switch len(matches) {
	case 0:
		return input
	case 1:
		return "/" + matches[0] + " "
	default:
		// Longest common prefix of all matches.
		lcp := matches[0]
		for _, m := range matches[1:] {
			for len(lcp) > 0 && !strings.HasPrefix(m, lcp) {
				lcp = lcp[:len(lcp)-1]
			}
		}
		return "/" + lcp
	}
}

// loginPromptState holds the /login account picker state.
type loginPromptState struct {
	selected int
}

var loginOptions = []struct {
	label       string
	description string
	claudeAI    bool
}{
	{"Claude.ai account", "Max, Pro, or Team subscription", true},
	{"Anthropic Console", "Console / Platform / API account", false},
}

func (m Model) handleLoginKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	p := m.loginPrompt
	switch msg.String() {
	case "up", "left":
		if p.selected > 0 {
			p.selected--
		}
	case "down", "right", "tab":
		if p.selected < len(loginOptions)-1 {
			p.selected++
		}
	case "enter", " ":
		opt := loginOptions[p.selected]
		m.loginPrompt = nil
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Opening browser to sign in…"})
		m.refreshViewport()
		useClaudeAI := opt.claudeAI
		prog := *m.cfg.Program
		return m, func() tea.Msg {
			prog.Send(loginStartMsg{claudeAI: useClaudeAI})
			return nil
		}
	case "escape", "ctrl+c":
		m.loginPrompt = nil
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Login cancelled."})
		m.refreshViewport()
		return m, nil
	case "1":
		p.selected = 0
		m.loginPrompt = p
		return m.handleLoginKey(tea.KeyMsg{Type: tea.KeyEnter})
	case "2":
		p.selected = 1
		m.loginPrompt = p
		return m.handleLoginKey(tea.KeyMsg{Type: tea.KeyEnter})
	}
	m.loginPrompt = p
	return m, nil
}

func (m Model) renderLoginPicker() string {
	p := m.loginPrompt
	if p == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render("Sign in to Claude") + "\n\n")
	sb.WriteString(stylePickerDesc.Render("Choose your account type:") + "\n\n")

	for i, opt := range loginOptions {
		var line string
		if i == p.selected {
			line = stylePickerItemSelected.Render(fmt.Sprintf("▶ %d. %s", i+1, opt.label)) +
				"  " + stylePickerDesc.Render(opt.description)
		} else {
			line = stylePickerItem.Render(fmt.Sprintf("  %d. %s", i+1, opt.label)) +
				"  " + stylePickerDesc.Render(opt.description)
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\n" + stylePickerDesc.Render("↑↓ navigate · Enter select · 1/2 quick pick · Escape cancel"))

	style := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		PaddingLeft(2).PaddingRight(2).PaddingTop(1).PaddingBottom(1)
	return style.Width(m.width - 4).Render(sb.String())
}

// permissionPromptState holds the active permission prompt data.
type permissionPromptState struct {
	toolName  string
	toolInput string
	reply     chan<- permissionReply
	selected  int // 0=Allow once, 1=Always allow, 2=Deny
}

var permissionOptions = []string{"Allow once", "Always allow", "Deny"}

// handlePermissionKey routes keys to the permission modal.
func (m Model) handlePermissionKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	p := m.permPrompt
	switch msg.String() {
	case "up", "left", "shift+tab":
		if p.selected > 0 {
			p.selected--
		}
	case "down", "right", "tab":
		if p.selected < len(permissionOptions)-1 {
			p.selected++
		}
	case "enter", " ":
		reply := permissionReply{
			allow:       p.selected != 2,
			alwaysAllow: p.selected == 1,
		}
		m.permPrompt = nil
		m.refreshViewport()
		prog := *m.cfg.Program
		return m, func() tea.Msg {
			p.reply <- reply
			_ = prog
			return nil
		}
	case "ctrl+c", "escape":
		// Treat escape as Deny.
		reply := permissionReply{allow: false}
		m.permPrompt = nil
		m.refreshViewport()
		return m, func() tea.Msg {
			p.reply <- reply
			return nil
		}
	case "1":
		p.selected = 0
		reply := permissionReply{allow: true, alwaysAllow: false}
		m.permPrompt = nil
		m.refreshViewport()
		return m, func() tea.Msg { p.reply <- reply; return nil }
	case "2":
		p.selected = 1
		reply := permissionReply{allow: true, alwaysAllow: true}
		m.permPrompt = nil
		m.refreshViewport()
		return m, func() tea.Msg { p.reply <- reply; return nil }
	case "3":
		reply := permissionReply{allow: false}
		m.permPrompt = nil
		m.refreshViewport()
		return m, func() tea.Msg { p.reply <- reply; return nil }
	}
	m.permPrompt = p
	return m, nil
}

// renderPermissionPrompt renders the permission modal overlay.
func (m Model) renderPermissionPrompt() string {
	p := m.permPrompt
	if p == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render("Permission required") + "\n\n")

	// Tool + input.
	label := p.toolName
	if p.toolInput != "" {
		short := p.toolInput
		maxLen := m.width - 20
		if maxLen > 10 && len(short) > maxLen {
			short = short[:maxLen] + "…"
		}
		label += "(" + short + ")"
	}
	sb.WriteString(stylePickerItem.Render(label) + "\n\n")

	for i, opt := range permissionOptions {
		prefix := "  "
		var rendered string
		if i == p.selected {
			rendered = stylePickerItemSelected.Render("▶ " + opt)
		} else {
			rendered = stylePickerItem.Render("  " + opt)
		}
		sb.WriteString(prefix + rendered + "\n")
	}
	sb.WriteString("\n" + stylePickerDesc.Render("↑↓ navigate · Enter select · 1/2/3 quick pick"))

	style := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		PaddingLeft(2).PaddingRight(2).PaddingTop(1).PaddingBottom(1)
	return style.Width(m.width - 4).Render(sb.String())
}

// applyCommandResult handles a slash command result in the TUI.
func (m Model) applyCommandResult(res commands.Result) (Model, tea.Cmd) {
	switch res.Type {
	case "clear":
		m.messages = nil
		m.history = nil
		m.refreshViewport()
		return m, nil
	case "exit":
		return m, tea.Quit
	case "model":
		m.modelName = res.Model
		m.cfg.Loop.SetModel(res.Model)
		if res.Text != "" {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: res.Text})
		}
		m.refreshViewport()
		return m, nil
	case "compact":
		if m.cfg.APIClient == nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: "Compact unavailable: no API client."})
			m.refreshViewport()
			return m, nil
		}
		if len(m.history) == 0 {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Nothing to compact."})
			m.refreshViewport()
			return m, nil
		}
		m.running = true
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Compacting conversation…"})
		m.refreshViewport()
		customInstructions := res.Text
		client := m.cfg.APIClient
		histCopy := make([]api.Message, len(m.history))
		copy(histCopy, m.history)
		return m, func() tea.Msg {
			result, err := compact.Compact(context.Background(), client, histCopy, customInstructions)
			if err != nil {
				return compactDoneMsg{err: err}
			}
			return compactDoneMsg{newHistory: result.NewHistory, summary: result.Summary}
		}
	case "error":
		m.messages = append(m.messages, Message{Role: RoleError, Content: res.Text})
		m.refreshViewport()
		return m, nil
	case "login":
		m.loginPrompt = &loginPromptState{selected: 0}
		m.refreshViewport()
		return m, nil
	case "add-dir":
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Added directory: " + res.Text})
		m.refreshViewport()
		return m, nil
	case "export":
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Export to file not yet implemented. Path would be: " + res.Text})
		m.refreshViewport()
		return m, nil
	default: // "text"
		if res.Text != "" {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: res.Text})
			m.refreshViewport()
		}
		return m, nil
	}
}

// CostSummary returns a human-readable cost/token summary for the /cost command.
func (m *Model) CostSummary() string {
	if m.totalInputTokens == 0 && m.costUSD == 0 {
		return "No API calls made yet in this session."
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Input tokens:  %d\n", m.totalInputTokens))
	sb.WriteString(fmt.Sprintf("Output tokens: %d\n", m.totalOutputTokens))
	if m.costUSD > 0 {
		sb.WriteString(fmt.Sprintf("Estimated cost: $%.4f", m.costUSD))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// isCancelError reports whether err represents a user-initiated cancellation.
// Covers context.Canceled, context.DeadlineExceeded, and the network-level
// "use of closed network connection" that appears when the HTTP response body
// is torn down mid-read (which doesn't wrap context.Canceled directly).
func isCancelError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "context canceled") ||
		strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "request canceled")
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
	switch {
	case m.flashMsg != "":
		spinRow = styleStatusAccent.Render(m.flashMsg)
	case m.running:
		spinRow = m.spinner.View() + " " + styleStatus.Render("Thinking…")
	default:
		spinRow = ""
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
	modelSeg := styleStatusModel.Render(shortModelName(m.modelName))
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
	right := styleStatus.Render("^Y copy code  ^C interrupt  /clear  /exit") + edgePad

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

	// Overlays: login picker > permission prompt > command picker.
	var overlayBox string
	if m.loginPrompt != nil {
		overlayBox = m.renderLoginPicker()
	} else if m.permPrompt != nil {
		overlayBox = m.renderPermissionPrompt()
	} else if len(m.cmdMatches) > 0 {
		overlayBox = m.renderCommandPicker()
	}

	// JoinVertical with explicit newlines between non-empty parts.
	parts := []string{vp}
	if spinRow != "" {
		parts = append(parts, spinRow)
	} else {
		parts = append(parts, "")
	}
	if overlayBox != "" {
		parts = append(parts, overlayBox)
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
