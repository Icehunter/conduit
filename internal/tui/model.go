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
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/buddy"
	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/coordinator"
	"github.com/icehunter/conduit/internal/keybindings"
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
//	spinner row:   1
//	input border:  1 (top) + 1 (bottom) = 2
//	input text:    inputRows (1..inputMaxRows)
//	status bar:    1
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
func isLetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

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
		msgs     []api.Message
		filePath string // source file — used to repoint cfg.Session so new turns append there
		err      error
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

	// questionAskMsg is sent when AskUserQuestion needs a real answer from
	// the user. Unlike permissionAskMsg it does NOT use the permission modal —
	// it shows the question in chat and lets the user type a free-form answer
	// or pick a numbered option via the normal input box.
	questionAskMsg struct {
		question string
		options  []questionOption
		multi    bool
		reply    chan<- []string
	}
	questionOption struct {
		Label       string
		Value       string
		Description string
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
	// NeedsTrust is true when the current working directory has not been
	// marked trusted in ~/.claude.json. The TUI shows the trust dialog
	// before allowing any agent interaction.
	NeedsTrust bool
	// SetTrusted persists acceptance of the workspace trust dialog.
	SetTrusted func() error
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

	running         bool
	cancelled       bool // true after Ctrl+C; cleared when next turn starts
	cancelTurn      context.CancelFunc
	streaming       string
	turnID          int               // incremented each turn; agentDoneMsg with stale ID is ignored
	pendingMessages []string          // messages typed while agent is running; drained after turn ends
	questionAsk     *questionAskState // non-nil when AskUserQuestion is waiting for user input

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
	prevCostUSD       float64 // cost before the current turn started; used to compute per-turn delta

	// turnCosts records the cost delta for each completed assistant turn,
	// most-recent last. Used by /cost to show per-turn breakdown.
	turnCosts []float64

	// flashMsg is shown in the spinner row briefly (e.g. "Copied!").
	flashMsg string

	// companionName is the configured companion's name, loaded once at startup.
	// Empty when no companion is configured. Used to strip [Name: ...] markers
	// from streaming content before they reach the viewport.
	companionName string

	// companionBubble is the text shown in the companion speech bubble overlay.
	// Set when the agent produces a [Name: ...] marker in a response.
	// Auto-cleared after ~10 seconds via a clearBubble tick.
	companionBubble string

	// buddyFrame is the current animation frame for the companion sprite.
	// Cycled by buddyTickMsg at ~500ms intervals whenever the companion is present.
	buddyFrame int

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

	// trustDialog is non-nil when the workspace trust dialog is pending.
	trustDialog *trustDialogState

	// Login picker state — non-nil when /login is active.
	loginPrompt *loginPromptState

	ready  bool // true once we've received the first WindowSizeMsg
	noAuth bool // true when TUI started without credentials

	// Resume picker state — non-nil when /resume is showing session list.
	resumePrompt *resumePromptState

	// doctorPanel holds the /doctor full-screen diagnostics overlay.
	// Non-nil when the doctor panel is open; nil otherwise.
	doctorPanel *doctorPanelState

	// searchPanel holds the /search results overlay. Non-nil when active.
	searchPanel *searchPanelState

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
	// pendingPDFs holds clipboard PDFs queued to send with the next message.
	pendingPDFs []*attach.PDF

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
	if sc, err := buddy.Load(); err == nil && sc != nil {
		m.companionName = sc.Name
	}

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

	// Sync displayed permission mode from the gate, which was initialized with
	// the value from settings.json. Without this the status bar always shows
	// "default" even when settings.json has "defaultMode": "plan".
	if cfg.Gate != nil {
		m.permissionMode = cfg.Gate.Mode()
	}

	// Workspace trust dialog — shown before any agent interaction when the
	// current directory hasn't been accepted yet. Mirrors CC's trust-dialog
	// gating (decoded/5053.js). Skipped in sandboxed / non-interactive mode
	// (NeedsTrust is false when CLAUDE_CODE_SANDBOXED is set or -p flag used).
	if cfg.NeedsTrust && cfg.SetTrusted != nil {
		m.trustDialog = &trustDialogState{setTrusted: cfg.SetTrusted}
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
	if m.companionName != "" {
		cmds = append(cmds, buddyTick())
	}
	return tea.Batch(cmds...)
}

type buddyTickMsg struct{}

func buddyTick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return buddyTickMsg{} })
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
		if m.questionAsk != nil {
			// Cancel a pending AskUserQuestion — send nil so the tool returns
			// "no answer" rather than blocking forever.
			m.questionAsk.reply <- nil
			m.questionAsk = nil
		}
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
			m.permPrompt != nil || m.picker != nil || m.onboarding != nil ||
			m.doctorPanel != nil || m.searchPanel != nil
		if !hasOverlay {
			content := strings.ReplaceAll(msg.Content, "\r\n", "\n")
			content = strings.ReplaceAll(content, "\r", "\n")

			// File drag-drop detection: terminals paste dragged files as
			// "file:///path/to/file" URIs or shell-escaped absolute paths.
			// Images → pendingImages badge; PDFs → pendingPDFs badge; other files → @mention.
			if paths, ok := attach.DetectDroppedPaths(strings.TrimSpace(content)); ok {
				for _, p := range paths {
					switch attach.DroppedFileType(p) {
					case attach.DropImage:
						if img, err := attach.ReadImageFile(p); err == nil {
							m.pendingImages = append(m.pendingImages, img)
						} else {
							m.input.InsertString(attach.MentionPath(p))
						}
					case attach.DropPDF:
						if pdf, err := attach.ReadPDFFile(p); err == nil {
							m.pendingPDFs = append(m.pendingPDFs, pdf)
						} else {
							m.input.InsertString(attach.MentionPath(p))
						}
					default:
						m.input.InsertString(attach.MentionPath(p))
					}
				}
				n := len(m.pendingImages) + len(m.pendingPDFs)
				if n > 0 {
					parts := []string{}
					if ni := len(m.pendingImages); ni > 0 {
						parts = append(parts, fmt.Sprintf("%d image(s)", ni))
					}
					if np := len(m.pendingPDFs); np > 0 {
						parts = append(parts, fmt.Sprintf("%d PDF(s)", np))
					}
					m.flashMsg = "📎 [" + strings.Join(parts, ", ") + "]  · Enter to send · Esc to clear"
					return m, tea.Tick(5*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} })
				}
				return m, nil
			}

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
			}
			m.input.InsertString(content)
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
		} else if msg.err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: msg.err.Error()})
			if len(m.history) > 0 && m.history[len(m.history)-1].Role == "user" {
				m.history = m.history[:len(m.history)-1]
			}
		} else {
			m.history = msg.history
			m.tallyTokens()
			// Record per-turn cost delta in both model and LiveState (LiveState
			// is read by GetTurnCosts from outside the Bubble Tea event loop).
			if delta := m.costUSD - m.prevCostUSD; delta > 0 {
				m.turnCosts = append(m.turnCosts, delta)
				if m.cfg.Live != nil {
					m.cfg.Live.AppendTurnCost(delta)
				}
			}
			m.prevCostUSD = m.costUSD
			m.persistNewMessages(msg.history)
			if m.cfg.Session != nil && m.totalInputTokens > 0 {
				_ = m.cfg.Session.AppendCost(m.totalInputTokens, m.totalOutputTokens, m.costUSD)
			}
			// Short responses (≤4 lines, ≤200 chars) when user addressed the
			// companion go to the bubble only. Longer responses (Claude being
			// snarky, actually answering) stay in chat.
			var bubbleCmd tea.Cmd
			m, bubbleCmd = m.maybeFireCompanionBubble()
			if bubbleCmd != nil {
				cmds = append(cmds, bubbleCmd)
			}
		}
		// Final assistant message just committed — refreshViewport's
		// sticky-bottom honors a scrolled-up user. They explicitly
		// scrolled away while results were streaming; don't yank them
		// back when the turn finalizes.
		m.refreshViewport()
		m.input.Focus()

		// Drain pending messages: if the user typed while we were running,
		// auto-submit the first queued message now. Subsequent ones will be
		// sent in future agentDoneMsg cycles.
		if len(m.pendingMessages) > 0 {
			next := m.pendingMessages[0]
			m.pendingMessages = m.pendingMessages[1:]
			// Inject into input so the normal submit path fires.
			m.input.SetValue(next)
			// Send the synthetic Enter key to trigger submission.
			cmds = append(cmds, func() tea.Msg { return tea.KeyPressMsg{Code: tea.KeyEnter} })
		}

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
			// Clear conversation and show welcome card for the new account.
			m.messages = nil
			m.history = nil
			m.welcomeDismissed = false
			m.messages = append(m.messages, m.welcomeCard())
		}
		m.refreshViewport()
		m.vp.GotoBottom()
		return m, nil

	case accountSwitchedMsg:
		// Switch active account and reload credentials.
		store, err := auth.ListAccounts()
		if err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: "account switch: " + err.Error()})
			m.refreshViewport()
			return m, nil
		}
		if err := auth.SetActive(&store, msg.email); err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: err.Error()})
			m.refreshViewport()
			return m, nil
		}
		if m.cfg.LoadAuth != nil && m.cfg.NewAPIClient != nil && m.cfg.Loop != nil {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: fmt.Sprintf("Switching to %s…", msg.email)})
			m.refreshViewport()
			return m, func() tea.Msg {
				ctx := context.Background()
				bearer, prof, err := m.cfg.LoadAuth(ctx)
				if err != nil {
					if errors.Is(err, auth.ErrNotLoggedIn) {
						return authReloadMsg{err: fmt.Errorf("no saved credentials for %s — run /login to add this account", msg.email)}
					}
					return authReloadMsg{err: fmt.Errorf("account switch: %w", err)}
				}
				return authReloadMsg{client: m.cfg.NewAPIClient(bearer), profile: prof}
			}
		}
		m.refreshViewport()
		return m, nil

	case commandsLoginMsg:
		// Trigger login flow from account panel "+ Add account" action.
		m.loginPrompt = &loginPromptState{selected: 0}
		m.refreshViewport()
		return m, nil

	case trustAcceptedMsg:
		// Trust accepted and persisted — dialog already cleared in acceptTrust.
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

	case questionAskMsg:
		// AskUserQuestion: open the interactive selection dialog overlay.
		state := &questionAskState{
			question:   msg.question,
			options:    msg.options,
			multi:      msg.multi,
			reply:      msg.reply,
			focusedIdx: 0,
			selected:   make([]bool, len(msg.options)),
		}
		m.questionAsk = state
		m.refreshViewport()
		m.vp.GotoBottom()
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
		// Repoint cfg.Session so new turns append to the resumed file.
		if msg.filePath != "" {
			m.cfg.Session = session.FromFile(msg.filePath)
			// Restore coordinator mode if the session was in coordinator mode.
			if notice := coordinator.MatchSessionMode(m.cfg.Session.ReadMode()); notice != "" {
				m.messages = append(m.messages, Message{Role: RoleSystem, Content: notice})
			}
		}
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

	case buddyTickMsg:
		if m.companionName != "" {
			m.buddyFrame++
			return m, buddyTick()
		}
		return m, nil

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
		m.panel != nil || m.pluginPanel != nil || m.settingsPanel != nil ||
		m.permPrompt != nil || m.picker != nil || m.onboarding != nil ||
		m.questionAsk != nil || m.trustDialog != nil
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
	// Doctor panel intercepts all keys when active.
	if m.doctorPanel != nil {
		m2, cmd := m.handleDoctorPanelKey(msg)
		return m2, cmd, true
	}
	// Search results panel intercepts all keys when active.
	if m.searchPanel != nil {
		m2, cmd := m.handleSearchPanelKey(msg)
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
	// Trust dialog captures all keys before anything else.
	if m.trustDialog != nil {
		m2, cmd := m.handleTrustKey(msg)
		return m2, cmd, true
	}

	// AskUserQuestion dialog captures all keys when active.
	if m.questionAsk != nil {
		m2, cmd := m.handleQuestionKey(msg)
		return m2, cmd, true
	}

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
		// History navigation — always consume UP so it never falls through
		// to the viewport keymap. Scroll via trackpad/wheel (MouseWheelMsg)
		// or Shift+Up/Down.
		if !m.running {
			if len(m.inputHistory) > 0 {
				if m.historyIdx == -1 {
					m.historyDraft = m.input.Value()
					m.historyIdx = len(m.inputHistory) - 1
				} else if m.historyIdx > 0 {
					m.historyIdx--
				}
				m.input.SetValue(m.inputHistory[m.historyIdx])
				m.input.CursorEnd()
			}
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
		// History forward — always consume DOWN too.
		if !m.running {
			if m.historyIdx != -1 {
				if m.historyIdx < len(m.inputHistory)-1 {
					m.historyIdx++
					m.input.SetValue(m.inputHistory[m.historyIdx])
				} else {
					m.historyIdx = -1
					m.input.SetValue(m.historyDraft)
				}
				m.input.CursorEnd()
			}
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
		// Persist to ~/.claude/settings.json so the Config tool and any
		// re-read of permissions.defaultMode (e.g. from agents/subprocesses)
		// see the cycled mode immediately.
		_ = settings.SavePermissionsField("defaultMode", string(m.permissionMode))
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
			if len(m.pendingImages) > 0 || len(m.pendingPDFs) > 0 || len(m.pastedBlocks) > 0 {
				n := len(m.pendingImages) + len(m.pendingPDFs)
				m.pendingImages = nil
				m.pendingPDFs = nil
				m.pastedBlocks = nil
				m.input.SetValue(rePasteToken.ReplaceAllString(m.input.Value(), ""))
				if n > 0 {
					m.flashMsg = fmt.Sprintf("%d attachment(s) and paste(s) cleared.", n)
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
		if m.questionAsk != nil {
			m.questionAsk.reply <- nil
			m.questionAsk = nil
		}
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
		// Try image first, then PDF, then fall through to textarea text paste.
		img, imgErr := attach.ReadClipboardImage()
		if imgErr == nil && img != nil {
			m.pendingImages = append(m.pendingImages, img)
			n := len(m.pendingImages) + len(m.pendingPDFs)
			m.flashMsg = fmt.Sprintf("📎 %d attachment(s)  (ctrl+v for more · Enter to send · Esc to clear)", n)
			return m, tea.Tick(5*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} }), true
		}
		if errors.Is(imgErr, attach.ErrNotSupported) {
			return m, nil, false
		}
		// No image — try PDF.
		pdf, pdfErr := attach.ReadClipboardPDF()
		if pdfErr == nil && pdf != nil {
			m.pendingPDFs = append(m.pendingPDFs, pdf)
			n := len(m.pendingImages) + len(m.pendingPDFs)
			m.flashMsg = fmt.Sprintf("📎 %d attachment(s)  (ctrl+v for more · Enter to send · Esc to clear)", n)
			return m, tea.Tick(5*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} }), true
		}
		// Clipboard has text — fall through to textarea for normal paste.
		return m, nil, false

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
			// Queue the message for delivery after the current turn completes.
			text := strings.TrimSpace(m.input.Value())
			if text != "" && !strings.HasPrefix(text, "/") {
				m.pendingMessages = append(m.pendingMessages, text)
				m.input.Reset()
				m.flashMsg = fmt.Sprintf("[queued — %d pending]", len(m.pendingMessages))
				return m, tea.Tick(2*time.Second, func(_ time.Time) tea.Msg { return clearFlash{} }), true
			}
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

		// Build user message content. Prepend any queued images/PDFs so Claude
		// sees attachments alongside the text. Accumulate on ctrl+v, send all on Enter.
		userContent := make([]api.ContentBlock, 0, len(m.pendingImages)+len(m.pendingPDFs)+1)
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
		for _, pdf := range m.pendingPDFs {
			userContent = append(userContent, api.ContentBlock{
				Type: "document",
				Source: &api.ImageSource{
					Type:      "base64",
					MediaType: pdf.MediaType,
					Data:      pdf.Data,
				},
			})
		}
		m.pendingPDFs = nil
		// Process @file mentions: inject referenced file/dir contents as
		// additional blocks before the user's message text. PDFs become
		// type=document blocks; everything else becomes type=text.
		if cwd, err := os.Getwd(); err == nil {
			for _, ref := range attach.ProcessAtMentions(apiText, cwd) {
				if ref.IsPDF {
					userContent = append(userContent, api.ContentBlock{
						Type: "document",
						Source: &api.ImageSource{
							Type:      "base64",
							MediaType: "application/pdf",
							Data:      ref.PDFData,
						},
					})
				} else {
					userContent = append(userContent, api.ContentBlock{
						Type: "text",
						Text: attach.FormatAtResult(ref),
					})
				}
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

// renderCommandPicker renders the slash command picker dropdown.
func (m Model) renderCommandPicker() string {
	const maxItems = 8

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

	// Content area: Width(m.width-2) outer - 2 border - 2 padding (1 each side).
	contentW := m.width - 4

	// Compute name column width from the longest name across all matches so
	// the column stays stable as the user scrolls through results.
	nameColW := 0
	for _, cmd := range m.cmdMatches {
		n := len([]rune(cmd.Name)) + 1 // +1 for leading "/"
		if n > nameColW {
			nameColW = n
		}
	}
	const minDescW = 20
	const gap = 2
	if nameColW > contentW-minDescW-gap {
		nameColW = contentW - minDescW - gap
	}
	descMax := contentW - nameColW - gap
	indent := strings.Repeat(" ", nameColW+gap)

	var sb strings.Builder
	for i := start; i < end; i++ {
		if i > start {
			sb.WriteByte('\n')
		}
		cmd := m.cmdMatches[i]

		// Render name: "/" + name padded to nameColW.
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

		// Word-wrap description so it flows to additional lines instead of being cut off.
		descLines := cmdDescWrap(cmd.Description, descMax)
		sb.WriteString(namePart + strings.Repeat(" ", gap) + highlightMatch(descLines[0], query, stylePickerDesc, stylePickerHighlight))
		for _, dl := range descLines[1:] {
			sb.WriteByte('\n')
			sb.WriteString(indent + highlightMatch(dl, query, stylePickerDesc, stylePickerHighlight))
		}
	}

	pad := strings.Repeat(" ", outerPad)
	return indentLines(stylePickerBorder.Width(m.width).Render(sb.String()), pad)
}

// cmdDescWrap splits a description into lines of at most maxW runes, breaking
// on word boundaries. Always returns at least one element.
func cmdDescWrap(s string, maxW int) []string {
	if maxW <= 0 || len([]rune(s)) <= maxW {
		return []string{s}
	}
	var lines []string
	words := strings.Fields(s)
	var cur strings.Builder
	for _, w := range words {
		wlen := len([]rune(w))
		if cur.Len() == 0 {
			cur.WriteString(w)
		} else if cur.Len()+1+wlen <= maxW {
			cur.WriteByte(' ')
			cur.WriteString(w)
		} else {
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(w)
		}
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	if len(lines) == 0 {
		return []string{s}
	}
	return lines
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
		out, err := exec.CommandContext(context.Background(), "fd", append(args, ".", dir)...).Output()
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
	// Strip companion markers ([Name: ...]) stored from previous sessions.
	content := text.String()
	content = stripCompanionMarkerGlobal(content)
	return Message{Role: RoleAssistant, Content: content}
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

// applyCommandResult handles a slash command result in the TUI.
func (m Model) applyCommandResult(res commands.Result) (Model, tea.Cmd) {
	switch res.Type {
	case "clear":
		m.messages = nil
		m.history = nil
		m.pendingMessages = nil
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
	case "coordinator-toggle":
		// Persist the new mode to the session JSONL so /resume can restore it.
		if m.cfg.Session != nil {
			mode := "normal"
			if coordinator.IsActive() {
				mode = "coordinator"
			}
			_ = m.cfg.Session.SetMode(mode)
		}
		if res.Text != "" {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: res.Text})
		}
		m.refreshViewport()
		return m, nil
	case "error":
		m.messages = append(m.messages, Message{Role: RoleError, Content: res.Text})
		m.refreshViewport()
		return m, nil
	case "login":
		m.loginPrompt = &loginPromptState{selected: 0}
		m.refreshViewport()
		return m, nil
	case "account-switch":
		email := res.Text
		store, err := auth.ListAccounts()
		if err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: "account-switch: " + err.Error()})
			m.refreshViewport()
			return m, nil
		}
		if email == "" {
			// No email given — list accounts.
			var sb strings.Builder
			sb.WriteString("Logged-in accounts:\n\n")
			for e, entry := range store.Accounts {
				active := ""
				if e == store.Active {
					active = "  ← active"
				}
				fmt.Fprintf(&sb, "  %s  (added %s)%s\n", entry.Email, entry.AddedAt.Format("2006-01-02"), active)
			}
			if len(store.Accounts) == 0 {
				sb.WriteString("  (none — run /login to add an account)\n")
			}
			sb.WriteString("\nUse /login --switch <email> to activate an account.")
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: sb.String()})
			m.refreshViewport()
			return m, nil
		}
		if err := auth.SetActive(&store, email); err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: err.Error()})
			m.refreshViewport()
			return m, nil
		}
		// Reload credentials and API client live — same flow as after /login.
		if m.cfg.LoadAuth != nil && m.cfg.NewAPIClient != nil && m.cfg.Loop != nil {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: fmt.Sprintf("Switching to %s…", email)})
			m.refreshViewport()
			return m, func() tea.Msg {
				ctx := context.Background()
				bearer, prof, err := m.cfg.LoadAuth(ctx)
				if err != nil {
					// ErrNotLoggedIn means we switched the active pointer but have
					// no token for this account — guide the user to /login.
					if errors.Is(err, auth.ErrNotLoggedIn) {
						return authReloadMsg{err: fmt.Errorf("no saved credentials for %s — run /login to sign in to this account", email)}
					}
					return authReloadMsg{err: fmt.Errorf("account switch: %w", err)}
				}
				return authReloadMsg{client: m.cfg.NewAPIClient(bearer), profile: prof}
			}
		}
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
		case "accounts":
			defaultTab = settingsTabAccounts
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
				rows := make([]mcpInfoRow, 0, len(servers))
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
				in, _, cost := live.Tokens()
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
		saveConfigFn := func(id string, value interface{}) {
			// Map config item IDs to settings keys where they differ.
			key := id
			switch id {
			case "defaultPermissionMode":
				if s, ok := value.(string); ok {
					_ = settings.SavePermissionsField("defaultMode", permModeStoredVal(s))
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
		// Format: filePath\tage\ttitle\tmsgCount
		var sessions []resumeSession
		for _, line := range strings.Split(res.Text, "\n") {
			parts := strings.SplitN(line, "\t", 4)
			if len(parts) < 3 {
				continue
			}
			rs := resumeSession{
				filePath: parts[0],
				age:      parts[1],
				preview:  parts[2],
			}
			if len(parts) == 4 {
				_, _ = fmt.Sscanf(parts[3], "%d", &rs.msgCount)
			}
			sessions = append(sessions, rs)
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

	case "search-panel":
		// res.Text = newline-separated tab-separated results; res.Model = query term.
		var results []searchResult
		for _, line := range strings.Split(strings.TrimSpace(res.Text), "\n") {
			parts := strings.SplitN(line, "\t", 5)
			if len(parts) < 5 {
				continue
			}
			results = append(results, searchResult{
				filePath: parts[0],
				title:    parts[1],
				age:      parts[2],
				role:     parts[3],
				snippet:  parts[4],
			})
		}
		m.searchPanel = &searchPanelState{query: res.Model, results: results}
		m.refreshViewport()
		return m, nil

	case "doctor-panel":
		// res.Text = newline-separated check lines; res.Model = binary + platform.
		m.doctorPanel = &doctorPanelState{
			checks:   strings.Split(strings.TrimSpace(res.Text), "\n"),
			platform: res.Model,
		}
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
		_, _ = fmt.Sscanf(res.Text, "%d", &n)
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
	fmt.Fprintf(&sb, "Input tokens:  %d\n", m.totalInputTokens)
	fmt.Fprintf(&sb, "Output tokens: %d\n", m.totalOutputTokens)
	if m.costUSD > 0 {
		fmt.Fprintf(&sb, "Estimated cost: $%.4f", m.costUSD)
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
// TurnCosts returns a copy of the per-turn cost deltas recorded this session.
func (m *Model) TurnCosts() []float64 {
	out := make([]float64, len(m.turnCosts))
	copy(out, m.turnCosts)
	return out
}

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
		// Disable the viewport's built-in key bindings entirely — "j","k","u","b",
		// space, etc. would fire for any non-consumed key. We handle scrolling
		// explicitly via Shift+Up/Down/PgUp/PgDn in handleKey.
		m.vp.KeyMap = viewport.KeyMap{} // disable built-in key bindings
		m.vp.MouseWheelEnabled = true   // handle tea.MouseWheelMsg for trackpad/wheel
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
	first := true
	for _, msg := range m.messages {
		rendered := renderMessage(msg, w)
		if rendered == "" {
			continue // skip empty renders (e.g. pure companion quip messages)
		}
		if !first {
			sb.WriteString(separator(w))
			sb.WriteByte('\n')
		}
		first = false
		sb.WriteString(rendered)
		sb.WriteByte('\n')
	}
	if m.streaming != "" {
		if !first {
			sb.WriteString(separator(w))
			sb.WriteByte('\n')
		}
		displayStreaming := m.stripCompanionMarker(m.streaming)
		if displayStreaming != "" {
			sb.WriteString(renderMessage(Message{Role: RoleAssistant, Content: displayStreaming}, w))
			sb.WriteByte('\n')
		}
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
		// MouseModeCellMotion: scroll wheel arrives as tea.MouseWheelMsg
		// (separate from UP/DOWN key events) so trackpad/wheel scrolls the
		// viewport while UP/DOWN navigates input history — matching CC's UX.
		// Trade-off: text selection requires Shift+drag (standard for
		// mouse-mode TUIs like vim/tmux). Text copy still works via ^Y.
		v.MouseMode = tea.MouseModeCellMotion
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
	} else if m.doctorPanel != nil {
		overlayBox = m.renderDoctorPanel()
	} else if m.searchPanel != nil {
		overlayBox = m.renderSearchPanel()
	} else if m.picker != nil {
		overlayBox = m.renderPicker()
	} else if m.onboarding != nil {
		overlayBox = m.renderOnboarding()
	} else if m.trustDialog != nil {
		overlayBox = m.renderTrustDialog()
	} else if m.permPrompt != nil {
		overlayBox = m.renderPermissionPrompt()
	} else if m.questionAsk != nil {
		overlayBox = m.renderQuestionDialog()
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
	// If clipboard images or PDFs are queued, prepend an attachment badge.
	if n := len(m.pendingImages) + len(m.pendingPDFs); n > 0 {
		parts := []string{}
		if ni := len(m.pendingImages); ni > 0 {
			parts = append(parts, fmt.Sprintf("%d image(s)", ni))
		}
		if np := len(m.pendingPDFs); np > 0 {
			parts = append(parts, fmt.Sprintf("%d PDF(s)", np))
		}
		label := "📎 [" + strings.Join(parts, ", ") + "]"
		badge := styleStatusAccent.Render(label) + "  " + stylePickerDesc.Render("ctrl+v for more · Enter to send · Esc to clear")
		innerView = badge + "\n" + innerView
	}
	inputBox := bStyle.Width(m.width).Render(innerView)

	// Status bar — fixed left-anchor layout so nothing shifts when mode changes.
	//
	// left:  edgePad  conduit  [mode badge]  |  model  [| ctx]  [| cost]
	// right: hints  edgePad
	// pad:   all remaining space between left and right
	edgePad := strings.Repeat(" ", outerPad)
	barSep := styleStatus.Render(" |")

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
	if coordinator.IsActive() {
		leftParts = append(leftParts, styleModePurple.Render("⬡ coordinator"))
	}
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
	var inputTok, outputTok int
	for _, msg := range m.history {
		t := 0
		for _, b := range msg.Content {
			t += tokens.Estimate(b.Text)
		}
		if msg.Role == "assistant" {
			outputTok += t
		} else {
			inputTok += t
		}
	}
	m.totalInputTokens = inputTok + outputTok // billing input = full context
	m.totalOutputTokens = outputTok
	// Opus 4.7: $15/M input + $75/M output.
	m.costUSD = float64(inputTok)*15.0/1_000_000 + float64(outputTok)*75.0/1_000_000
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
	m.cfg.Live.SetTokens(m.totalInputTokens, m.totalOutputTokens, m.costUSD)
	m.cfg.Live.SetRateLimitWarning(m.rateLimitWarning)
	if m.cfg.Session != nil {
		m.cfg.Live.SetSessionID(m.cfg.Session.ID)
		m.cfg.Live.SetSessionFile(m.cfg.Session.FilePath)
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
