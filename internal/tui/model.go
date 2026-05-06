// Package tui implements the Bubble Tea TUI for conduit.
package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

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
	"github.com/icehunter/conduit/internal/coordinator"
	"github.com/icehunter/conduit/internal/keybindings"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/planusage"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/profile"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tools/tasktool"
	"github.com/icehunter/conduit/internal/tui/workinganim"
)

// chromeHeight returns the number of terminal rows consumed by everything
// except the viewport, given the current input row count and terminal
// height. Dynamic so multi-line input doesn't permanently squeeze chat.
//
//	working row:   1
//	input border:  1 (top) + 1 (bottom) = 2
//	input text:    inputRows (1..inputMaxRows)
//	status bar:    1
//
// The input is capped at inputMaxRows visible rows (~30% of the screen,
// floor 1, ceiling 12) so the chat viewport always keeps at least 70% of
// the terminal. Beyond the cap, the textarea scrolls internally.
const (
	chromeFixed   = 4 // working row + 2 borders + status (everything except input rows)
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
	RoleLocal
	RoleTool
	RoleAssistantInfo
	RoleError
	RoleSystem
)

// Message is one entry in the displayed conversation.
type Message struct {
	Role     Role
	Content  string
	ToolName string
	ToolID   string

	ToolInput    string
	ToolStarted  time.Time
	ToolDuration time.Duration
	ToolError    bool

	AssistantModel    string
	AssistantDuration time.Duration
	AssistantCost     float64

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
	localCallDoneMsg struct {
		turnID int
		call   commands.LocalCall
		chat   bool
		text   string
		err    error
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
	// loginDoneMsg is sent when the OAuth flow completes and live credentials
	// have been reloaded.
	loginDoneMsg struct {
		client  *api.Client
		profile *profile.Info
		tokens  auth.PersistedTokens
		err     error
	}
	// authReloadMsg is sent after loginDone to deliver the refreshed API client + profile.
	authReloadMsg struct {
		client  *api.Client
		profile *profile.Info
		tokens  auth.PersistedTokens
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

	planUsageMsg struct {
		info planusage.Info
		err  error
	}
	planUsageTickMsg struct{}
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
	LoadAuth func(ctx context.Context) (auth.PersistedTokens, *profile.Info, error)
	// NewAPIClient constructs a fresh client for the given persisted token.
	NewAPIClient func(auth.PersistedTokens) *api.Client
	// Live is the shared state bag readable from command callbacks outside
	// the Bubble Tea event loop. Populated by the model on each Update.
	Live *LiveState
	// NeedsTrust is true when the current working directory has not been
	// marked trusted in ~/.claude.json. The TUI shows the trust dialog
	// before allowing any agent interaction.
	NeedsTrust bool
	// SetTrusted persists acceptance of the workspace trust dialog.
	SetTrusted func() error
	// UsageStatusEnabled controls the conduit-only plan usage footer.
	UsageStatusEnabled bool
	// InitialLocalMode restores the hidden /local-mode compatibility bridge.
	InitialLocalMode bool
	// InitialLocalServer is the MCP server normal chat should route to when
	// InitialLocalMode is enabled.
	InitialLocalServer string
	// InitialLocalDirectTool is the MCP tool used for normal local-mode chat.
	InitialLocalDirectTool string
	// InitialLocalImplementTool is the MCP tool used for scoped local diffs.
	InitialLocalImplementTool string
	// InitialActiveProvider is conduit's provider routing selector.
	InitialActiveProvider *settings.ActiveProviderSettings
	// InitialProviders/Roles are conduit's named provider role bindings.
	InitialProviders map[string]settings.ActiveProviderSettings
	InitialRoles     map[string]string
	// BackgroundModel returns the model for helper calls such as /compact.
	BackgroundModel func() string
	// FetchPlanUsage returns the current Claude plan usage windows for a
	// provider/account that supports plan usage.
	FetchPlanUsage func(context.Context, settings.ActiveProviderSettings) (planusage.Info, error)
}

// Model is the Bubble Tea model.
type Model struct {
	cfg      Config
	messages []Message
	history  []api.Message

	input   textarea.Model
	vp      viewport.Model
	working workinganim.Anim

	width  int
	height int
	panelH int

	running         bool
	cancelled       bool // true after Ctrl+C; cleared when next turn starts
	cancelTurn      context.CancelFunc
	streaming       string
	apiRetryStatus  string
	turnID          int               // incremented each turn; agentDoneMsg with stale ID is ignored
	turnStarted     time.Time         // wall time when the current agent turn started
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
	atQuery    string   // last @ fragment used to populate atMatches
	atCwd      string   // cwd used to populate atMatches

	totalInputTokens  int
	totalOutputTokens int
	costUSD           float64
	prevCostUSD       float64 // cost before the current turn started; used to compute per-turn delta

	// turnCosts records the cost delta for each completed assistant turn,
	// most-recent last. Used by /cost to show per-turn breakdown.
	turnCosts []float64

	// flashMsg is shown in the working row briefly (e.g. "Copied!").
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

	usageStatusEnabled bool
	planUsage          planusage.Info
	planUsageErr       string
	planUsageFetching  bool
	planUsageCachedAt  time.Time // when the last successful fetch completed
	planUsageBackoff   time.Time // don't issue another fetch before this time
	planUsageProvider  string

	// fastMode is true when /fast is active (showing ⚡ badge).
	fastMode bool

	// activeProvider is conduit's provider routing selector. For now the TUI
	// supports Claude subscription and MCP-backed private/local providers.
	activeProvider *settings.ActiveProviderSettings
	providers      map[string]settings.ActiveProviderSettings
	roles          map[string]string

	// localMode/local* are compatibility fields for the hidden /local-mode and
	// /local debug commands while provider routing settles.
	localMode          bool
	localModeServer    string
	localDirectTool    string
	localImplementTool string

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

	// helpOverlay holds the keyboard shortcut help modal. Non-nil when open.
	helpOverlay *helpOverlayState

	// verboseMode shows full tool output bodies; false (compact) is the default.
	verboseMode bool

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

	m := Model{
		cfg:                cfg,
		input:              ta,
		working:            *workinganim.New(14, "Thinking", colorAccent, colorTool, colorDim, colorWindowBg),
		modelName:          cfg.ModelName,
		localMode:          cfg.InitialLocalMode,
		localModeServer:    cfg.InitialLocalServer,
		localDirectTool:    cfg.InitialLocalDirectTool,
		localImplementTool: cfg.InitialLocalImplementTool,
		historyIdx:         -1,
		loginFlowMsgStart:  -1,
		usageStatusEnabled: cfg.UsageStatusEnabled,
		providers:          cloneProviderMap(cfg.InitialProviders),
		roles:              cloneStringMap(cfg.InitialRoles),
	}
	if cfg.InitialActiveProvider != nil {
		provider := *cfg.InitialActiveProvider
		m.activeProvider = &provider
		if provider.Kind == "mcp" {
			m.localMode = true
			if m.localModeServer == "" {
				m.localModeServer = provider.Server
			}
			if m.localDirectTool == "" {
				m.localDirectTool = provider.DirectTool
			}
			if m.localImplementTool == "" {
				m.localImplementTool = provider.ImplementTool
			}
			m.ensureDefaultLocalTools()
		}
	}
	// Sync displayed permission mode from the gate before rendering any
	// startup messages. The active provider is role-dependent, so welcome
	// cards must see the same mode as the footer and agent loop.
	if cfg.Gate != nil {
		m.permissionMode = cfg.Gate.Mode()
		m.applyEffectiveProviderForMode()
	}
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
		m.messages = append(m.messages, historyToDisplayMessages(cfg.ResumedHistory)...)
		m.tallyTokens()
	} else {
		m.messages = append(m.messages, m.welcomeCard())
	}

	// Load user keybindings. Conduit owns ~/.conduit/keybindings.json; the
	// Claude path is a compatibility fallback for users who have not copied
	// bindings over yet.
	keybindingsDir := settings.ConduitDir()
	if _, err := os.Stat(keybindings.UserFilePath(keybindingsDir)); err != nil {
		keybindingsDir = settings.ClaudeDir()
	}
	if bindings, err := keybindings.LoadAll(keybindingsDir); err == nil {
		m.kb = keybindings.NewResolver(bindings)
	} else {
		m.kb = keybindings.NewResolver(keybindings.Defaults())
	}

	// Seed plan usage from disk cache so the footer shows immediately on
	// startup and multiple instances don't all hammer the API at once.
	if m.usageStatusEnabled {
		cacheKey := ""
		if provider, ok := m.planUsageProviderSettings(); ok {
			cacheKey = settings.ProviderKey(provider)
		}
		if entry, err := planusage.LoadCacheForKeyWithFallback(settings.ConduitDir(), settings.ClaudeDir(), cacheKey); err == nil && planUsageCacheEntryUseful(entry) {
			m.planUsage = entry.Info
			m.planUsageCachedAt = entry.CachedAt
			m.planUsageProvider = cacheKey
			if !entry.BackoffUntil.IsZero() && time.Now().Before(entry.BackoffUntil) {
				m.planUsageBackoff = entry.BackoffUntil
			}
		}
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

func cloneProviderMap(in map[string]settings.ActiveProviderSettings) map[string]settings.ActiveProviderSettings {
	if len(in) == 0 {
		return map[string]settings.ActiveProviderSettings{}
	}
	out := make(map[string]settings.ActiveProviderSettings, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// Init starts the blink + working indicator tick. Also kicks off the MCP approval
// picker if any project-scope servers are awaiting consent, and the
// coordinator-panel tick that drives the active-task footer.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{textarea.Blink, m.working.Start(), coordTick()}
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
	if m.usageStatusEnabled && m.cfg.FetchPlanUsage != nil {
		if (m.planUsageCachedAt.IsZero() || time.Since(m.planUsageCachedAt) >= planUsageRefreshInterval) &&
			!time.Now().Before(m.planUsageBackoff) {
			m2, cmd := m.startPlanUsageFetch()
			m = m2
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		} else {
			cmds = append(cmds, planUsageTick())
		}
	}
	return tea.Batch(cmds...)
}

type buddyTickMsg struct{}

const planUsageRefreshInterval = 60 * time.Second

func fetchPlanUsageCmd(fetch func(context.Context, settings.ActiveProviderSettings) (planusage.Info, error), provider settings.ActiveProviderSettings) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		info, err := fetch(ctx, provider)
		return planUsageMsg{info: info, err: err}
	}
}

func (m Model) startPlanUsageFetch() (Model, tea.Cmd) {
	if !m.usageStatusEnabled || m.cfg.FetchPlanUsage == nil {
		return m, nil
	}
	provider, ok := m.planUsageProviderSettings()
	if !ok {
		m.planUsageFetching = false
		m.planUsageErr = ""
		m.planUsage = planusage.Info{}
		m.planUsageCachedAt = time.Time{}
		m.planUsageBackoff = time.Time{}
		return m, nil
	}
	m.planUsageFetching = true
	m.planUsageProvider = settings.ProviderKey(provider)
	return m, fetchPlanUsageCmd(m.cfg.FetchPlanUsage, provider)
}

func (m Model) planUsageProviderSettings() (settings.ActiveProviderSettings, bool) {
	provider, ok := m.providerForCurrentMode()
	if !ok || provider.Kind != "claude-subscription" || provider.Account == "" {
		return settings.ActiveProviderSettings{}, false
	}
	return provider, true
}

func planUsageCacheEntryUseful(entry planusage.CacheEntry) bool {
	return !entry.CachedAt.IsZero() || !entry.BackoffUntil.IsZero()
}

// savePlanUsageCacheCmd persists the cache entry to disk as a fire-and-forget
// Cmd — failures are silently dropped (non-fatal).
func savePlanUsageCacheCmd(dir, key string, entry planusage.CacheEntry) tea.Cmd {
	return func() tea.Msg {
		_ = planusage.SaveCacheForKey(dir, key, entry)
		return nil
	}
}

func planUsageTick() tea.Cmd {
	return tea.Tick(planUsageRefreshInterval, func(time.Time) tea.Msg {
		return planUsageTickMsg{}
	})
}

// planUsageErrBackoff returns how long to wait before retrying after an error.
// Rate-limit errors use max(Retry-After, 5min); other errors use 30s.
func planUsageErrBackoff(err error) time.Duration {
	var rle *planusage.RateLimitError
	if errors.As(err, &rle) {
		if rle.RetryAfter > 5*time.Minute {
			return rle.RetryAfter
		}
		return 5 * time.Minute
	}
	return 30 * time.Second
}

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
			// Mark any in-flight tool rows as interrupted so they don't
			// stay stuck showing "running…" after the cancel.
			for i := range m.messages {
				if m.messages[i].Role == RoleTool && m.messages[i].Content == "running…" {
					m.messages[i].Content = "interrupted."
					m.messages[i].ToolError = true
				}
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
			m.doctorPanel != nil || m.searchPanel != nil || m.helpOverlay != nil
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
				m = m.updateAtMatches()
			}
			return m, tea.Batch(cmds...)
		}
		// Not consumed — fall through so textarea and viewport get the key.

	case agentMsg:
		m = m.applyAgentEvent(msg.event)
		return m, nil

	case planUsageMsg:
		m.planUsageFetching = false
		if msg.err != nil {
			backoff := planUsageErrBackoff(msg.err)
			m.planUsageBackoff = time.Now().Add(backoff)
			// Only surface the error when we have no cached data to show.
			if m.planUsageCachedAt.IsZero() {
				m.planUsageErr = msg.err.Error()
			}
			// Persist updated backoff so other instances (and restarts) respect it.
			entry := planusage.CacheEntry{
				Info:         m.planUsage,
				CachedAt:     m.planUsageCachedAt,
				BackoffUntil: m.planUsageBackoff,
			}
			saveCacheCmd := savePlanUsageCacheCmd(settings.ConduitDir(), m.planUsageProvider, entry)
			if m.usageStatusEnabled {
				return m, tea.Batch(planUsageTick(), saveCacheCmd)
			}
			return m, saveCacheCmd
		}
		m.planUsage = msg.info
		m.planUsageCachedAt = time.Now()
		m.planUsageBackoff = time.Time{}
		m.planUsageErr = ""
		entry := planusage.CacheEntry{
			Info:     m.planUsage,
			CachedAt: m.planUsageCachedAt,
		}
		saveCacheCmd := savePlanUsageCacheCmd(settings.ConduitDir(), m.planUsageProvider, entry)
		if m.usageStatusEnabled {
			return m, tea.Batch(planUsageTick(), saveCacheCmd)
		}
		return m, saveCacheCmd

	case planUsageTickMsg:
		if !m.usageStatusEnabled || m.cfg.FetchPlanUsage == nil || m.planUsageFetching {
			return m, nil
		}
		if time.Now().Before(m.planUsageBackoff) {
			return m, planUsageTick()
		}
		return m.startPlanUsageFetch()

	case agentDoneMsg:
		if msg.turnID != m.turnID {
			// Stale completion from a previous (interrupted) turn — discard.
			return m, nil
		}
		m.running = false
		m.cancelled = false
		m.cancelTurn = nil
		m.apiRetryStatus = ""
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
			turnCostDelta := m.costUSD - m.prevCostUSD
			if turnCostDelta > 0 {
				m.turnCosts = append(m.turnCosts, turnCostDelta)
				if m.cfg.Live != nil {
					m.cfg.Live.AppendTurnCost(turnCostDelta)
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
			m.appendAssistantInfo(turnCostDelta)
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
		loadAuth := m.cfg.LoadAuth
		newAPIClient := m.cfg.NewAPIClient
		return m, func() tea.Msg {
			display := &tuiLoginDisplay{prog: prog}
			if err := runLoginFlow(useClaudeAI, display); err != nil {
				prog.Send(loginDoneMsg{err: err})
				return nil
			}
			if loadAuth != nil && newAPIClient != nil {
				tok, prof, err := loadAuth(context.Background())
				if err != nil {
					prog.Send(loginDoneMsg{err: fmt.Errorf("reload credentials: %w", err)})
					return nil
				}
				prog.Send(loginDoneMsg{client: newAPIClient(tok), profile: prof, tokens: tok})
				return nil
			}
			prog.Send(loginDoneMsg{})
			return nil
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
		if msg.client != nil && m.cfg.Loop != nil {
			m.cfg.Loop.SetClient(msg.client)
			if msg.profile != nil {
				m.cfg.Profile = *msg.profile
			}
			m.messages = nil
			m.history = nil
			m.welcomeDismissed = false
			if _, ok := m.activeMCPProvider(); !ok {
				provider := accountBackedActiveProvider(m.modelName, m.cfg.Profile.Email, msg.tokens)
				m.setActiveProvider(provider)
				if suffix := persistActiveProvider(provider); suffix != "" {
					m.messages = append(m.messages, Message{Role: RoleError, Content: strings.TrimSpace(suffix)})
				}
			}
		}
		m.messages = append(m.messages, m.welcomeCard())
		m.refreshViewport()
		m.vp.GotoBottom()
		if msg.client != nil && m.usageStatusEnabled && m.cfg.FetchPlanUsage != nil {
			return m.startPlanUsageFetch()
		}
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
			if _, ok := m.activeMCPProvider(); !ok {
				provider := accountBackedActiveProvider(m.modelName, m.cfg.Profile.Email, msg.tokens)
				m.setActiveProvider(provider)
				if suffix := persistActiveProvider(provider); suffix != "" {
					m.messages = append(m.messages, Message{Role: RoleError, Content: strings.TrimSpace(suffix)})
				}
			}
			if m.usageStatusEnabled && m.cfg.FetchPlanUsage != nil {
				return m.startPlanUsageFetch()
			}
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
		if err := auth.SetActive(&store, msg.account); err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: err.Error()})
			m.refreshViewport()
			return m, nil
		}
		if m.cfg.LoadAuth != nil && m.cfg.NewAPIClient != nil && m.cfg.Loop != nil {
			m.messages = append(m.messages, Message{Role: RoleSystem, Content: fmt.Sprintf("Switching to %s…", msg.account)})
			m.refreshViewport()
			return m, func() tea.Msg {
				ctx := context.Background()
				tok, prof, err := m.cfg.LoadAuth(ctx)
				if err != nil {
					if errors.Is(err, auth.ErrNotLoggedIn) {
						return authReloadMsg{err: fmt.Errorf("no saved credentials for %s — run /login to add this account", msg.account)}
					}
					return authReloadMsg{err: fmt.Errorf("account switch: %w", err)}
				}
				return authReloadMsg{client: m.cfg.NewAPIClient(tok), profile: prof, tokens: tok}
			}
		}
		m.refreshViewport()
		return m.startPlanUsageFetch()

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

	case localCallDoneMsg:
		if msg.turnID != m.turnID {
			return m, nil
		}
		m.running = false
		m.cancelTurn = nil
		if msg.err != nil {
			m.messages = append(m.messages, Message{Role: RoleError, Content: fmt.Sprintf("Local call failed: %v", msg.err)})
			if msg.chat && len(m.history) > 0 && m.history[len(m.history)-1].Role == "user" {
				m.history = m.history[:len(m.history)-1]
			}
		} else {
			label := msg.call.Server
			if label == "" {
				label = "local"
			}
			text := strings.TrimSpace(msg.text)
			if text == "" {
				text = "(empty local response)"
			}
			m.messages = append(m.messages, Message{Role: RoleLocal, Content: text, ToolName: label})
			if msg.chat {
				m.history = append(m.history, api.Message{
					Role:         "assistant",
					Content:      []api.ContentBlock{{Type: "text", Text: text}},
					ProviderKind: "mcp",
					Provider:     label,
				})
				m.persistNewMessages(m.history)
			}
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
		// could call coordTick if needed, but the working indicator tick is
		// already running during agent.Run so we don't lose ticks during work).
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
		m.messages = append(m.messages, historyToDisplayMessages(msg.msgs)...)
		m.tallyTokens()
		m.refreshViewport()
		m.vp.GotoBottom()
		return m, nil

	case workinganim.StepMsg:
		cmds = append(cmds, m.working.Animate(msg))

	case clearFlash:
		m.flashMsg = ""
		return m, nil

	case clearBubble:
		m.companionBubble = ""
		return m, nil

	case companionBubbleMsg:
		m.companionBubble = msg.text
		return m, tea.Tick(5*time.Second, func(_ time.Time) tea.Msg { return clearBubble{} })

	case buddyTickMsg:
		if m.companionName != "" {
			m.buddyFrame++
			return m, buddyTick()
		}
		return m, nil

	case setPermissionModeMsg:
		m.applyPermissionMode(msg.mode)
		return m.startPlanUsageFetch()

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
			cap := chromeHeight(nextLines, m.height-m.usageFooterRows()) - chromeFixed
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
		m = m.updateAtMatches()
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
	// Help overlay intercepts all keys when active.
	if m.helpOverlay != nil {
		m2, cmd := m.handleHelpOverlayKey(msg)
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
		if m.commandPickerActive() {
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
		if m.commandPickerActive() {
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
		m.applyPermissionMode(m.permissionMode)
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
		m2, usageCmd := m.startPlanUsageFetch()
		return m2, tea.Batch(usageCmd, tea.Tick(1500*time.Millisecond, func(_ time.Time) tea.Msg { return clearFlash{} })), true

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
		if m.commandPickerActive() {
			if msg.String() == "tab" {
				// Tab: complete to the command name with trailing space, close picker.
				if len(m.cmdMatches) > 0 {
					m.input.SetValue("/" + m.cmdMatches[m.cmdSelected].Name + " ")
					m.input.CursorEnd()
				}
			} else {
				m.input.Reset()
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
			for i := range m.messages {
				if m.messages[i].Role == RoleTool && m.messages[i].Content == "running…" {
					m.messages[i].Content = "interrupted."
					m.messages[i].ToolError = true
				}
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

	case "ctrl+o":
		m.verboseMode = !m.verboseMode
		if m.verboseMode {
			m.flashMsg = "verbose mode on (ctrl+o to toggle)"
		} else {
			m.flashMsg = "compact mode on (ctrl+o to toggle)"
		}
		m.refreshViewport()
		return m, tea.Tick(1500*time.Millisecond, func(_ time.Time) tea.Msg { return clearFlash{} }), true

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
		activeMCP, usingMCPProvider := m.activeMCPProvider()
		if m.noAuth && !usingMCPProvider {
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
		if usingMCPProvider {
			apiText := m.expandPastePlaceholders(text)
			m.pastedBlocks = nil
			m.pendingImages = nil
			m.pendingPDFs = nil
			userContent := m.userTextContent(apiText)
			m.history = append(m.history, api.Message{
				Role:    "user",
				Content: userContent,
			})
			call := commands.NewLocalDirectCallWithTool(activeMCP.Server, activeMCP.DirectTool, localPromptFromContent(userContent))
			m.running = true
			m.cancelled = false
			m.streaming = ""
			m.apiRetryStatus = ""
			m.turnStarted = time.Now()
			m.refreshViewport()
			m.vp.GotoBottom()
			m.turnID++
			turnID := m.turnID
			ctx, cancel := context.WithCancel(context.Background())
			m.cancelTurn = cancel
			manager := m.cfg.MCPManager
			if manager == nil {
				m.running = false
				m.cancelTurn = nil
				m.messages = append(m.messages, Message{Role: RoleError, Content: "Local provider unavailable: MCP manager is not configured."})
				m.refreshViewport()
				return m, nil, true
			}
			input, err := json.Marshal(call.Arguments)
			if err != nil {
				m.running = false
				m.cancelTurn = nil
				m.messages = append(m.messages, Message{Role: RoleError, Content: "Local provider input invalid: " + err.Error()})
				m.refreshViewport()
				return m, nil, true
			}
			return m, func() tea.Msg {
				return runLocalCall(ctx, manager, call, input, turnID, true)
			}, true
		}
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
		userContent = append(userContent, m.atMentionContent(apiText)...)
		userContent = append(userContent, api.ContentBlock{Type: "text", Text: apiText})
		m.history = append(m.history, api.Message{
			Role:    "user",
			Content: userContent,
		})
		m.running = true
		m.cancelled = false
		m.streaming = ""
		m.apiRetryStatus = ""
		m.turnStarted = time.Now()
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
	// Base must have BOTH fg and bg — every other style inherits from Base.
	// Without explicit fg, text rendered on the cursor row uses terminal
	// default fg (light gray on most terminals = unreadable on light theme).
	taBase := lipgloss.NewStyle().Foreground(colorFg).Background(colorWindowBg)
	taPlaceholder := lipgloss.NewStyle().Foreground(colorMuted).Background(colorWindowBg)

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
	styles.Cursor.Color = colorFg
	ta.SetStyles(styles)
}
