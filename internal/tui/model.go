// Package tui implements the Bubble Tea TUI for conduit.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/attach"
	"github.com/icehunter/conduit/internal/buddy"
	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/keybindings"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/memdir"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/profile"
	"github.com/icehunter/conduit/internal/ratelimit"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/theme"
	"github.com/icehunter/conduit/internal/tokens"
	"github.com/icehunter/conduit/internal/tools/tasktool"
)

// chromeHeight returns the number of terminal rows consumed by everything
// except the viewport, given the current input row count and terminal
// height. Dynamic so multi-line input doesn't permanently squeeze chat.
//
//   spinner row:   1
//   input border:  1 (top) + 1 (bottom) = 2
//   input text:    inputRows (1..inputMaxRows)
//   status bar:    1
//
// The input is capped at inputMaxRows visible rows (~30% of the screen,
// floor 1, ceiling 12) so the chat viewport always keeps at least 70% of
// the terminal. Beyond the cap, the textarea scrolls internally.
const (
	chromeFixed   = 4 // spinner + 2 borders + status (everything except input rows)
	inputMinRows  = 1
	inputMaxRows  = 12
	inputMaxRatio = 0.30
)

// rePasteToken matches "[Pasted text #N +M lines]" placeholder tokens
// inserted by the bracketed-paste handler.
var rePasteToken = regexp.MustCompile(`\[Pasted text #(\d+) \+\d+ lines\]`)

// isNewlineInsertKey reports whether the key would cause the textarea to
// insert a newline. Mirrors the binding set on
// textarea.KeyMap.InsertNewline (shift+enter, alt+enter, ctrl+j) — kept
// in sync manually because bubbles textarea doesn't expose the bound
// key list as a string set.
func isNewlineInsertKey(k tea.KeyPressMsg) bool {
	switch k.String() {
	case "shift+enter", "alt+enter", "ctrl+j":
		return true
	}
	return false
}

func chromeHeight(inputRows, termHeight int) int {
	cap := int(float64(termHeight) * inputMaxRatio)
	if cap < inputMinRows {
		cap = inputMinRows
	}
	if cap > inputMaxRows {
		cap = inputMaxRows
	}
	if inputRows < inputMinRows {
		inputRows = inputMinRows
	}
	if inputRows > cap {
		inputRows = cap
	}
	return chromeFixed + inputRows
}

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
	Role        Role
	Content     string
	ToolName    string
	WelcomeCard bool // render as the two-panel welcome banner
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
	// loginURLMsg carries OAuth URLs to display inline in the conversation.
	loginURLMsg struct {
		automatic string
		manual    string
	}
	// loginBrowserFailMsg is sent when the browser fails to open.
	loginBrowserFailMsg struct{ err error }
	// loginDoneMsg is sent when the OAuth flow completes.
	loginDoneMsg struct{ err error }
	// authReloadMsg is sent after loginDone to deliver the refreshed API client + profile.
	authReloadMsg struct {
		client  *api.Client
		profile *profile.Info
		err     error
	}

	// resumePickMsg is sent when /resume is invoked with session list data.
	resumePickMsg struct {
		sessions []resumeSession
	}
	// resumeLoadMsg carries a loaded session's history after the user picks one.
	resumeLoadMsg struct {
		msgs []api.Message
		err  error
	}

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
	clearFlash  struct{}
	clearBubble struct{}

	// setPermissionModeMsg is sent by EnterPlanMode/ExitPlanMode tool
	// callbacks to change the active permission mode from outside the TUI event loop.
	setPermissionModeMsg struct{ mode permissions.Mode }

	// setModelNameMsg is sent by /fast and /model to update the displayed model name.
	setModelNameMsg struct {
		name string
		fast bool // true when sent by /fast toggle
	}
)

// Config is passed from main to the TUI.
type Config struct {
	Version    string
	ModelName  string
	Loop       *agent.Loop
	Program    **tea.Program
	Commands   *commands.Registry
	APIClient  *api.Client
	MCPManager *mcp.Manager
	Gate       *permissions.Gate

	// AuthErr is non-nil when the TUI started without valid credentials.
	AuthErr error
	// Profile is the user's subscription/account info fetched at startup.
	Profile profile.Info
	// Session is the active transcript session (nil if persistence unavailable).
	Session *session.Session
	// ResumedHistory is pre-loaded history from a --continue session.
	ResumedHistory []api.Message
	// Resumed is true when --continue loaded a prior session.
	Resumed bool
	// LoadAuth reloads credentials + profile after /login.
	LoadAuth func(ctx context.Context) (string, *profile.Info, error)
	// NewAPIClient constructs a fresh client for the given bearer.
	NewAPIClient func(bearer string) *api.Client
	// Live is the shared state bag readable from command callbacks outside
	// the Bubble Tea event loop. Populated by the model on each Update.
	Live *LiveState
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

	// @ file/dir completion picker state. Active when the last word (no
	// spaces) in the input starts with "@". Cleared on space, Tab-accept,
	// or Escape.
	atMatches  []string // relative paths matching the @ query
	atSelected int      // selected index

	totalInputTokens  int
	totalOutputTokens int
	costUSD           float64

	// flashMsg is shown in the spinner row briefly (e.g. "Copied!").
	flashMsg string

	// companionBubble is the text shown in the companion speech bubble overlay.
	// Set when the agent produces a short (<= 100 char) single-line response
	// while a companion is configured and the user addressed it by name.
	// Auto-cleared after ~10 seconds via a clearBubble tick.
	companionBubble string

	// rateLimitWarning is non-empty when a recent turn's rate-limit headers
	// indicate quota is running low (<20% remaining). Shown in the status bar.
	rateLimitWarning string

	// fastMode is true when /fast is active (showing ⚡ badge).
	fastMode bool

	// modelName is the currently active model (can be changed via /model).
	modelName string

	// inputHistory stores previous sent messages for up/down history cycling.
	// historyIdx=-1 means we're at the live input (not browsing history).
	inputHistory []string
	historyIdx   int
	// historyDraft saves the in-progress text before the user starts browsing.
	historyDraft string

	// Permission prompt state — non-nil when a tool is waiting for approval.
	permPrompt *permissionPromptState

	// Login picker state — non-nil when /login is active.
	loginPrompt *loginPromptState

	ready  bool // true once we've received the first WindowSizeMsg
	noAuth bool // true when TUI started without credentials

	// Resume picker state — non-nil when /resume is showing session list.
	resumePrompt *resumePromptState

	// Generic picker for /theme, /model, /output-style. Non-nil when active.
	picker *pickerState

	// Onboarding overlay — shown on first run until the user dismisses it.
	// Nil after dismissal or for returning users.
	onboarding *onboardingState

	// loginFlowMsgStart is the index into m.messages where the login flow
	// messages begin. -1 means no login flow is in progress.
	loginFlowMsgStart int

	// persistedCount tracks how many messages from m.history have already
	// been written to the session file (avoids duplicating on each turn).
	persistedCount int

	// welcomeDismissed is true once the welcome card has been removed.
	welcomeDismissed bool

	// panel is the unified MCP/Plugin/Marketplace browser overlay.
	// Non-nil when active.
	panel *panelState

	// pluginPanel is the full plugin browser overlay.
	// Non-nil when active.
	pluginPanel *pluginPanelState

	// settingsPanel is the full-screen Settings/Config/Stats/Usage panel.
	// Non-nil when active.
	settingsPanel *settingsPanelState

	// permissionMode tracks the active permission mode for Shift+Tab cycling.
	// Mirrors getNextPermissionMode.ts cycle: default → acceptEdits → plan → default.
	permissionMode permissions.Mode

	// outputStyleName / outputStylePrompt hold the active output style.
	// When set, the prompt is prepended to the system blocks on each turn.
	outputStyleName   string
	outputStylePrompt string

	// kb resolves user-customized keybindings from ~/.claude/keybindings.json.
	// Nil when keybindings could not be loaded (treated as defaults-only).
	kb *keybindings.Resolver

	// pendingImages holds clipboard images queued to send with the next
	// message. Each ctrl+v appends one image. Cleared on submit or Esc.
	pendingImages []*attach.Image

	// pastedBlocks holds large text pastes that are displayed as
	// "[Pasted text #N +X lines]" placeholders in the textarea. The
	// placeholder string is written into the textarea; the raw content is
	// stored here indexed by placeholder number. On submit the raw content
	// replaces the placeholder in the sent message. Backspace in the
	// textarea removes the entire placeholder token.
	pastedBlocks map[int]string // placeholder# → raw text
	pastedSeq    int            // monotonic counter for placeholder numbers
}

// New builds the initial Model.
func New(cfg Config) Model {
	ta := textarea.New()
	ta.Placeholder = "Message conduit  (Enter ↵ send · Shift+Enter newline)"
	// Per-line chevron prompt, but only on the first line. Bubbles textarea
	// renders Prompt on every visible row (replacing the default ┃, U+2503
	// HEAVY VERTICAL), and a row of repeating "❯" looks like a confused list,
	// not an input cue. SetPromptFunc gives us a per-line callback so
	// continuation rows render as plain spaces of the same width.
	ta.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "❯ "
		}
		return "  "
	})
	ta.Focus()
	ta.SetHeight(1)
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	// Newline keys: Shift+Enter is the canonical binding now that bubbletea
	// v2's key disambiguation decodes it correctly on every modern terminal
	// (Kitty/Ghostty/WezTerm/Warp/iTerm2/Alacritty/Foot/Rio/Contour). Keep
	// the legacy alternatives bound so users on terminals without
	// progressive enhancement (e.g. Apple Terminal, raw xterm, tmux without
	// passthrough) can still insert newlines:
	//   alt+enter — ESC+CR sequence emitted by Option/Alt+Enter
	//   ctrl+j   — the literal newline byte (LF), universal
	ta.KeyMap.InsertNewline.SetKeys("shift+enter", "alt+enter", "ctrl+j")
	// Remove default enter binding from the textarea — we handle it ourselves.

	// Static cursor (no blink) — blink causes the chat bar to repaint twice
	// a second, and on light themes the repaint cycle is visible as flashing
	// because lipgloss bg paint regenerates each frame. v2 reads cursor
	// blink off Styles.Cursor.Blink instead of cursor.SetMode.
	tas := ta.Styles()
	tas.Cursor.Blink = false
	ta.SetStyles(tas)

	applyTextareaTheme(&ta)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = styleSpinner

	m := Model{cfg: cfg, input: ta, spinner: sp, modelName: cfg.ModelName, historyIdx: -1, loginFlowMsgStart: -1}

	// First-run welcome — only when not resuming and the persistence flag
	// hasn't been set. Look at user-level settings only since the
	// onboardingComplete flag is global, not per-project.
	if !cfg.Resumed {
		merged, err := settings.Load("")
		if err == nil && !merged.OnboardingComplete {
			userName := cfg.Profile.DisplayName
			if userName == "" {
				userName = cfg.Profile.Email
			}
			m.onboarding = &onboardingState{
				authenticated: cfg.AuthErr == nil,
				userName:      userName,
			}
		}
	}

	if cfg.AuthErr != nil {
		m.messages = append(m.messages, Message{
			Role:    RoleSystem,
			Content: "Not logged in · Run /login to authenticate",
		})
		m.noAuth = true
	} else if cfg.Resumed && len(cfg.ResumedHistory) > 0 {
		m.history = cfg.ResumedHistory
		m.persistedCount = len(cfg.ResumedHistory) // already on disk
		// Rebuild display messages from the API history so the user can see the conversation.
		m.messages = append(m.messages, Message{
			Role:    RoleSystem,
			Content: fmt.Sprintf("Resumed previous conversation (%d messages). ↑ scroll to see history.", len(cfg.ResumedHistory)),
		})
		for _, apiMsg := range cfg.ResumedHistory {
			m.messages = append(m.messages, historyToDisplayMessage(apiMsg))
		}
		m.tallyTokens()
	} else {
		m.messages = append(m.messages, m.welcomeCard())
	}

	// Load user keybindings. Errors are silently ignored — we fall back to
	// defaults so a malformed keybindings.json never prevents startup.
	if bindings, err := keybindings.LoadAll(settings.ClaudeDir()); err == nil {
		m.kb = keybindings.NewResolver(bindings)
	} else {
		m.kb = keybindings.NewResolver(keybindings.Defaults())
	}

	return m
}

// Init starts the blink + spinner tick. Also kicks off the MCP approval
// picker if any project-scope servers are awaiting consent, and the
// coordinator-panel tick that drives the active-task footer.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{textarea.Blink, m.spinner.Tick, coordTick()}
	if m.cfg.MCPManager != nil {
		if pending := m.cfg.MCPManager.PendingApprovals(); len(pending) > 0 {
			cmds = append(cmds, func() tea.Msg {
				return mcpApprovalMsg{pending: pending}
			})
		}
	}
	return tea.Batch(cmds...)
}

// mcpApprovalMsg is sent on startup when project-scope MCP servers need
// user approval. The Update handler opens a picker per server, sequentially.
type mcpApprovalMsg struct {
	pending []string
}

// coordTickMsg fires every second whenever active sub-agent tasks exist,
// so the coordinator footer panel re-renders with updated elapsed times.
type coordTickMsg struct{}


// coordTick schedules the next coordinator tick — only resubscribes when
// there's still at least one in_progress task to display.
func coordTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return coordTickMsg{} })
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

	case tea.InterruptMsg:
		// bubbletea v2 sends InterruptMsg when it catches SIGINT (ctrl+c
		// in non-raw-mode or from kill). Mirror the KeyPressMsg ctrl+c
		// handler: cancel a running turn, or quit when idle.
		if m.running && m.cancelTurn != nil {
			m.cancelTurn()
			m.running = false
			m.cancelTurn = nil
			m.cancelled = true
			if m.streaming != "" {
				m.messages = append(m.messages, Message{Role: RoleAssistant, Content: m.streaming})
				m.streaming = ""
			}
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Interrupted."})
			m.refreshViewport()
			m.input.Focus()
			return m, nil
		}
		return m, tea.Quit

	case tea.PasteMsg:
		// Bracketed paste arrives as a single event in bubbletea v2.
		// Normalize line endings: terminals may send \r\n or bare \r.
		hasOverlay := m.loginPrompt != nil || m.resumePrompt != nil ||
			m.panel != nil || m.pluginPanel != nil || m.settingsPanel != nil ||
			m.permPrompt != nil || m.picker != nil || m.onboarding != nil
		if !hasOverlay {
			content := strings.ReplaceAll(msg.Content, "\r\n", "\n")
			content = strings.ReplaceAll(content, "\r", "\n")
			lineCount := strings.Count(content, "\n") + 1
			isLarge := lineCount > 1 || len(content) > 300
			if isLarge {
				// Store raw content and insert a removable placeholder token.
				// Mirrors CC's "[Pasted text #N +X lines]" UX. The placeholder
				// is a single pseudo-word so backspace removes it whole.
				m.pastedSeq++
				seq := m.pastedSeq
				if m.pastedBlocks == nil {
					m.pastedBlocks = map[int]string{}
				}
				m.pastedBlocks[seq] = content
				placeholder := fmt.Sprintf("[Pasted text #%d +%d lines]", seq, lineCount)
				m.input.InsertString(placeholder)
				m.flashMsg = fmt.Sprintf("Pasted %d lines  (Esc to clear)", lineCount)
				return m, tea.Tick(3*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} })
			} else {
				m.input.InsertString(content)
			}
		}
		return m, nil

	case tea.KeyPressMsg:
		m2, cmd, consumed := m.handleKey(msg)
		m = m2
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if consumed {
			// Key was fully handled — skip textarea/viewport so the raw key
			// doesn't also move the textarea cursor or scroll the viewport.
			if !m.running && m.cfg.Commands != nil {
				m.cmdMatches, m.cmdSelected = m.computeCommandMatches()
			}
			if !m.running {
				m.atMatches, m.atSelected = m.computeAtMatches()
			}
			return m, tea.Batch(cmds...)
		}
		// Not consumed — fall through so textarea and viewport get the key.

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
			// Persist new messages + cost snapshot to the session transcript.
			m.persistNewMessages(msg.history)
			if m.cfg.Session != nil && m.totalInputTokens > 0 {
				_ = m.cfg.Session.AppendCost(m.totalInputTokens, m.totalOutputTokens, m.costUSD)
			}
			// Companion speech bubble: if the final assistant text is a single
			// short line (≤ 100 chars) and the user addressed the companion by
			// name, show it in the corner bubble instead of (in addition to)
			// the conversation history.
			cmds = append(cmds, m.maybeFireCompanionBubble())
		}
		// Final assistant message just committed — refreshViewport's
		// sticky-bottom honors a scrolled-up user. They explicitly
		// scrolled away while results were streaming; don't yank them
		// back when the turn finalizes.
		m.refreshViewport()
		m.input.Focus()
		return m, tea.Batch(cmds...)

	case loginStartMsg:
		useClaudeAI := msg.claudeAI
		prog := *m.cfg.Program
		return m, func() tea.Msg {
			display := &tuiLoginDisplay{prog: prog}
			err := runLoginFlow(useClaudeAI, display)
			return loginDoneMsg{err: err}
		}

	case loginURLMsg:
		var sb strings.Builder
		sb.WriteString("Opening browser to sign in.\n")
		sb.WriteString("If the browser doesn't open, paste this URL:\n\n")
		sb.WriteString("  " + msg.automatic + "\n\n")
		sb.WriteString("Or, for a code-paste flow:\n\n")
		sb.WriteString("  " + msg.manual)
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: sb.String()})
		m.refreshViewport()
		m.vp.GotoBottom()
		return m, nil

	case loginBrowserFailMsg:
		m.messages = append(m.messages, Message{
			Role:    RoleSystem,
			Content: fmt.Sprintf("Couldn't open browser (%v). Paste the URL above.", msg.err),
		})
		m.refreshViewport()
		return m, nil

	case loginDoneMsg:
		if msg.err != nil {
			// Strip the ephemeral "Opening browser…" / URL messages on failure too.
			if m.loginFlowMsgStart >= 0 && m.loginFlowMsgStart < len(m.messages) {
				m.messages = m.messages[:m.loginFlowMsgStart]
			}
			m.loginFlowMsgStart = -1
			m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Login failed: %v", msg.err)})
			m.refreshViewport()
			m.vp.GotoBottom()
			return m, nil
		}
		// Strip all ephemeral login flow messages (picker, "Opening browser…", URLs).
		if m.loginFlowMsgStart >= 0 && m.loginFlowMsgStart < len(m.messages) {
			m.messages = m.messages[:m.loginFlowMsgStart]
		}
		m.loginFlowMsgStart = -1
		m.noAuth = false
		// Reload credentials, swap the API client, then show the welcome card.
		if m.cfg.LoadAuth != nil && m.cfg.NewAPIClient != nil && m.cfg.Loop != nil {
			return m, func() tea.Msg {
				ctx := context.Background()
				bearer, prof, err := m.cfg.LoadAuth(ctx)
				if err != nil {
					return authReloadMsg{err: err}
				}
				return authReloadMsg{client: m.cfg.NewAPIClient(bearer), profile: prof}
			}
		}
		m.messages = append(m.messages, m.welcomeCard())
		m.refreshViewport()
		m.vp.GotoBottom()
		return m, nil

	case authReloadMsg:
		if msg.err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Could not reload credentials: %v", msg.err)})
		} else if msg.client != nil {
			m.cfg.Loop.SetClient(msg.client)
			if msg.profile != nil {
				m.cfg.Profile = *msg.profile
			}
			m.messages = append(m.messages, m.welcomeCard())
		}
		m.refreshViewport()
		m.vp.GotoBottom()
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

	case resumePickMsg:
		if len(msg.sessions) == 0 {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "No previous sessions found for this directory."})
			m.refreshViewport()
			return m, nil
		}
		m.resumePrompt = &resumePromptState{sessions: msg.sessions, selected: 0}
		m.refreshViewport()
		return m, nil

	case coordTickMsg:
		// Re-arm the tick whenever there's still active work to display.
		// When idle, we let it fall off — next sub-agent run schedules a
		// fresh tick from wherever the work was kicked off (TaskCreate
		// could call coordTick if needed, but the spinner tick is already
		// running during agent.Run so we don't lose ticks during work).
		hasActive := false
		for _, t := range tasktool.GlobalStore().List() {
			if t.Status == tasktool.StatusInProgress {
				hasActive = true
				break
			}
		}
		if hasActive {
			cmds = append(cmds, coordTick())
		}
		return m, tea.Batch(cmds...)

	case mcpApprovalMsg:
		// Open a 3-option picker for the first pending server. Once
		// resolved (via the /mcp-approve handler invoked by the picker),
		// we re-check PendingApprovals and queue the next one.
		if len(msg.pending) == 0 {
			return m, nil
		}
		name := msg.pending[0]
		m.picker = &pickerState{
			kind:  "mcp-approve",
			title: fmt.Sprintf("Approve MCP server %q from .mcp.json?", name),
			items: []pickerItem{
				{Value: name + " yes", Label: "Yes — approve this server"},
				{Value: name + " yes_all", Label: "Yes, all project servers"},
				{Value: name + " no", Label: "No — deny and don't ask again"},
			},
			selected: 0,
		}
		m.refreshViewport()
		return m, nil

	case resumeLoadMsg:
		// Remove the "Loading session…" message.
		if len(m.messages) > 0 && m.messages[len(m.messages)-1].Content == "Loading session…" {
			m.messages = m.messages[:len(m.messages)-1]
		}
		if msg.err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Failed to load session: %v", msg.err)})
			m.refreshViewport()
			return m, nil
		}
		// Replace current history and rebuild display.
		m.history = msg.msgs
		m.persistedCount = len(msg.msgs)
		m.messages = append(m.messages, Message{
			Role:    RoleSystem,
			Content: fmt.Sprintf("Resumed previous conversation (%d messages). ↑ scroll to see history.", len(msg.msgs)),
		})
		for _, apiMsg := range msg.msgs {
			m.messages = append(m.messages, historyToDisplayMessage(apiMsg))
		}
		m.tallyTokens()
		m.refreshViewport()
		m.vp.GotoBottom()
		return m, nil

	case spinner.TickMsg:
		var spCmd tea.Cmd
		m.spinner, spCmd = m.spinner.Update(msg)
		cmds = append(cmds, spCmd)

	case clearFlash:
		m.flashMsg = ""
		return m, nil

	case clearBubble:
		m.companionBubble = ""
		return m, nil

	case companionBubbleMsg:
		m.companionBubble = msg.text
		return m, tea.Tick(10*time.Second, func(_ time.Time) tea.Msg { return clearBubble{} })

	case setPermissionModeMsg:
		m.permissionMode = msg.mode
		if m.cfg.Gate != nil {
			m.cfg.Gate.SetMode(msg.mode)
		}
		m.syncLive()
		return m, nil

	case setModelNameMsg:
		m.modelName = msg.name
		m.fastMode = msg.fast
		m.syncLive()
		return m, nil

	case pluginCountsMsg:
		if m.pluginPanel != nil && msg.err == nil {
			m.pluginPanel.loadingCounts = false
			m.pluginPanel.applyInstallCounts(msg.counts)
		}
		return m, nil

	case pluginInstallMsg:
		if m.pluginPanel != nil {
			p := m.pluginPanel
			if msg.err != nil {
				p.errors = append(p.errors, fmt.Sprintf("install %s: %v", msg.pluginID, msg.err))
				return m, nil
			}
			// Reload full panel from disk so version/description/sort are correct.
			return m, reloadPluginPanelCmd(m.cfg.MCPManager, p.tab, p.errors)
		}
		return m, nil

	case pluginPanelReloadMsg:
		if m.pluginPanel != nil {
			newPanel := rebuildPluginPanel(msg)
			newPanel.selected = 0
			m.pluginPanel = newPanel
			return m, func() tea.Msg {
				counts, err := plugins.LoadInstallCounts()
				return pluginCountsMsg{counts: counts, err: err}
			}
		}
		return m, nil

	case settingsStatsMsg:
		if m.settingsPanel != nil {
			m.settingsPanel.statsData = msg.stats
			m.settingsPanel.statsLoaded = true
			m.refreshViewport()
		}
		return m, nil

	}

	// Propagate remaining messages to sub-components.
	// Skip textarea/viewport when an overlay is active — they must not
	// consume keys (especially Escape) that belong to the overlay.
	overlayActive := m.loginPrompt != nil || m.resumePrompt != nil ||
		m.panel != nil || m.pluginPanel != nil || m.settingsPanel != nil || m.permPrompt != nil ||
		m.picker != nil || m.onboarding != nil
	var taCmd, vpCmd tea.Cmd
	if !overlayActive {
		prevLines := m.input.LineCount()
		// Pre-grow before a newline insert. Without this, bubbles textarea
		// receives the insert at the old Height=N, repositionView scrolls
		// the viewport down by 1 to keep the cursor visible, and the
		// textarea's internal YOffset becomes 1. A later SetHeight(N+1)
		// only grows the *capacity* — it doesn't reset YOffset, so the
		// first row stays scrolled offscreen. Pre-growing means the insert
		// happens with capacity already in place: cursor on row N is still
		// within [YOffset=0, YOffset+Height-1=N], no scroll fires.
		if k, ok := msg.(tea.KeyPressMsg); ok && isNewlineInsertKey(k) {
			nextLines := m.input.LineCount() + 1
			cap := chromeHeight(nextLines, m.height) - chromeFixed
			if nextLines <= cap {
				m.input.SetHeight(nextLines)
			}
		}
		m.input, taCmd = m.input.Update(msg)
		// If the input grew or shrunk a line (Alt+Enter, backspace into a
		// newline boundary, paste of multi-line content), reflow the
		// viewport so chat doesn't get squeezed by a now-taller input.
		if m.input.LineCount() != prevLines {
			m = m.applyLayout()
			m.refreshViewport()
		}
		m.vp, vpCmd = m.vp.Update(msg)
	}
	cmds = append(cmds, taCmd, vpCmd)

	// Recompute command picker matches after every key so the list stays live.
	if !m.running && m.cfg.Commands != nil {
		m.cmdMatches, m.cmdSelected = m.computeCommandMatches()
	}
	// Recompute @ file picker matches after every key.
	if !m.running {
		m.atMatches, m.atSelected = m.computeAtMatches()
	}

	return m, tea.Batch(cmds...)
}

// handleKey processes a key event. The bool return indicates whether the key
// was fully consumed (true = skip textarea/viewport propagation).
// handleKey is the top-level key dispatcher. It runs overlay intercepts,
// then the keybinding resolver, then falls through to handleKeyBuiltins.
func (m Model) handleKey(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	if m.loginPrompt != nil {
		m2, cmd := m.handleLoginKey(msg)
		return m2, cmd, true
	}
	// Resume picker intercepts all keys when active.
	if m.resumePrompt != nil {
		m2, cmd := m.handleResumeKey(msg)
		return m2, cmd, true
	}
	// Generic picker (/theme /model /output-style) intercepts keys.
	if m.picker != nil {
		m2, cmd := m.handlePickerKey(msg)
		return m2, cmd, true
	}
	// First-run onboarding intercepts keys until dismissed.
	if m.onboarding != nil {
		m2, cmd := m.handleOnboardingKey(msg)
		return m2, cmd, true
	}
	// Unified panel intercepts all keys when active.
	if m.panel != nil {
		m2, cmd := m.handlePanelKey(msg)
		return m2, cmd, true
	}
	// Plugin panel intercepts all keys when active.
	if m.pluginPanel != nil {
		m2, cmd := m.handlePluginPanelKey(msg)
		return m2, cmd, true
	}
	// Settings panel intercepts all keys when active.
	if m.settingsPanel != nil {
		m2, cmd, consumed := m.handleSettingsPanelKey(msg)
		return m2, cmd, consumed
	}
	// Permission prompt intercepts all keys when active.
	if m.permPrompt != nil {
		m2, cmd := m.handlePermissionKey(msg)
		return m2, cmd, true
	}

	// User-customizable keybindings. Checked after overlay handlers so
	// modal overlays always own their own key space. "command:*" actions
	// execute the named slash command directly; other action IDs not yet
	// handled here fall through to the built-in switch below.
	if m.kb != nil {
		contexts := m.activeContexts()
		if res := m.kb.Resolve(msg, contexts...); res.Matched {
			if res.Unbound {
				// Explicit null — swallow key, skip built-ins.
				return m, nil, true
			}
			if m2, cmd, ok := m.dispatchKeybindingAction(res.Action, msg); ok {
				return m2, cmd, true
			}
		}
	}

	return m.handleKeyBuiltins(msg)
}

// handleKeyBuiltins is the built-in key handler. It never consults the
// keybinding resolver, which means dispatchKeybindingAction can safely
// call it for synthetic re-dispatches without triggering infinite recursion.
func (m Model) handleKeyBuiltins(msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	// Viewport scrollback. Plain Up/Down/PgUp/PgDn are owned by the chat
	// input (history navigation, multi-line cursor, textarea paging).
	// Users still need a way to scroll the chat transcript without
	// reaching for the mouse — so reserve Shift+Up/Shift+Down for
	// line-by-line scroll and Shift+PgUp/Shift+PgDn for page scroll.
	// Modern terminals running the Kitty keyboard protocol report
	// these as distinct keys; legacy terminals collapse Shift+arrow
	// onto plain arrow, which falls through to the existing handlers
	// below — so this is purely additive on capable terminals.
	switch msg.String() {
	case "shift+up":
		m.vp.ScrollUp(1)
		return m, nil, true
	case "shift+down":
		m.vp.ScrollDown(1)
		return m, nil, true
	case "shift+pgup", "pgup":
		m.vp.PageUp()
		return m, nil, true
	case "shift+pgdown", "pgdown":
		m.vp.PageDown()
		return m, nil, true
	}

	switch msg.String() {
	case "up":
		if len(m.cmdMatches) > 0 {
			if m.cmdSelected > 0 {
				m.cmdSelected--
			}
			return m, nil, true
		}
		if len(m.atMatches) > 0 {
			if m.atSelected > 0 {
				m.atSelected--
			}
			return m, nil, true
		}
		// History: navigate backwards (older).
		if len(m.inputHistory) > 0 && !m.running {
			if m.historyIdx == -1 {
				m.historyDraft = m.input.Value()
				m.historyIdx = len(m.inputHistory) - 1
			} else if m.historyIdx > 0 {
				m.historyIdx--
			}
			m.input.SetValue(m.inputHistory[m.historyIdx])
			m.input.CursorEnd()
			return m, nil, true
		}

	case "down":
		if len(m.cmdMatches) > 0 {
			if m.cmdSelected < len(m.cmdMatches)-1 {
				m.cmdSelected++
			}
			return m, nil, true
		}
		if len(m.atMatches) > 0 {
			if m.atSelected < len(m.atMatches)-1 {
				m.atSelected++
			}
			return m, nil, true
		}
		// History: navigate forwards (newer / back to draft).
		if m.historyIdx != -1 && !m.running {
			if m.historyIdx < len(m.inputHistory)-1 {
				m.historyIdx++
				m.input.SetValue(m.inputHistory[m.historyIdx])
			} else {
				m.historyIdx = -1
				m.input.SetValue(m.historyDraft)
			}
			m.input.CursorEnd()
			return m, nil, true
		}

	case "shift+tab":
		// Cycle: default → acceptEdits → plan → bypassPermissions → default.
		// Mirrors getNextPermissionMode.ts from real Claude Code.
		switch m.permissionMode {
		case "", permissions.ModeDefault:
			m.permissionMode = permissions.ModeAcceptEdits
		case permissions.ModeAcceptEdits:
			m.permissionMode = permissions.ModePlan
		case permissions.ModePlan:
			m.permissionMode = permissions.ModeBypassPermissions
		default:
			m.permissionMode = permissions.ModeDefault
		}
		if m.cfg.Gate != nil {
			m.cfg.Gate.SetMode(m.permissionMode)
		}
		m.syncLive()
		switch m.permissionMode {
		case permissions.ModeAcceptEdits:
			m.flashMsg = "⏵⏵ accept edits on (shift+tab to cycle)"
		case permissions.ModePlan:
			m.flashMsg = "⏸ plan mode on (shift+tab to cycle)"
		case permissions.ModeBypassPermissions:
			m.flashMsg = "⏵⏵ auto mode on (shift+tab to cycle)"
		default:
			m.flashMsg = "default mode (shift+tab to cycle)"
		}
		return m, tea.Tick(1500*time.Millisecond, func(_ time.Time) tea.Msg { return clearFlash{} }), true

	case "tab", "esc":
		// Clear pending images and paste placeholders on Esc.
		if msg.String() == "esc" {
			if len(m.pendingImages) > 0 || len(m.pastedBlocks) > 0 {
				n := len(m.pendingImages)
				m.pendingImages = nil
				m.pastedBlocks = nil
				m.input.SetValue(rePasteToken.ReplaceAllString(m.input.Value(), ""))
				if n > 0 {
					m.flashMsg = fmt.Sprintf("%d image(s) and paste(s) cleared.", n)
				} else {
					m.flashMsg = "Paste(s) cleared."
				}
				return m, tea.Tick(1500*time.Millisecond, func(_ time.Time) tea.Msg { return clearFlash{} }), true
			}
		}
		if len(m.atMatches) > 0 {
			if msg.String() == "tab" || msg.String() == "esc" {
				if msg.String() == "tab" {
					m = m.acceptAtMatch()
				} else {
					m.atMatches = nil
					m.atSelected = 0
				}
				return m, nil, true
			}
		}
		if len(m.cmdMatches) > 0 {
			if msg.String() == "tab" {
				// Tab: complete to the command name with trailing space, close picker.
				m.input.SetValue("/" + m.cmdMatches[m.cmdSelected].Name + " ")
				m.input.CursorEnd()
			}
			m.cmdMatches = nil
			m.cmdSelected = 0
			return m, nil, true
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
			return m, nil, true
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
			return m, nil, true
		}
		return m, tea.Quit, true

	case "ctrl+v":
		// Synchronous clipboard image check (~50ms on macOS via osascript).
		// If clipboard has an image, attach it and consume the key.
		// If clipboard has text (not an image), return consumed=false so
		// the textarea handles normal text paste.
		img, err := attach.ReadClipboardImage()
		switch {
		case err == nil && img != nil:
			m.pendingImages = append(m.pendingImages, img)
			n := len(m.pendingImages)
			m.flashMsg = fmt.Sprintf("📎 %d image(s) attached  (ctrl+v for more · Enter to send · Esc to clear)", n)
			return m, tea.Tick(5*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} }), true
		case errors.Is(err, attach.ErrNotSupported):
			// Platform doesn't support image paste — let textarea handle ctrl+v normally.
			return m, nil, false
		default:
			// ErrNoImage or osascript error — clipboard likely has text.
			// Fall through to textarea for normal text paste.
			return m, nil, false
		}

	case "backspace":
		// If the cursor is immediately after a paste placeholder token, delete
		// the entire token in one keystroke (mirroring how CC handles it).
		if len(m.pastedBlocks) > 0 {
			val := m.input.Value()
			col := m.input.Column()
			// Determine the rune position of the cursor within the full value
			// by walking lines up to the current line+col.
			line := m.input.Line()
			pos := 0
			for i, l := range strings.Split(val, "\n") {
				if i == line {
					pos += col
					break
				}
				pos += len(l) + 1 // +1 for the \n
			}
			// Look for any paste token ending exactly at pos.
			prefix := val[:pos]
			if strings.HasSuffix(prefix, "]") {
				loc := rePasteToken.FindStringIndex(prefix)
				if loc != nil && loc[1] == len(prefix) {
					// Found a token ending at the cursor — extract its seq#,
					// delete it from the input, and remove from pastedBlocks.
					token := prefix[loc[0]:]
					if sub := rePasteToken.FindStringSubmatch(token); len(sub) == 2 {
						seq, _ := strconv.Atoi(sub[1])
						delete(m.pastedBlocks, seq)
					}
					newVal := val[:loc[0]] + val[pos:]
					m.input.SetValue(newVal)
					// SetValue leaves cursor at the end. Reposition to loc[0]
					// (where the token was) when there's text after it.
					if val[pos:] != "" {
						prefix2 := newVal[:loc[0]]
						targetLine := strings.Count(prefix2, "\n")
						lines2 := strings.Split(prefix2, "\n")
						targetCol := len(lines2[len(lines2)-1])
						m.input.CursorStart()
						for i := 0; i < targetLine; i++ {
							m.input.CursorDown()
						}
						m.input.SetCursorColumn(targetCol)
					}
					return m, nil, true
				}
			}
		}

	case "ctrl+y":
		// Copy the raw code from the most recent assistant code block to
		// the system clipboard via OSC 52 (works in iTerm2, kitty, WezTerm).
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role == RoleAssistant {
				blocks := extractCodeBlocks(m.messages[i].Content)
				if len(blocks) > 0 {
					copyToClipboard(blocks[len(blocks)-1].code)
					m.flashMsg = "✓ Copied to clipboard"
					return m, tea.Tick(2000000000, func(_ time.Time) tea.Msg { return clearFlash{} }), true
				}
			}
		}
		m.flashMsg = "No code block found"
		return m, tea.Tick(1500000000, func(_ time.Time) tea.Msg { return clearFlash{} }), true

	case "enter":
		if m.running {
			return m, nil, true
		}

		// If the @ file picker is open, accept selected path.
		if len(m.atMatches) > 0 {
			m = m.acceptAtMatch()
			return m, nil, true
		}

		// If the command picker is open, dispatch the selected command immediately.
		if len(m.cmdMatches) > 0 {
			selected := m.cmdMatches[m.cmdSelected]
			m.cmdMatches = nil
			m.cmdSelected = 0
			m.input.Reset()
			m.dismissWelcome()
			if m.cfg.Commands != nil {
				if res, ok := m.cfg.Commands.Dispatch("/" + selected.Name); ok {
					m2, cmd := m.applyCommandResult(res)
					return m2, cmd, true
				}
			}
			return m, nil, true
		}

		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil, true
		}

		// Dispatch slash commands before sending to the agent.
		if strings.HasPrefix(text, "/") {
			m.dismissWelcome()
			m.input.Reset()
			if m.cfg.Commands != nil {
				if res, ok := m.cfg.Commands.Dispatch(text); ok {
					m2, cmd := m.applyCommandResult(res)
					return m2, cmd, true
				}
			}
			m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Unknown command: %s (try /help)", text)})
			m.refreshViewport()
			return m, nil, true
		}

		// Reject messages when not authenticated.
		if m.noAuth {
			m.messages = append(m.messages, Message{
				Role:    RoleError,
				Content: "Not logged in. Use /login to sign in first.",
			})
			m.input.Reset()
			m.refreshViewport()
			m.vp.GotoBottom()
			return m, nil, true
		}

		m.dismissWelcome()
		m.input.Reset()
		// Append to history only if it differs from the last entry.
		if len(m.inputHistory) == 0 || m.inputHistory[len(m.inputHistory)-1] != text {
			m.inputHistory = append(m.inputHistory, text)
		}
		m.historyIdx = -1
		m.historyDraft = ""
		m.messages = append(m.messages, Message{Role: RoleUser, Content: text})
		// Expand paste placeholders before sending to the API.
		// The textarea holds "[Pasted text #N +X lines]" tokens; the agent
		// receives the raw pasted content. After expansion, clear the map.
		apiText := m.expandPastePlaceholders(text)
		m.pastedBlocks = nil

		// Build user message content. Prepend any queued images as image
		// blocks so Claude sees screenshot(s) alongside the text. CC
		// supports multiple images; we mirror that by accumulating on
		// each ctrl+v and sending all at once on Enter.
		userContent := make([]api.ContentBlock, 0, len(m.pendingImages)+1)
		for _, img := range m.pendingImages {
			userContent = append(userContent, api.ContentBlock{
				Type: "image",
				Source: &api.ImageSource{
					Type:      "base64",
					MediaType: img.MediaType,
					Data:      img.Data,
				},
			})
		}
		m.pendingImages = nil
		// Process @file mentions: inject referenced file/dir contents as
		// additional text blocks before the user's message text.
		if cwd, err := os.Getwd(); err == nil {
			for _, ref := range attach.ProcessAtMentions(apiText, cwd) {
				userContent = append(userContent, api.ContentBlock{
					Type: "text",
					Text: attach.FormatAtResult(ref),
				})
			}
		}
		userContent = append(userContent, api.ContentBlock{Type: "text", Text: apiText})
		m.history = append(m.history, api.Message{
			Role:    "user",
			Content: userContent,
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
		}, true
	}
	return m, nil, false
}

// expandPastePlaceholders replaces "[Pasted text #N +X lines]" tokens in s
// with the raw content from m.pastedBlocks. Tokens with no matching entry
// are left as-is (shouldn't happen in practice).
func (m Model) expandPastePlaceholders(s string) string {
	if len(m.pastedBlocks) == 0 {
		return s
	}
	return rePasteToken.ReplaceAllStringFunc(s, func(tok string) string {
		sub := rePasteToken.FindStringSubmatch(tok)
		if len(sub) != 2 {
			return tok
		}
		seq, err := strconv.Atoi(sub[1])
		if err != nil {
			return tok
		}
		if raw, ok := m.pastedBlocks[seq]; ok {
			return raw
		}
		return tok
	})
}

// maybeFireCompanionBubble checks if the last assistant message should be
// displayed as a companion speech bubble. Fires when all of:
//   - A companion is configured (buddy store has a name)
//   - The last user message mentioned the companion by name
//   - The last assistant message is brief (≤ 4 lines, ≤ 200 chars total)
//
// Multi-line companion quips (e.g. "*action*\n\nReply") are allowed —
// CC shows them in the bubble too.
//
// Mirrors the fireCompanionObserver logic from src/screens/REPL.tsx.
func (m Model) maybeFireCompanionBubble() tea.Cmd {
	sc, err := buddy.Load()
	if err != nil || sc == nil || sc.Name == "" {
		return nil
	}
	// Find last user message and check if it mentions the companion name.
	companionName := strings.ToLower(sc.Name)
	for i := len(m.messages) - 1; i >= 0; i-- {
		msg := m.messages[i]
		if msg.Role == RoleUser {
			if !strings.Contains(strings.ToLower(msg.Content), companionName) {
				return nil // user didn't address the companion
			}
			break
		}
		if msg.Role == RoleAssistant {
			break // assistant responded but no user turn above — skip
		}
	}
	// Find last assistant message.
	for i := len(m.messages) - 1; i >= 0; i-- {
		msg := m.messages[i]
		if msg.Role != RoleAssistant {
			continue
		}
		text := strings.TrimSpace(msg.Content)
		if text == "" {
			return nil
		}
		lines := strings.Split(text, "\n")
		// Blank-line normalized count (paragraphs).
		var nonBlank int
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				nonBlank++
			}
		}
		if nonBlank > 4 || len(text) > 200 {
			return nil // too long — regular response, not a quip
		}
		return func() tea.Msg {
			return companionBubbleMsg{text: text}
		}
	}
	return nil
}

// companionBubbleMsg is sent when the companion should speak.
type companionBubbleMsg struct{ text string }

// renderCompanionBubble renders a speech bubble with the companion face.
// The face and bubble box are joined horizontally so they align properly.
// Returns "" when no bubble is active or companion not configured.
func (m Model) renderCompanionBubble() string {
	if m.companionBubble == "" {
		return ""
	}
	sc, err := buddy.Load()
	if err != nil || sc == nil {
		return ""
	}
	bones := buddy.GenerateBones(sc.UserID)
	face := buddy.RenderFace(bones)

	const maxW = 28
	// Word-wrap the text to maxW columns.
	var wrapped []string
	words := strings.Fields(m.companionBubble)
	var cur string
	for _, w := range words {
		if cur == "" {
			cur = w
		} else if lipgloss.Width(cur)+1+lipgloss.Width(w) <= maxW {
			cur += " " + w
		} else {
			wrapped = append(wrapped, cur)
			cur = w
		}
	}
	if cur != "" {
		wrapped = append(wrapped, cur)
	}

	// Build the speech bubble text block.
	textStyle := lipgloss.NewStyle().Foreground(colorFg).Italic(true)
	var rows []string
	for _, l := range wrapped {
		rows = append(rows, textStyle.Render(l))
	}
	bubbleStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		PaddingLeft(1).PaddingRight(1).
		Width(maxW + 2)
	bubble := bubbleStyle.Render(strings.Join(rows, "\n"))

	// Join face (center-aligned vertically) + bubble side by side.
	faceStyle := lipgloss.NewStyle().PaddingRight(1).PaddingTop(1)
	return lipgloss.JoinHorizontal(lipgloss.Center, faceStyle.Render(face), bubble)
}

// renderCommandPicker renders the slash command picker dropdown.
func (m Model) renderCommandPicker() string {
	const maxItems = 8
	// Cap total line width so the picker stays readable.
	const maxLineW = 80

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

	// Compute name column width from the longest name in the visible window,
	// capped so descriptions always get at least 20 chars.
	nameColW := 0
	for i := start; i < end; i++ {
		n := len([]rune(m.cmdMatches[i].Name)) + 1 // +1 for leading "/"
		if n > nameColW {
			nameColW = n
		}
	}
	lineW := m.width - 4 // account for picker border padding
	if lineW > maxLineW {
		lineW = maxLineW
	}
	const minDescW = 20
	const gap = 2
	if nameColW > lineW-minDescW-gap {
		nameColW = lineW - minDescW - gap
	}
	descMax := lineW - nameColW - gap

	var sb strings.Builder
	for i := start; i < end; i++ {
		cmd := m.cmdMatches[i]

		// Render name: "/" + name left-padded to nameColW.
		rawName := "/" + cmd.Name
		runes := []rune(rawName)
		if len(runes) > nameColW {
			runes = runes[:nameColW]
		}
		rawName = string(runes) + strings.Repeat(" ", nameColW-len(runes))

		var namePart string
		if i == m.cmdSelected {
			namePart = highlightMatch(rawName, query, stylePickerItemSelected, stylePickerHighlight)
		} else {
			namePart = highlightMatch(rawName, query, stylePickerItem, stylePickerHighlight)
		}

		// Render description, ellipsized to fit.
		desc := cmd.Description
		if descMax > 4 && len([]rune(desc)) > descMax {
			desc = string([]rune(desc)[:descMax-1]) + "…"
		}
		descPart := highlightMatch(desc, query, stylePickerDesc, stylePickerHighlight)

		sb.WriteString(namePart + strings.Repeat(" ", gap) + descPart)
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
	// Rank matches: 0 = name prefix, 1 = name contains, 2 = description contains.
	// Stable within each rank to preserve alphabetical order from Registry.All().
	type ranked struct {
		cmd  commands.Command
		rank int
	}
	var rs []ranked
	for _, c := range all {
		if c.Name == "quit" {
			continue
		}
		switch {
		case strings.HasPrefix(c.Name, query):
			rs = append(rs, ranked{c, 0})
		case strings.Contains(c.Name, query):
			rs = append(rs, ranked{c, 1})
		case strings.Contains(strings.ToLower(c.Description), query):
			rs = append(rs, ranked{c, 2})
		}
	}
	// Stable sort by rank only; alphabetical order within rank is preserved.
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].rank < rs[j].rank })
	matches := make([]commands.Command, len(rs))
	for i, r := range rs {
		matches[i] = r.cmd
	}
	// Preserve selection if the same set, otherwise reset.
	sel := m.cmdSelected
	if sel >= len(matches) {
		sel = 0
	}
	return matches, sel
}

// --- @ file completion ---

// atFragment returns the partial @query at the end of the current input
// (i.e. the last whitespace-delimited token if it starts with "@").
// Returns "" if no @ fragment is present.
func atFragment(val string) string {
	// Get the last non-space token.
	idx := strings.LastIndexAny(val, " \t\n")
	token := val
	if idx >= 0 {
		token = val[idx+1:]
	}
	if strings.HasPrefix(token, "@") {
		return token[1:] // strip the @
	}
	return ""
}

// computeAtMatches returns file/dir paths matching the current @fragment.
// Returns (nil,0) when no @ fragment is active or the input starts with "/".
func (m Model) computeAtMatches() ([]string, int) {
	val := m.input.Value()
	if strings.HasPrefix(val, "/") || m.running {
		return nil, 0
	}
	// Find the last token in the input.
	lastIdx := strings.LastIndexAny(val, " \t\n")
	lastToken := val
	if lastIdx >= 0 {
		lastToken = val[lastIdx+1:]
	}
	if !strings.HasPrefix(lastToken, "@") {
		return nil, 0 // no @ at end of input
	}
	frag := lastToken[1:] // query after @

	cwd, _ := os.Getwd()
	matches := searchFiles(cwd, frag, 8)
	if len(matches) == 0 {
		return nil, 0
	}
	sel := m.atSelected
	if sel >= len(matches) {
		sel = 0
	}
	return matches, sel
}

// acceptAtMatch inserts the selected @ match into the input, replacing the
// current @ fragment.
func (m Model) acceptAtMatch() Model {
	if len(m.atMatches) == 0 {
		return m
	}
	chosen := m.atMatches[m.atSelected]
	val := m.input.Value()
	// Find the @ token at the end and replace it.
	idx := strings.LastIndexAny(val, " \t\n")
	var prefix string
	if idx >= 0 {
		prefix = val[:idx+1]
	}
	// Construct the replacement: @path + space (so user can keep typing).
	replacement := "@" + chosen + " "
	// Quote paths with spaces.
	if strings.Contains(chosen, " ") {
		replacement = `@"` + chosen + `" `
	}
	m.input.SetValue(prefix + replacement)
	m.input.CursorEnd()
	m.atMatches = nil
	m.atSelected = 0
	return m
}

// searchFiles returns up to max relative paths in dir matching query.
// It tries fd first (respects .gitignore), falls back to filepath.WalkDir.
func searchFiles(dir, query string, max int) []string {
	// Try fd (fast, respects .gitignore).
	if _, err := exec.LookPath("fd"); err == nil {
		args := []string{"--type", "f", "--type", "d", "--max-results", fmt.Sprintf("%d", max)}
		if query != "" {
			args = append(args, query)
		}
		out, err := exec.Command("fd", append(args, ".", dir)...).Output()
		if err == nil {
			var paths []string
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if line == "" {
					continue
				}
				rel, err := filepath.Rel(dir, line)
				if err == nil {
					paths = append(paths, rel)
				} else {
					paths = append(paths, line)
				}
				if len(paths) >= max {
					break
				}
			}
			if len(paths) > 0 {
				return paths
			}
		}
	}
	// Fallback: WalkDir with depth ≤ 3.
	queryLow := strings.ToLower(query)
	var paths []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || len(paths) >= max {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "." {
			return nil
		}
		// Skip hidden dirs, .git, node_modules, vendor at depth > 0.
		name := d.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Depth check: count separators.
		depth := strings.Count(rel, string(os.PathSeparator))
		if d.IsDir() && depth >= 3 {
			return filepath.SkipDir
		}
		if queryLow == "" || strings.Contains(strings.ToLower(name), queryLow) {
			paths = append(paths, rel)
		}
		return nil
	})
	return paths
}

// renderAtPicker renders the @ file completion picker above the input box.
// Returns "" when no matches are active.
func (m Model) renderAtPicker() string {
	if len(m.atMatches) == 0 {
		return ""
	}
	const maxItems = 8
	cwd, _ := os.Getwd()
	start := m.atSelected - maxItems/2
	if start < 0 {
		start = 0
	}
	end := start + maxItems
	if end > len(m.atMatches) {
		end = len(m.atMatches)
		start = end - maxItems
		if start < 0 {
			start = 0
		}
	}

	var sb strings.Builder
	for i := start; i < end; i++ {
		path := m.atMatches[i]
		icon := "+"
		if info, err := os.Stat(filepath.Join(cwd, path)); err == nil && info.IsDir() {
			icon = "◇"
			path += "/"
		}
		line := fmt.Sprintf(" %s %s", icon, path)
		if i == m.atSelected {
			line = stylePickerItemSelected.Render(line)
		} else {
			line = stylePickerItem.Render(line)
		}
		sb.WriteString(line)
		if i < end-1 {
			sb.WriteByte('\n')
		}
	}
	return stylePickerBorder.Width(m.width - 2).Render(sb.String())
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

// resumeSession is one entry in the /resume picker.
type resumeSession struct {
	id       string
	filePath string
	age      string
	preview  string // first user message, truncated
}

// resumePromptState holds the /resume session picker state.
type resumePromptState struct {
	sessions []resumeSession
	filtered []int // indices into sessions matching filter
	filter   string
	selected int
}

// resumeFilter applies the current filter string and resets selection.
func (p *resumePromptState) applyFilter() {
	q := strings.ToLower(p.filter)
	p.filtered = p.filtered[:0]
	for i, s := range p.sessions {
		if q == "" || strings.Contains(strings.ToLower(s.preview), q) ||
			strings.Contains(strings.ToLower(s.age), q) {
			p.filtered = append(p.filtered, i)
		}
	}
	if p.selected >= len(p.filtered) {
		p.selected = 0
	}
}

func (m Model) handleResumeKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.resumePrompt
	switch msg.String() {
	case "up", "k":
		if p.selected > 0 {
			p.selected--
		}
	case "down", "j":
		if p.selected < len(p.filtered)-1 {
			p.selected++
		}
	case "enter", "space":
		if len(p.filtered) == 0 {
			break
		}
		picked := p.sessions[p.filtered[p.selected]]
		m.resumePrompt = nil
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Loading session…"})
		m.refreshViewport()
		filePath := picked.filePath
		return m, func() tea.Msg {
			msgs, err := session.LoadMessages(filePath)
			return resumeLoadMsg{msgs: msgs, err: err}
		}
	case "esc", "ctrl+c":
		if p.filter != "" {
			// Esc clears filter first, then second Esc cancels.
			p.filter = ""
			p.applyFilter()
			m.resumePrompt = p
			return m, nil
		}
		m.resumePrompt = nil
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Resume cancelled."})
		m.refreshViewport()
		return m, nil
	case "backspace":
		if len(p.filter) > 0 {
			p.filter = p.filter[:len([]rune(p.filter))-1]
			p.applyFilter()
		}
	default:
		// Any printable character updates the search filter.
		if r := []rune(msg.String()); len(r) == 1 && r[0] >= 0x20 {
			p.filter += string(r)
			p.applyFilter()
		}
	}
	m.resumePrompt = p
	return m, nil
}

func (m Model) renderResumePicker() string {
	p := m.resumePrompt
	if p == nil {
		return ""
	}
	var sb strings.Builder
	// Search line.
	if p.filter != "" {
		sb.WriteString(styleStatusAccent.Render("▶ "+p.filter) + "  " +
			stylePickerDesc.Render(fmt.Sprintf("(%d/%d)", len(p.filtered), len(p.sessions))) + "\n\n")
	} else {
		sb.WriteString(styleStatusAccent.Render("Resume a previous conversation") +
			"  " + stylePickerDesc.Render("type to search") + "\n\n")
	}

	const maxVisible = 12
	start := p.selected - maxVisible/2
	if start < 0 {
		start = 0
	}
	end := start + maxVisible
	if end > len(p.filtered) {
		end = len(p.filtered)
		start = end - maxVisible
		if start < 0 {
			start = 0
		}
	}

	if len(p.filtered) == 0 {
		sb.WriteString(stylePickerDesc.Render("  (no matches)") + "\n")
	}
	for vi := start; vi < end; vi++ {
		i := p.filtered[vi]
		s := p.sessions[i]
		label := s.age + "  " + s.preview
		var line string
		if vi == p.selected {
			line = stylePickerItemSelected.Render("▶ "+label)
		} else {
			line = stylePickerItem.Render("  " + label)
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString("\n" + stylePickerDesc.Render("↑↓/jk navigate · Enter load · Esc clear search · Ctrl+C cancel"))

	style := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		PaddingLeft(2).PaddingRight(2).PaddingTop(1).PaddingBottom(1)
	return style.Width(m.width - 4).Render(sb.String())
}


// ---- First-run onboarding overlay ------------------------------------------

// onboardingState is the first-run welcome shown until the user dismisses
// it with Enter. Mirrors the gating from src/components/Onboarding.tsx but
// trimmed to a single screen — conduit doesn't need CC's preflight,
// API-key, or terminal-setup steps inside the wizard (those are handled
// elsewhere or descoped).
type onboardingState struct {
	authenticated bool
	userName      string
}

func (m Model) handleOnboardingKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "enter", "esc":
		m.onboarding = nil
		// Persist so the welcome doesn't show on next launch. Best-effort —
		// a failure here just means the user sees the screen again, no data
		// loss, so silent.
		_ = settings.SaveRawKey("onboardingComplete", true)
		m.refreshViewport()
		return m, nil
	case "ctrl+c", "q":
		// Treat as exit so users can bail without persisting.
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) renderOnboarding() string {
	o := m.onboarding
	if o == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render("Welcome to conduit") + "\n\n")
	sb.WriteString("Conduit is a Go-native CLI for the Claude API — a port of the\n")
	sb.WriteString("official Claude Code with the same wire protocol, tool set, and\n")
	sb.WriteString("plugin/MCP system.\n\n")

	if o.authenticated {
		who := o.userName
		if who == "" {
			who = "your account"
		}
		sb.WriteString(stylePickerItem.Render("✓ Signed in as ") + styleStatusAccent.Render(who) + "\n\n")
	} else {
		sb.WriteString(stylePickerItem.Render("✗ Not signed in") + " — run " + styleStatusAccent.Render("/login") + " when ready.\n\n")
	}

	sb.WriteString("Useful commands:\n")
	sb.WriteString("  " + styleStatusAccent.Render("/help") + "    list all slash commands\n")
	sb.WriteString("  " + styleStatusAccent.Render("/login") + "   authenticate with your Anthropic account\n")
	sb.WriteString("  " + styleStatusAccent.Render("/theme") + "   pick a color palette\n")
	sb.WriteString("  " + styleStatusAccent.Render("/doctor") + "  diagnose auth / MCP / settings\n")
	sb.WriteString("  " + styleStatusAccent.Render("/quit") + "    exit\n\n")

	sb.WriteString(stylePickerDesc.Render("Press Enter to continue · Ctrl+C to exit"))

	style := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		PaddingLeft(2).PaddingRight(2).PaddingTop(1).PaddingBottom(1)
	return style.Width(m.width - 4).Render(sb.String())
}

// ---- Generic picker overlay (/theme /model /output-style) ------------------

// pickerItem is one row in a small selection picker.
// JSON tags let commands construct payloads with json.Marshal directly.
type pickerItem struct {
	Value string `json:"value"` // dispatched as `/<kind> <value>` on Enter
	Label string `json:"label"` // human-readable display
}

// pickerState drives the small select-one overlay used by /theme, /model,
// and /output-style. The picker has no awareness of what each kind does:
// on Enter it dispatches `/<kind> <value>` back through the command
// registry, so the underlying command does the actual work.
type pickerState struct {
	kind     string       // "theme" | "model" | "output-style"
	title    string       // header line
	items    []pickerItem // options (caller-ordered)
	selected int          // current cursor row
	current  string       // value to highlight as "active"
}

func (m Model) handlePickerKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.picker
	switch msg.String() {
	case "up", "k":
		if p.selected > 0 {
			p.selected--
		}
	case "down", "j":
		if p.selected < len(p.items)-1 {
			p.selected++
		}
	case "home", "g":
		p.selected = 0
	case "end", "G":
		p.selected = len(p.items) - 1
	case "enter", "space":
		if p.selected < 0 || p.selected >= len(p.items) {
			return m, nil
		}
		picked := p.items[p.selected].Value
		kind := p.kind
		m.picker = nil
		if m.cfg.Commands == nil {
			return m, nil
		}
		if res, ok := m.cfg.Commands.Dispatch("/" + kind + " " + picked); ok {
			return m.applyCommandResult(res)
		}
		return m, nil
	case "esc", "ctrl+c", "q":
		m.picker = nil
		m.refreshViewport()
		return m, nil
	}
	m.picker = p
	return m, nil
}

func (m Model) renderPicker() string {
	p := m.picker
	if p == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render(p.title) + "\n\n")

	for i, it := range p.items {
		marker := "  "
		if it.Value == p.current {
			marker = "● "
		}
		label := marker + it.Label
		if i == p.selected {
			sb.WriteString(stylePickerItemSelected.Render("▶ "+label) + "\n")
		} else {
			sb.WriteString(stylePickerItem.Render("  "+label) + "\n")
		}
	}
	sb.WriteString("\n" + stylePickerDesc.Render("↑↓/jk navigate · Enter select · Escape cancel"))

	style := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		PaddingLeft(2).PaddingRight(2).PaddingTop(1).PaddingBottom(1)
	return style.Width(m.width - 4).Render(sb.String())
}

// ---- Unified panel (MCP / Plugins / Marketplaces) --------------------------

type panelTab int

const (
	panelTabMCP panelTab = 0
)

var panelTabNames = []string{"MCP"}

// panelMCPItem is one MCP server row — carries all info for detail view.
type panelMCPItem struct {
	name      string
	scope     string // "User" | "Project" | "Built-in"
	source    string // config file or "plugin:name"
	status    string // "connected" | "failed" | "pending" | "disabled"
	command   string // stdio command or URL
	args      string // space-separated args
	toolCount int
	err       string
	disabled  bool
	// tools populated on-demand when detail is opened
	tools []panelToolItem
}

// panelToolItem is one tool inside a server detail.
type panelToolItem struct {
	name        string
	fullName    string // e.g. mcp__plugin_context7_context7__resolve-library-id
	description string
	schema      string // raw JSON schema for params display
}

// panelPluginItem is one plugin row.
type panelPluginItem struct {
	name        string
	description string
	cmdCount    int
}

// panelMarketplaceItem is one marketplace row.
type panelMarketplaceItem struct {
	name        string
	source      string
	lastUpdated string
}

// panelView is the navigation depth.
type panelView int

const (
	panelViewList       panelView = 0 // tab root list
	panelViewDetail     panelView = 1 // item detail (server/plugin/marketplace)
	panelViewTools      panelView = 2 // tool list inside a server
	panelViewToolDetail panelView = 3 // single tool detail
)

// panelState is the unified browser overlay.
type panelState struct {
	tab      panelTab
	view     panelView
	selected int // cursor within the current view (list row, action row, tool row)
	// serverIdx is preserved when drilling into detail/tools/tool-detail so
	// the render functions always know which server is being inspected.
	serverIdx int

	// raw encoded data
	mcpRaw    string
	pluginRaw string

	// parsed lists
	mcpItems         []panelMCPItem
	pluginItems      []panelPluginItem
	marketplaceItems []panelMarketplaceItem
}

func newPanel(tab panelTab) *panelState {
	return &panelState{tab: tab}
}

func (p *panelState) parseMCPItems() {
	p.mcpItems = nil
	for _, line := range strings.Split(p.mcpRaw, "\n") {
		if line == "" {
			continue
		}
		// name\tscope\tsource\tstatus\tcommand\targs\ttoolCount\terr\tdisabled
		parts := strings.SplitN(line, "\t", 9)
		item := panelMCPItem{}
		get := func(i int) string {
			if i < len(parts) {
				return parts[i]
			}
			return ""
		}
		item.name = get(0)
		item.scope = get(1)
		item.source = get(2)
		item.status = get(3)
		item.command = get(4)
		item.args = get(5)
		fmt.Sscanf(get(6), "%d", &item.toolCount)
		item.err = get(7)
		item.disabled = get(8) == "1"
		p.mcpItems = append(p.mcpItems, item)
	}
	// Sort so User then Project then Built-in — the visual order matches the
	// flat index used by p.selected.
	scopeRank := func(s string) int {
		switch s {
		case "User":
			return 0
		case "Project":
			return 1
		default:
			return 2
		}
	}
	for i := 1; i < len(p.mcpItems); i++ {
		for j := i; j > 0 && scopeRank(p.mcpItems[j].scope) < scopeRank(p.mcpItems[j-1].scope); j-- {
			p.mcpItems[j], p.mcpItems[j-1] = p.mcpItems[j-1], p.mcpItems[j]
		}
	}
}

func (p *panelState) parsePluginItems() {
	p.pluginItems = nil
	for _, line := range strings.Split(p.pluginRaw, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		item := panelPluginItem{}
		if len(parts) > 0 {
			item.name = parts[0]
		}
		if len(parts) > 1 {
			item.description = parts[1]
		}
		if len(parts) > 2 {
			fmt.Sscanf(parts[2], "%d", &item.cmdCount)
		}
		p.pluginItems = append(p.pluginItems, item)
	}
}

// loadTabData is a no-op for the MCP-only panel (reserved for future tabs).
func (p *panelState) loadTabData() {}

func (p *panelState) currentLen() int {
	return len(p.mcpItems)
}

func (p *panelState) selectedMCPItem() *panelMCPItem {
	if p.serverIdx >= 0 && p.serverIdx < len(p.mcpItems) {
		return &p.mcpItems[p.serverIdx]
	}
	return nil
}

// handlePanelKey routes keyboard input through the panel navigation stack.
func (m Model) handlePanelKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	p := m.panel
	key := msg.String()

	switch p.view {
	case panelViewList:
		switch key {
		case "left", "h":
			p.tab = panelTab((int(p.tab) + len(panelTabNames) - 1) % len(panelTabNames))
			p.selected = 0
			p.loadTabData()
		case "right", "l":
			p.tab = panelTab((int(p.tab) + 1) % len(panelTabNames))
			p.selected = 0
			p.loadTabData()
		case "up", "k":
			if p.selected > 0 {
				p.selected--
			}
		case "down", "j":
			if p.selected < p.currentLen()-1 {
				p.selected++
			}
		case "enter":
			if p.currentLen() > 0 {
				p.serverIdx = p.selected // remember which server/plugin was selected
				p.view = panelViewDetail
				p.selected = 0 // reset to first action in detail
			}
		case "esc", "q", "ctrl+c":
			m.panel = nil
			m.refreshViewport()
			return m, nil
		}

	case panelViewDetail:
		switch key {
		case "up", "k":
			if p.selected > 0 {
				p.selected--
			}
		case "down", "j":
			item2 := p.selectedMCPItem()
			if item2 != nil && p.selected < len(mcpActions(item2))-1 {
				p.selected++
			}
		case "enter":
			if p.tab == panelTabMCP {
				item := p.selectedMCPItem()
				if item == nil {
					break
				}
				actions := mcpActions(item)
				if p.selected >= len(actions) {
					break
				}
				action := actions[p.selected]
				switch action {
				case "View tools":
					// Populate tools from the live manager if not already cached.
					if m.cfg.MCPManager != nil && len(item.tools) == 0 {
						for _, srv := range m.cfg.MCPManager.Servers() {
							if srv.Name == item.name {
								prefix := mcp.NormalizeServerName(srv.Name)
								for _, t := range srv.Tools {
									item.tools = append(item.tools, panelToolItem{
										name:        t.Name,
										fullName:    "mcp__" + prefix[:len(prefix)-2] + "__" + t.Name,
										description: t.Description,
									})
								}
								p.mcpItems[p.serverIdx] = *item
								break
							}
						}
					}
					p.view = panelViewTools
					p.selected = 0
				case "Reconnect":
					if m.cfg.MCPManager != nil {
						cwd, _ := os.Getwd()
						srvName := item.name
						mgr := m.cfg.MCPManager
						go func() { _ = mgr.Reconnect(context.Background(), srvName, cwd) }()
						p.mcpItems[p.serverIdx].status = "pending"
						p.mcpItems[p.serverIdx].err = ""
					}
					p.view = panelViewList
					p.selected = 0
				case "Disable":
					cwd, _ := os.Getwd()
					_ = mcp.SetDisabled(item.name, cwd, true)
					p.mcpItems[p.serverIdx].disabled = true
					p.mcpItems[p.serverIdx].status = "disabled"
					// Close the live connection.
					if m.cfg.MCPManager != nil {
						go func() { m.cfg.MCPManager.DisconnectServer(item.name) }()
					}
					p.view = panelViewList
					p.selected = 0
				case "Enable":
					cwd, _ := os.Getwd()
					_ = mcp.SetDisabled(item.name, cwd, false)
					p.mcpItems[p.serverIdx].disabled = false
					p.mcpItems[p.serverIdx].status = "pending"
					// Reconnect.
					if m.cfg.MCPManager != nil {
						srvName := item.name
						mgr := m.cfg.MCPManager
						go func() { _ = mgr.Reconnect(context.Background(), srvName, cwd) }()
					}
					p.view = panelViewList
					p.selected = 0
				}
			}
		case "esc", "q":
			p.view = panelViewList
			p.selected = 0 // cursor resets to top of list
		case "ctrl+c":
			m.panel = nil
			m.refreshViewport()
			return m, nil
		}

	case panelViewTools:
		switch key {
		case "up", "k":
			if p.selected > 0 {
				p.selected--
			}
		case "down", "j":
			item := p.selectedMCPItem() // uses serverIdx
			if item != nil && p.selected < len(item.tools)-1 {
				p.selected++
			}
		case "enter":
			p.view = panelViewToolDetail
		case "esc", "q":
			p.view = panelViewDetail
			p.selected = 0
		case "ctrl+c":
			m.panel = nil
			m.refreshViewport()
			return m, nil
		}

	case panelViewToolDetail:
		switch key {
		case "esc", "q", "enter":
			p.view = panelViewTools
		case "ctrl+c":
			m.panel = nil
			m.refreshViewport()
			return m, nil
		}
	}

	m.panel = p
	return m, nil
}

// renderPanel renders the unified panel as a full-screen takeover.
// Height = terminal height minus 1 (status bar). Width = full terminal width.
func (m Model) renderPanel() string {
	p := m.panel
	if p == nil {
		return ""
	}

	w := m.width
	if w < 10 {
		w = 10
	}
	// Available height for the panel content = terminal height - 1 (status bar).
	// Border (top+bottom=2) + padding (top+bottom=2) = 4 rows consumed by chrome.
	panelH := m.height - 1
	if panelH < 4 {
		panelH = 4
	}
	// lipgloss v2's Width() is total block width (including border + padding).
	// Width(w-2) - 2 border - 4 padding = w - 8 content area.
	innerW := w - 8

	var sb strings.Builder

	// Panel title — always shown.
	sb.WriteString(styleStatusAccent.Render("MCP") + "\n")
	sb.WriteString(stylePickerDesc.Render(strings.Repeat("─", innerW)) + "\n\n")

	switch p.view {
	case panelViewList:
		m.renderPanelList(&sb, p, innerW)
		sb.WriteString("\n" + stylePickerDesc.Render("↑↓ navigate · Enter detail · Esc close"))
	case panelViewDetail:
		m.renderPanelDetail(&sb, p, innerW)
		sb.WriteString("\n" + stylePickerDesc.Render("↑↓ navigate · Enter select · Esc back"))
	case panelViewTools:
		m.renderPanelTools(&sb, p, innerW)
		sb.WriteString("\n" + stylePickerDesc.Render("↑↓ navigate · Enter view · Esc back"))
	case panelViewToolDetail:
		m.renderPanelToolDetail(&sb, p, innerW)
		sb.WriteString("\n" + stylePickerDesc.Render("Esc back"))
	}

	style := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		PaddingLeft(2).PaddingRight(2).PaddingTop(1).PaddingBottom(1).
		Width(w - 2). // -2 for border
		Height(panelH - 2) // -2 for border
	return style.Render(sb.String())
}

func (m Model) renderPanelList(sb *strings.Builder, p *panelState, innerW int) {
	switch p.tab {
	case panelTabMCP:
		if len(p.mcpItems) == 0 {
			sb.WriteString(stylePickerDesc.Render("No MCP servers configured.\nAdd servers to ~/.claude.json under \"mcpServers\"."))
			return
		}
		sb.WriteString(styleStatusAccent.Render("Manage MCP servers") + "\n")
		sb.WriteString(fmt.Sprintf("%d server%s", len(p.mcpItems), pluralS(len(p.mcpItems))))

		// Items are pre-sorted by scope (User → Project → Built-in).
		// Insert a section header whenever scope changes.
		lastScope := ""
		for i, item := range p.mcpItems {
			if item.scope != lastScope {
				lastScope = item.scope
				src := item.source
				sb.WriteString(fmt.Sprintf("\n  %s (%s)\n",
					fgOnBg(colorFg).Bold(true).Render(item.scope+" MCPs"), src))
			}
			cursor := "  "
			nameStyle := stylePickerItem
			if i == p.selected {
				cursor = stylePickerItemSelected.Render("❯") + " "
				nameStyle = stylePickerItemSelected
			}
			sb.WriteString(fmt.Sprintf("%s%s · %s\n", cursor, nameStyle.Render(item.name), renderMCPStatus(item.status)))
		}
		sb.WriteString("\n" + stylePickerDesc.Render("https://code.claude.com/docs/en/mcp for help"))

	}
}

func (m Model) renderPanelDetail(sb *strings.Builder, p *panelState, innerW int) {
	item := p.selectedMCPItem()
	if item == nil {
		return
	}
	// Title
	sb.WriteString(styleStatusAccent.Render(item.name) + " MCP Server\n\n")
	// Info grid
	writeField := func(label, value string) {
		sb.WriteString(fmt.Sprintf("%-18s%s\n", label+":", value))
	}
	writeField("Status", renderMCPStatus(item.status))
	if item.command != "" {
		writeField("Command", item.command)
	}
	if item.args != "" {
		writeField("Args", item.args)
	}
	if item.source != "" {
		writeField("Config location", item.source)
	}
	writeField("Tools", fmt.Sprintf("%d tool%s", item.toolCount, pluralS(item.toolCount)))
	if item.err != "" {
		// Wrap long error messages to the inner panel width — without
		// this, OAuth errors (which can be hundreds of chars long with
		// a URL chain) get clipped at the right edge.
		wrapW := innerW - 2
		if wrapW < 20 {
			wrapW = 20
		}
		errStyle := fgOnBg(colorError).Width(wrapW)
		sb.WriteString("\n" + errStyle.Render("Error: "+item.err) + "\n")
	}
	sb.WriteByte('\n')
	// Context-sensitive actions matching Claude Code's MCPStdioServerMenu:
	//   1. View tools   — only if connected and has tools
	//   2. Reconnect    — only if not disabled
	//   3. Disable/Enable — always shown, label toggles
	actions := mcpActions(item)
	for i, action := range actions {
		cursor := "  "
		style := stylePickerItem
		if i == p.selected {
			cursor = stylePickerItemSelected.Render("❯") + " "
			style = stylePickerItemSelected
		}
		sb.WriteString(fmt.Sprintf("%s%d. %s\n", cursor, i+1, style.Render(action)))
	}
}

// mcpActions returns the context-sensitive action list for a server detail view.
// Matches MCPStdioServerMenu.tsx in the real Claude Code.
func mcpActions(item *panelMCPItem) []string {
	var actions []string
	if !item.disabled && item.status == "connected" && item.toolCount > 0 {
		actions = append(actions, "View tools")
	}
	if !item.disabled {
		actions = append(actions, "Reconnect")
	}
	if item.disabled {
		actions = append(actions, "Enable")
	} else {
		actions = append(actions, "Disable")
	}
	return actions
}

func (m Model) renderPanelTools(sb *strings.Builder, p *panelState, _ int) {
	item := p.selectedMCPItem()
	if item == nil {
		return
	}
	sb.WriteString(fmt.Sprintf("Tools for %s\n", styleStatusAccent.Render(item.name)))
	sb.WriteString(fmt.Sprintf("%d tool%s\n\n", len(item.tools), pluralS(len(item.tools))))

	if len(item.tools) == 0 {
		sb.WriteString(stylePickerDesc.Render("No tools loaded (server may not be connected)."))
		return
	}
	for i, t := range item.tools {
		cursor := "  "
		nameStyle := stylePickerItem
		if i == p.selected {
			cursor = stylePickerItemSelected.Render("❯") + " "
			nameStyle = stylePickerItemSelected
		}
		// Pad the raw name first — %-30s on a styled string counts ANSI
		// escape bytes toward the width, so the visible padding becomes 0
		// and the description glues onto the tool name.
		const nameWidth = 30
		paddedName := t.name
		if pad := nameWidth - len([]rune(t.name)); pad > 0 {
			paddedName += strings.Repeat(" ", pad)
		}
		attrs := stylePickerDesc.Render("read-only, open-world")
		sb.WriteString(fmt.Sprintf("%s%d. %s%s\n", cursor, i+1, nameStyle.Render(paddedName), attrs))
	}
}

func (m Model) renderPanelToolDetail(sb *strings.Builder, p *panelState, innerW int) {
	item := p.selectedMCPItem()
	if item == nil {
		return
	}
	if p.selected >= len(item.tools) {
		return
	}
	tool := item.tools[p.selected]

	// Title bar
	sb.WriteString(styleStatusAccent.Render(tool.name) + " [read-only] [open-world]\n")
	sb.WriteString(stylePickerDesc.Render(item.name) + "\n\n")
	sb.WriteString(fmt.Sprintf("Tool name: %s\n", tool.name))
	if tool.fullName != "" {
		sb.WriteString(fmt.Sprintf("Full name: %s\n", tool.fullName))
	}
	if tool.description != "" {
		sb.WriteString("\nDescription:\n")
		// Word-wrap description to innerW.
		words := strings.Fields(tool.description)
		line := ""
		for _, w := range words {
			if len(line)+len(w)+1 > innerW-2 {
				sb.WriteString("  " + line + "\n")
				line = w
			} else {
				if line != "" {
					line += " "
				}
				line += w
			}
		}
		if line != "" {
			sb.WriteString("  " + line + "\n")
		}
	}
}

func renderMCPStatus(status string) string {
	switch status {
	case "connected":
		return fgOnBg(lipgloss.Color("2")).Render("✔ connected")
	case "failed":
		return fgOnBg(lipgloss.Color("1")).Render("✗ failed")
	default:
		return stylePickerDesc.Render("… " + status)
	}
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
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

func (m Model) handleLoginKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
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
	case "enter", "space":
		opt := loginOptions[p.selected]
		m.loginPrompt = nil
		// Remove the "Not logged in" welcome message if present so the entire
		// login flow (including that message) gets swept away on completion.
		m.loginFlowMsgStart = m.findNoAuthMsgIdx()
		if m.loginFlowMsgStart < 0 {
			m.loginFlowMsgStart = len(m.messages)
		}
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Opening browser to sign in…"})
		m.refreshViewport()
		useClaudeAI := opt.claudeAI
		prog := *m.cfg.Program
		return m, func() tea.Msg {
			prog.Send(loginStartMsg{claudeAI: useClaudeAI})
			return nil
		}
	case "esc", "ctrl+c":
		m.loginPrompt = nil
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Login cancelled."})
		m.refreshViewport()
		return m, nil
	case "1":
		p.selected = 0
		m.loginPrompt = p
		return m.handleLoginKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	case "2":
		p.selected = 1
		m.loginPrompt = p
		return m.handleLoginKey(tea.KeyPressMsg{Code: tea.KeyEnter})
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

// welcomeCard returns the two-panel welcome banner shown on startup.
// Content is tab-separated: version, modelName, cwd, displayName, email, orgName, subscriptionType.
func (m Model) welcomeCard() Message {
	cwd, _ := os.Getwd()
	p := m.cfg.Profile
	fields := []string{
		m.cfg.Version,
		m.modelName,
		cwd,
		p.DisplayName,
		p.Email,
		p.OrganizationName,
		p.SubscriptionType,
	}
	return Message{
		Role:        RoleSystem,
		WelcomeCard: true,
		Content:     strings.Join(fields, "\t"),
	}
}

// dismissWelcome removes the welcome card from the message list the first time
// the user sends a message or a slash command. Idempotent after first call.
func (m *Model) dismissWelcome() {
	if m.welcomeDismissed {
		return
	}
	m.welcomeDismissed = true
	for i, msg := range m.messages {
		if msg.WelcomeCard {
			m.messages = append(m.messages[:i], m.messages[i+1:]...)
			return
		}
	}
}

// historyToDisplayMessage converts an api.Message back into a display Message.
func historyToDisplayMessage(msg api.Message) Message {
	var text strings.Builder
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				text.WriteString(block.Text)
			}
		case "tool_use":
			return Message{Role: RoleTool, ToolName: block.Name, Content: ""}
		case "tool_result":
			return Message{Role: RoleTool, ToolName: "result", Content: block.ResultContent}
		}
	}
	if msg.Role == "user" {
		return Message{Role: RoleUser, Content: text.String()}
	}
	return Message{Role: RoleAssistant, Content: text.String()}
}

// exportConversation writes the conversation display messages to a markdown file.
func (m *Model) exportConversation(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, msg := range m.messages {
		switch msg.Role {
		case RoleUser:
			fmt.Fprintf(f, "**You:** %s\n\n", msg.Content)
		case RoleAssistant:
			fmt.Fprintf(f, "**Claude:** %s\n\n", msg.Content)
		case RoleSystem:
			fmt.Fprintf(f, "> %s\n\n", strings.ReplaceAll(msg.Content, "\n", "\n> "))
		case RoleError:
			fmt.Fprintf(f, "> ⚠ %s\n\n", msg.Content)
		}
	}
	return nil
}

// persistNewMessages appends any messages not yet written to the session file.
func (m *Model) persistNewMessages(history []api.Message) {
	if m.cfg.Session == nil {
		return
	}
	for i := m.persistedCount; i < len(history); i++ {
		_ = m.cfg.Session.AppendMessage(history[i]) // best-effort
	}
	m.persistedCount = len(history)
}

// findNoAuthMsgIdx returns the index of the "Not logged in" welcome message,
// or -1 if it isn't present (e.g. user was already authenticated at startup).
func (m Model) findNoAuthMsgIdx() int {
	for i, msg := range m.messages {
		if msg.Role == RoleSystem && strings.HasPrefix(msg.Content, "Not logged in") {
			return i
		}
	}
	return -1
}

// tuiLoginDisplay implements auth.LoginDisplay by sending inline TUI messages
// instead of printing to stderr.
type tuiLoginDisplay struct {
	prog *tea.Program
}

func (d *tuiLoginDisplay) Show(automatic, manual string) {
	d.prog.Send(loginURLMsg{automatic: automatic, manual: manual})
}

func (d *tuiLoginDisplay) BrowserOpenFailed(err error) {
	d.prog.Send(loginBrowserFailMsg{err: err})
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
func (m Model) handlePermissionKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
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
	case "enter", "space":
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
	case "ctrl+c", "esc":
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
		m.syncLive()
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
	case "prompt":
		// Inject text as a user turn and kick off an agent run — same as
		// typing the prompt in the input box, but sourced from a slash command.
		if res.Text == "" || m.running || m.noAuth {
			return m, nil
		}
		m.dismissWelcome()
		m.messages = append(m.messages, Message{Role: RoleUser, Content: res.Text})
		m.history = append(m.history, api.Message{
			Role:    "user",
			Content: []api.ContentBlock{{Type: "text", Text: res.Text}},
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
	case "mcp-dialog":
		p := newPanel(panelTabMCP)
		p.mcpRaw = res.Text
		p.parseMCPItems()
		m.panel = p
		m.refreshViewport()
		return m, nil

	case "plugin-panel":
		p, err := newPluginPanel(res.Text)
		if err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: err.Error()})
			m.refreshViewport()
			return m, nil
		}
		// Build discover items synchronously (reads marketplace.json files).
		installedIDs := map[string]bool{}
		for _, item := range p.installedItems {
			installedIDs[item.pluginID] = true
		}
		p.buildDiscoverItems(installedIDs)
		// Inject MCP sub-entries.
		p.injectMCPSubEntries(m.cfg.MCPManager)
		m.pluginPanel = p
		m.refreshViewport()
		// Kick off async install count loading.
		return m, func() tea.Msg {
			counts, err := plugins.LoadInstallCounts()
			return pluginCountsMsg{counts: counts, err: err}
		}

	case "settings-panel":
		// res.Text is the default tab name: "status", "config", "stats", "usage"
		defaultTab := settingsTabStatus
		switch res.Text {
		case "config":
			defaultTab = settingsTabConfig
		case "stats":
			defaultTab = settingsTabStats
		case "usage":
			defaultTab = settingsTabUsage
		}
		cwd, _ := os.Getwd()
		sessPath := ""
		if m.cfg.Session != nil {
			sessPath = m.cfg.Session.FilePath
		}
		var getMCPInfo func() []mcpInfoRow
		if m.cfg.MCPManager != nil {
			getMCPInfo = func() []mcpInfoRow {
				servers := m.cfg.MCPManager.Servers()
				var rows []mcpInfoRow
				for _, srv := range servers {
					rows = append(rows, mcpInfoRow{
						name:   srv.Name,
						status: string(srv.Status),
						tools:  len(srv.Tools),
					})
				}
				return rows
			}
		}
		live := m.cfg.Live
		getStatus := func() statusSnapshot {
			snap := statusSnapshot{}
			if live != nil {
				snap.sessionID = live.SessionID()
				snap.model = live.ModelName()
				snap.fastMode = live.FastMode()
				snap.effort = live.EffortLevel()
				snap.rateLimitWarn = live.RateLimitWarning()
				in, cost := live.Tokens()
				snap.inputTokens = in
				snap.costUSD = cost
				switch live.PermissionMode() {
				case permissions.ModeAcceptEdits:
					snap.permMode = "acceptEdits"
				case permissions.ModePlan:
					snap.permMode = "plan"
				case permissions.ModeBypassPermissions:
					snap.permMode = "bypassPermissions"
				default:
					snap.permMode = "default"
				}
			}
			snap.version = m.cfg.Version
			snap.authenticated = !m.noAuth
			return snap
		}
		rlInfo := ratelimit.Info{}
		if live != nil && live.RateLimitWarning() != "" {
			// Rate limit data isn't directly exposed yet — Info will be empty.
			// TODO: expose from LiveState once full header parsing lands.
		}
		saveConfigFn := func(id string, value interface{}) {
			// Map config item IDs to settings keys where they differ.
			key := id
			switch id {
			case "defaultPermissionMode":
				if s, ok := value.(string); ok {
					_ = settings.SaveRawKey("permissions", map[string]interface{}{"defaultMode": permModeStoredVal(s)})
					return
				}
			case "notifChannel":
				key = "preferredNotifChannel"
			case "alwaysThinkingEnabled":
				key = "alwaysThinkingEnabled"
			case "outputStyle":
				if s, ok := value.(string); ok {
					_ = settings.SaveOutputStyle(outputStyleStoredVal(s))
					return
				}
			case "theme":
				// Apply the theme live so the panel re-renders with new colors.
				if s, ok := value.(string); ok {
					theme.Set(s)
					_ = settings.SaveRawKey("theme", s)
					return
				}
			}
			_ = settings.SaveRawKey(key, value)
		}
		panel, statsCmd := newSettingsPanel(
			defaultTab, getStatus, getMCPInfo,
			saveConfigFn,
			m.cfg.Gate, m.cfg.MCPManager, sessPath, rlInfo, cwd,
		)
		m.settingsPanel = panel
		m.refreshViewport()
		return m, statsCmd

	case "picker":
		// Open generic picker overlay. JSON payload in res.Text:
		//   {"title":"...","current":"dark","items":[{"value":"dark","label":"Dark"}]}
		// Kind ("theme"|"model"|"output-style") comes from res.Model.
		var payload struct {
			Title   string       `json:"title"`
			Current string       `json:"current"`
			Items   []pickerItem `json:"items"`
		}
		if err := json.Unmarshal([]byte(res.Text), &payload); err != nil || len(payload.Items) == 0 {
			m.messages = append(m.messages, Message{Role: RoleError, Content: "picker: invalid or empty payload"})
			m.refreshViewport()
			return m, nil
		}
		// Position cursor on the current value if present.
		sel := 0
		for i, it := range payload.Items {
			if it.Value == payload.Current {
				sel = i
				break
			}
		}
		m.picker = &pickerState{
			kind:     res.Model,
			title:    payload.Title,
			items:    payload.Items,
			selected: sel,
			current:  payload.Current,
		}
		m.refreshViewport()
		return m, nil

	case "resume-pick":
		// Parse tab-separated session lines from the command result.
		var sessions []resumeSession
		for _, line := range strings.Split(res.Text, "\n") {
			parts := strings.SplitN(line, "\t", 3)
			if len(parts) < 3 {
				continue
			}
			sessions = append(sessions, resumeSession{
				filePath: parts[0],
				age:      parts[1],
				preview:  parts[2],
			})
		}
		if len(sessions) == 0 {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "No previous sessions found."})
			m.refreshViewport()
			return m, nil
		}
		p := &resumePromptState{sessions: sessions, selected: 0}
		// Initialize filtered list with all sessions (no filter yet).
		p.filtered = make([]int, len(sessions))
		for i := range sessions {
			p.filtered[i] = i
		}
		m.resumePrompt = p
		m.refreshViewport()
		return m, nil

	case "output-style":
		// res.Model = style name, res.Text = style prompt (empty to clear).
		m.outputStyleName = res.Model
		m.outputStylePrompt = res.Text
		// Rebuild system blocks. The Max-subscription wire fingerprint
		// requires system[0..3] to be billing/identity/agent/output-guidance
		// in exact order — prepending anything else returns a literal
		// 429 rate_limit_error with no quota actually hit. So we append
		// the style block AFTER the base fingerprint blocks.
		if m.cfg.Loop != nil {
			cwd, _ := os.Getwd()
			mem := memdir.BuildPrompt(cwd)
			baseBlocks := agent.BuildSystemBlocks(mem, "")
			if res.Text != "" {
				styleBlock := api.SystemBlock{Type: "text", Text: "# Output style: " + res.Model + "\n\n" + res.Text}
				newBlocks := append(baseBlocks, styleBlock)
				m.cfg.Loop.SetSystem(newBlocks)
			} else {
				m.cfg.Loop.SetSystem(baseBlocks)
			}
		}
		// Persist to settings so the style survives restarts.
		_ = settings.SaveOutputStyle(res.Model)
		msg := "Output style cleared."
		if res.Model != "" {
			msg = fmt.Sprintf("Output style set to: %s", res.Model)
		}
		m.messages = append(m.messages, Message{Role: RoleSystem, Content: msg})
		m.refreshViewport()
		return m, nil

	case "rewind":
		// res.Text is the number of turns removed (as string from the command).
		// Trim from m.history: each "turn" is one user+assistant message pair.
		n := 1
		fmt.Sscanf(res.Text, "%d", &n)
		removed := 0
		for i := 0; i < n && len(m.history) >= 2; i++ {
			// Remove the last user+assistant pair from the API history.
			m.history = m.history[:len(m.history)-2]
			removed++
		}
		// Also trim display messages — keep system messages, remove last n user+assistant pairs.
		for i := 0; i < removed; i++ {
			// Walk backwards to find and remove the last assistant then user display message.
			for j := len(m.messages) - 1; j >= 0; j-- {
				if m.messages[j].Role == RoleAssistant {
					m.messages = append(m.messages[:j], m.messages[j+1:]...)
					break
				}
			}
			for j := len(m.messages) - 1; j >= 0; j-- {
				if m.messages[j].Role == RoleUser {
					m.messages = append(m.messages[:j], m.messages[j+1:]...)
					break
				}
			}
		}
		if removed > 0 {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: fmt.Sprintf("Rewound %d turn(s).", removed)})
		} else {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Nothing to rewind."})
		}
		m.refreshViewport()
		return m, nil

	case "export":
		path := res.Text
		if err := m.exportConversation(path); err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Export failed: %v", err)})
		} else {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: "Conversation exported to: " + path})
		}
		m.refreshViewport()
		return m, nil
	case "flash":
		// Briefly surface in the spinner row, then queue the next pending
		// MCP approval if any are still waiting after this one resolved.
		if res.Text != "" {
			m.flashMsg = res.Text
		}
		m.refreshViewport()
		var cmd tea.Cmd
		if m.cfg.MCPManager != nil {
			if pending := m.cfg.MCPManager.PendingApprovals(); len(pending) > 0 {
				cmd = func() tea.Msg { return mcpApprovalMsg{pending: pending} }
			}
		}
		return m, tea.Batch(cmd, tea.Tick(2*time.Second, func(time.Time) tea.Msg { return clearFlash{} }))
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

// TasksSummary returns the list of active tasks for /tasks.
// Tasks are tracked by the TaskTool — for now we surface the tool messages.
func (m *Model) TasksSummary() string {
	var tasks []string
	for _, msg := range m.messages {
		if msg.Role == RoleTool && strings.HasPrefix(msg.ToolName, "Task") {
			tasks = append(tasks, fmt.Sprintf("  [%s] %s", msg.ToolName, msg.Content))
		}
	}
	if len(tasks) == 0 {
		return "No active tasks."
	}
	return "Active tasks:\n" + strings.Join(tasks, "\n")
}

// LastThinking returns the last thinking block text from the assistant.
func (m *Model) LastThinking() string {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Role == RoleAssistant && strings.Contains(m.messages[i].Content, "<thinking>") {
			return m.messages[i].Content
		}
	}
	return ""
}

// CopyLastResponse copies the last assistant text to clipboard.
// Returns "" on success, error message otherwise.
func (m *Model) CopyLastResponse() string {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Role == RoleAssistant {
			copyToClipboard(m.messages[i].Content)
			return ""
		}
	}
	return "No assistant response to copy."
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
		// refreshViewport's sticky-bottom logic preserves the user's
		// scroll position when they've scrolled up to read history mid-
		// stream. GotoBottom only fires when they're already pinned to
		// the bottom.
		m.refreshViewport()

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

	case agent.EventRateLimit:
		m.rateLimitWarning = ev.RateLimitWarning
		m.syncLive()

	case agent.EventPartial:
		// Conversation recovery: persist the partial assistant message to
		// the session JSONL so /resume can pick up from where we left off.
		// FilterUnresolvedToolUses runs at load time to drop orphan
		// tool_use blocks that never got a tool_result.
		if m.cfg.Session != nil && len(ev.PartialBlocks) > 0 {
			_ = m.cfg.Session.AppendMessage(api.Message{
				Role:    "assistant",
				Content: ev.PartialBlocks,
			})
		}
	}
	return m
}

// applyLayout recalculates component dimensions.
func (m Model) applyLayout() Model {
	if m.width == 0 || m.height == 0 {
		return m
	}
	inputRows := m.input.LineCount()
	if inputRows < 1 {
		inputRows = 1
	}
	vpHeight := m.height - chromeHeight(inputRows, m.height)
	if vpHeight < 1 {
		vpHeight = 1
	}
	// Match the textarea's visible row count to the available chrome budget
	// so it doesn't try to render more rows than the layout reserved.
	visibleRows := m.height - vpHeight - chromeFixed
	if visibleRows < inputMinRows {
		visibleRows = inputMinRows
	}
	if visibleRows > inputMaxRows {
		visibleRows = inputMaxRows
	}
	m.input.SetHeight(visibleRows)
	// Input inner width: full width minus left+right border (2) minus left+right padding (2).
	inputW := m.width - 4
	if inputW < 10 {
		inputW = 10
	}

	if !m.ready {
		m.vp = viewport.New(viewport.WithWidth(m.width), viewport.WithHeight(vpHeight))
		m.vp.Style = lipgloss.NewStyle() // app bg behind viewport content
		m.ready = true
	} else {
		m.vp.SetWidth(m.width)
		m.vp.SetHeight(vpHeight)
	}
	m.input.SetWidth(inputW)
	// Drop bubbles textarea's Placeholder feature — its internal
	// placeholderView path emits ANSI sequences (cursor reverse-video,
	// internal viewport, partial line padding) that our outer bg paint
	// can't reliably override. We render our own placeholder hint inline
	// in View() when input is empty.
	m.input.Placeholder = ""
	m.refreshViewport()
	return m
}

// refreshViewport rebuilds the viewport content string.
//
// Sticky-bottom: if the user was already pinned to the bottom (reading
// new content as it streams), we re-pin after rebuilding. If they
// scrolled up to read history, SetContent leaves YOffset alone in
// bubbles v2 — but we explicitly re-call GotoBottom only when AtBottom
// was already true, so in-flight scrollback is preserved while the
// model is streaming new tokens.
func (m *Model) refreshViewport() {
	if !m.ready {
		return
	}
	w := m.vp.Width()
	if w <= 0 {
		return
	}
	wasAtBottom := m.vp.AtBottom()
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
	if wasAtBottom {
		m.vp.GotoBottom()
	}
}

// View renders the full TUI frame. v2 returns tea.View — internally we
// still build a string and wrap it via mkView so all the existing
// rendering/paint logic stays unchanged.
//
// Basic keyboard disambiguation (shift+enter, ctrl+i, etc) is enabled by
// default in bubbletea v2 — no opt-in required for those keys. The
// KeyboardEnhancements field below opts into more advanced features:
// ReportAlternateKeys lets terminals report alternate key values (helps
// international keyboards), and we leave ReportEventTypes off because we
// don't need key release events.
func (m Model) View() tea.View {
	mkView := func(content string) tea.View {
		var v tea.View
		v.SetContent(content)
		v.AltScreen = true
		v.KeyboardEnhancements.ReportAlternateKeys = true
		// MouseMode intentionally left unset (MouseModeNone). Enabling
		// cell-motion captures every click — terminals can no longer
		// drag-select text in the alt-screen. Most modern terminals
		// (iTerm2, Ghostty, WezTerm, Kitty) translate scroll-wheel into
		// cursor up/down sequences in alt-screen mode, which the
		// viewport already binds. Explicit Shift+Up/Down/PgUp/PgDn
		// handlers below cover keyboard scrollback. The combination
		// preserves native text selection.
		return v
	}
	if !m.ready {
		return mkView("Loading…\n")
	}

	// Re-apply theme styles to widgets every render. Necessary because
	// Bubble Tea returns NEW Model values from Update — any closure that
	// captured a pointer at startup (e.g. theme.OnChange listener) refers
	// to a stale Model the framework no longer uses. Cheap to do per-frame
	// (just struct field assignment) and guarantees theme switches apply.
	applyTextareaTheme(&m.input)
	m.spinner.Style = styleSpinner

	// Compute overlay box first so we can shrink the viewport to keep the
	// input row + status bar on screen. Without this, a tall overlay
	// (resume picker with 10 sessions, login picker, permission prompt)
	// pushes inputBox past the bottom of the alt-screen and lipgloss
	// Height(m.height) clips it. Order matters: viewport must be rendered
	// AFTER its height has been adjusted for the overlay.
	var overlayBox string
	if m.loginPrompt != nil {
		overlayBox = m.renderLoginPicker()
	} else if m.resumePrompt != nil {
		overlayBox = m.renderResumePicker()
	} else if m.picker != nil {
		overlayBox = m.renderPicker()
	} else if m.onboarding != nil {
		overlayBox = m.renderOnboarding()
	} else if m.permPrompt != nil {
		overlayBox = m.renderPermissionPrompt()
	} else if len(m.cmdMatches) > 0 {
		overlayBox = m.renderCommandPicker()
	} else if len(m.atMatches) > 0 {
		overlayBox = m.renderAtPicker()
	} else if m.companionBubble != "" {
		overlayBox = m.renderCompanionBubble()
	}
	if overlayBox != "" {
		overlayLines := strings.Count(overlayBox, "\n") + 1
		newH := m.vp.Height() - overlayLines
		if newH < 1 {
			newH = 1
		}
		m.vp.SetHeight(newH)
	}

	// Viewport.
	vp := m.vp.View()

	// Spinner row — always 1 line to prevent layout shift.
	// Always emit a full-width bg-painted line so the area under the viewport
	// doesn't expose terminal default bg.
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
	// Force-paint the textarea view in light themes. The textarea's
	// placeholderView and internal viewport emit ANSI sequences with
	// internal \033[0m resets that clear bg back to terminal default,
	// leaving a dark stripe across the input row. Solution:
	//   1. Replace internal \033[0m with soft reset (\033[22;23;39m) +
	//      bg reapply, so bg persists across reset boundaries.
	//   2. Pad each line to inner width with bg-painted spaces so cells
	//      right of the placeholder text are filled.
	//   3. Wrap in a fg+bg style so the line starts with bg set.
	innerView := m.input.View()
	if theme.Active().Background != "" {
		innerW := m.width - 4
		if innerW < 1 {
			innerW = 1
		}
		bgEsc := theme.AnsiBG(theme.Active().Background)
		fgEsc := theme.AnsiFG(theme.Active().Primary)
		const fullReset = "\x1b[0m"
		const softReset = "\x1b[22;23;39m"
		var fixed []string
		for _, line := range strings.Split(innerView, "\n") {
			line = strings.ReplaceAll(line, fullReset, softReset+bgEsc+fgEsc)
			w := lipgloss.Width(line)
			if w < innerW {
				line += strings.Repeat(" ", innerW-w)
			}
			fixed = append(fixed, bgEsc+fgEsc+line+fullReset)
		}
		innerView = strings.Join(fixed, "\n")
	}
	// If clipboard images are queued, prepend an attachment badge.
	if n := len(m.pendingImages); n > 0 {
		label := fmt.Sprintf("📎 [%d image(s)]", n)
		badge := styleStatusAccent.Render(label) + "  " + stylePickerDesc.Render("ctrl+v for more · Enter to send · Esc to clear")
		innerView = badge + "\n" + innerView
	}
	inputBox := bStyle.Width(m.width - 2).Render(innerView)

	// Status bar — fixed left-anchor layout so nothing shifts when mode changes.
	//
	// left:  edgePad  conduit  [mode badge]  |  model  [| ctx]  [| cost]
	// right: hints  edgePad
	// pad:   all remaining space between left and right
	edgePad := strings.Repeat(" ", outerPad)
	barSep := styleStatus.Render(" | ")

	appSeg := styleStatusAccent.Render("conduit")

	var modeBadge string
	switch m.permissionMode {
	case permissions.ModeAcceptEdits:
		modeBadge = styleModePurple.Render("⏵⏵ accept edits")
	case permissions.ModePlan:
		modeBadge = styleModeCyan.Render("⏸ plan mode")
	case permissions.ModeBypassPermissions:
		modeBadge = styleModeYellow.Render("⏵⏵ auto")
	}

	modelSeg := styleStatusModel.Render(shortModelName(m.modelName))

	var leftParts []string
	leftParts = append(leftParts, edgePad+appSeg)
	if modeBadge != "" {
		leftParts = append(leftParts, modeBadge)
	}
	leftParts = append(leftParts, modelSeg)
	if m.fastMode {
		leftParts = append(leftParts, styleStatus.Render("⚡ fast"))
	}
	if m.totalInputTokens > 0 {
		pct := m.totalInputTokens * 100 / 200000
		if pct > 100 {
			pct = 100
		}
		leftParts = append(leftParts, styleStatus.Render(fmt.Sprintf("%d%% ctx", pct)))
	}
	if m.costUSD > 0 {
		leftParts = append(leftParts, styleStatus.Render(fmt.Sprintf("$%.2f", m.costUSD)))
	}
	if m.rateLimitWarning != "" {
		leftParts = append(leftParts, styleModeYellow.Render("⚠ "+m.rateLimitWarning))
	}
	left := strings.Join(leftParts, barSep)

	// Show session title (from /rename or first message) in the right side.
	var rightParts []string
	if m.cfg.Session != nil {
		title := session.ExtractTitle(m.cfg.Session.FilePath)
		if title != "" {
			const maxTitle = 30
			runes := []rune(title)
			if len(runes) > maxTitle {
				title = string(runes[:maxTitle-1]) + "…"
			}
			rightParts = append(rightParts, styleStatus.Render(title))
		}
	}
	rightParts = append(rightParts, styleStatus.Render("^Y copy  ^C stop  shift+tab mode"))
	right := strings.Join(rightParts, barSep) + edgePad

	pad := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if pad < 1 {
		pad = 1
	}
	statusBar := left + strings.Repeat(" ", pad) + right

	// Panel is a full-screen takeover — replace vp+spinner+input with it.
	// Only status bar remains at the bottom.
	if m.panel != nil {
		panel := m.renderPanel()
		return mkView(paintApp(m.width, m.height, lipgloss.JoinVertical(lipgloss.Left, panel, statusBar)))
	}

	// Plugin panel is also a full-screen takeover.
	if m.pluginPanel != nil {
		pluginPanel := m.renderPluginPanel()
		return mkView(paintApp(m.width, m.height, lipgloss.JoinVertical(lipgloss.Left, pluginPanel, statusBar)))
	}

	// Settings panel is a full-screen takeover.
	if m.settingsPanel != nil {
		sp := m.renderSettingsPanel()
		return mkView(paintApp(m.width, m.height, lipgloss.JoinVertical(lipgloss.Left, sp, statusBar)))
	}

	// (Overlay was computed earlier so we could shrink the viewport.)

	// JoinVertical with explicit newlines between non-empty parts.
	// spinRow is always full-width bg-painted (set above) so it covers the
	// gap between viewport and input regardless of whether it has content.
	parts := []string{vp}
	parts = append(parts, spinRow)
	if overlayBox != "" {
		parts = append(parts, overlayBox)
	}
	parts = append(parts, inputBox)
	if coord := renderCoordinatorPanel(m.width); coord != "" {
		parts = append(parts, coord)
	}
	parts = append(parts, statusBar)
	return mkView(paintApp(m.width, m.height, lipgloss.JoinVertical(lipgloss.Left, parts...)))
}

// renderCoordinatorPanel renders a footer row per active sub-agent task
// (in_progress) so the user can see what background work is running.
// Mirrors src/components/CoordinatorAgentStatus.tsx CoordinatorTaskPanel
// trimmed to a static one-line-per-task layout. Empty when no tasks
// are in_progress.
func renderCoordinatorPanel(width int) string {
	if width < 20 {
		return ""
	}
	var active []*tasktool.Task
	for _, t := range tasktool.GlobalStore().List() {
		if t.Status == tasktool.StatusInProgress {
			active = append(active, t)
		}
	}
	if len(active) == 0 {
		return ""
	}
	pad := strings.Repeat(" ", outerPad)
	var sb strings.Builder
	for i, t := range active {
		label := t.ActiveForm
		if label == "" {
			label = t.Subject
		}
		elapsed := time.Since(t.CreatedAt).Round(time.Second)
		// Truncate label so [elapsed] fits without wrapping.
		const tailMax = 12 // " · 999s"-ish
		max := width - outerPad*2 - 4 - tailMax
		if max < 10 {
			max = 10
		}
		runes := []rune(label)
		if len(runes) > max {
			label = string(runes[:max-1]) + "…"
		}
		line := pad + styleStatusAccent.Render("⚙ ") + styleStatus.Render(label) + " " + stylePickerDesc.Render("· "+elapsed.String())
		sb.WriteString(line)
		if i < len(active)-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// paintApp paints the theme background across the visible TUI region —
// but ONLY when the active palette has a Background value set. Dark themes
// leave Background empty so the terminal bg shows through cleanly.
//
// Light themes set Background so the user sees a light surface even on a
// dark terminal. The paint isn't pixel-perfect (TUI cell rendering means
// some widget chrome shows terminal bg), but it makes light themes usable.
//
// Two-phase paint to keep bg painted across lipgloss internal resets:
//  1. Replace internal "\x1b[0m" with soft reset + bg reapply
//  2. Pad each line to width and wrap in styleAppSurface
func paintApp(w, h int, content string) string {
	if w <= 0 || h <= 0 {
		return content
	}
	pBg := theme.Active().Background
	if pBg == "" {
		// Dark themes — passthrough, terminal bg shows through.
		return content
	}
	bg := theme.AnsiBG(pBg)
	const fullReset = "\x1b[0m"
	const softReset = "\x1b[22;23;39m"
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		line = strings.ReplaceAll(line, fullReset, softReset+bg)
		visW := lipgloss.Width(line)
		if visW < w {
			line += strings.Repeat(" ", w-visW)
		}
		lines[i] = bg + line + fullReset
	}
	out := strings.Join(lines, "\n")
	return styleAppSurface.Width(w).Height(h).Render(out)
}

// tallyTokens estimates token usage from conversation history using
// cl100k_base — the tokenizer Claude approximates for billing. Falls
// back to chars/4 if the encoder fails to initialize (offline first run).
func (m *Model) tallyTokens() {
	total := 0
	for _, msg := range m.history {
		for _, b := range msg.Content {
			total += tokens.Estimate(b.Text)
		}
	}
	m.totalInputTokens = total
	// Opus 4.7: ~$15/$75 per M in/out, blended ~$45/M estimate.
	m.costUSD = float64(total) * 45.0 / 1_000_000
	m.syncLive()
}

// syncLive pushes frequently-read fields into the thread-safe LiveState bag
// so command callbacks running outside the Bubble Tea event loop always see
// current values, not the stale initial snapshot from New().
func (m *Model) syncLive() {
	if m.cfg.Live == nil {
		return
	}
	m.cfg.Live.SetModelName(m.modelName)
	m.cfg.Live.SetPermissionMode(m.permissionMode)
	m.cfg.Live.SetTokens(m.totalInputTokens, m.costUSD)
	m.cfg.Live.SetRateLimitWarning(m.rateLimitWarning)
	if m.cfg.Session != nil {
		m.cfg.Live.SetSessionID(m.cfg.Session.ID)
	}
}

// shortModelName converts "claude-opus-4-7" → "Opus 4.7".
// Strips date suffixes like "-20251001" so "claude-haiku-4-5-20251001" → "Haiku 4.5".
func shortModelName(name string) string {
	name = strings.TrimPrefix(name, "claude-")
	idx := strings.Index(name, "-")
	if idx < 0 {
		return capitalize(name)
	}
	family := capitalize(name[:idx])
	rest := name[idx+1:]
	// Strip YYYYMMDD date suffix segments (8-digit numbers).
	parts := strings.Split(rest, "-")
	var verParts []string
	for _, p := range parts {
		if len(p) == 8 {
			allDigits := true
			for _, c := range p {
				if c < '0' || c > '9' {
					allDigits = false
					break
				}
			}
			if allDigits {
				break // drop this and everything after
			}
		}
		verParts = append(verParts, p)
	}
	ver := strings.Join(verParts, ".")
	return family + " " + ver
}

func capitalize(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// applyTextareaTheme rebuilds the textarea's stored Focused/Blurred styles
// from the current theme palette. Bubbles textarea caches styles by VALUE,
// so reassigning the package-level color vars in RebuildStyles doesn't
// reach the textarea — we have to re-set them explicitly.
//
// Called from Model.New() and from the theme.OnChange listener registered
// in registerThemeAwareWidgets.
func applyTextareaTheme(ta *textarea.Model) {
	hasBg := theme.Active().Background != ""
	maybeBg := func(s lipgloss.Style) lipgloss.Style {
		if hasBg {
			return s.Background(colorBg)
		}
		return s
	}

	// Base must have BOTH fg and bg — every other style inherits from Base.
	// Without explicit fg, text rendered on the cursor row uses terminal
	// default fg (light gray on most terminals = unreadable on light theme).
	taBase := maybeBg(lipgloss.NewStyle().Foreground(colorFg))
	taPlaceholder := maybeBg(lipgloss.NewStyle().Foreground(colorMuted))

	// v2: textarea Styles is a value-typed accessor — read, mutate, write back.
	styles := ta.Styles()
	for _, s := range []*textarea.StyleState{&styles.Focused, &styles.Blurred} {
		s.Base = taBase
		s.Text = taBase
		s.Placeholder = taPlaceholder
		s.Prompt = taBase
		s.CursorLine = taBase
		s.CursorLineNumber = taBase
		s.EndOfBuffer = taBase
		s.LineNumber = taBase
	}
	// v2: cursor color/blink live on Styles.Cursor (CursorStyle struct).
	// Static (non-blink) was preserved earlier in New() via Blink=false.
	if hasBg {
		styles.Cursor.Color = colorFg
	} else {
		styles.Cursor.Color = colorFg
	}
	ta.SetStyles(styles)
}

// AllBindings returns the flat binding list from the active resolver, suitable
// for /keybindings display. Falls back to Defaults() when the resolver is nil.
func (m Model) AllBindings() []keybindings.Binding {
	if m.kb == nil {
		return keybindings.Defaults()
	}
	return m.kb.Bindings()
}

// activeContexts returns the keybinding context stack for the current UI
// state. "Global" is always present; one specific context is prepended
// based on which overlay or input mode is active.
func (m Model) activeContexts() []string {
	switch {
	case m.permPrompt != nil:
		return []string{"Confirmation", "Global"}
	case m.picker != nil, m.resumePrompt != nil, m.loginPrompt != nil:
		return []string{"Select", "Global"}
	case m.settingsPanel != nil:
		return []string{"Settings", "Global"}
	case m.pluginPanel != nil:
		return []string{"Plugin", "Global"}
	case m.panel != nil:
		return []string{"Global"}
	default:
		return []string{"Chat", "Global"}
	}
}

// dispatchKeybindingAction maps a resolved action ID to an existing handler.
// Returns (model, cmd, true) when the action was handled, (m, nil, false)
// when the action ID is not (yet) wired here and should fall through.
//
// "command:*" actions dispatch a slash command as if the user had typed it.
// Other IDs mirror the built-in switch in handleKey.
func (m Model) dispatchKeybindingAction(action string, msg tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	// "command:help" → run /help, "command:compact" → run /compact, etc.
	if strings.HasPrefix(action, "command:") {
		cmdName := strings.TrimPrefix(action, "command:")
		if m.cfg.Commands == nil {
			return m, nil, false
		}
		if res, ok := m.cfg.Commands.Dispatch("/" + cmdName); ok {
			m2, cmd := m.applyCommandResult(res)
			return m2, cmd, true
		}
		return m, nil, false
	}

	switch action {
	// App-level
	case "app:interrupt":
		// Same as ctrl+c: cancel turn if running, quit if idle.
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		return m2, cmd, true
	case "app:exit":
		return m, tea.Quit, true
	case "app:redraw":
		return m, tea.ClearScreen, true

	// Chat input
	// All re-dispatch cases use handleKeyBuiltins — NOT handleKey — to
	// break the recursion: handleKey runs the KB resolver which calls
	// dispatchKeybindingAction again for the same action.
	case "chat:cancel":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyEsc})
		return m2, cmd, true
	case "chat:submit":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyEnter})
		return m2, cmd, true
	case "chat:cycleMode":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
		return m2, cmd, true

	case "select:next":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyDown})
		return m2, cmd, true
	case "select:previous":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyUp})
		return m2, cmd, true
	case "select:accept":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyEnter})
		return m2, cmd, true
	case "select:cancel":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyEsc})
		return m2, cmd, true

	case "confirm:yes":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: 'y'})
		return m2, cmd, true
	case "confirm:no":
		m2, cmd, _ := m.handleKeyBuiltins(tea.KeyPressMsg{Code: 'n'})
		return m2, cmd, true
	}

	return m, nil, false
}
