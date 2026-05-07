// Package tui implements the Bubble Tea TUI for conduit.
package tui

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/attach"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/buddy"
	"github.com/icehunter/conduit/internal/commands"
	"github.com/icehunter/conduit/internal/keybindings"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/planusage"
	"github.com/icehunter/conduit/internal/profile"
	"github.com/icehunter/conduit/internal/session"
	"github.com/icehunter/conduit/internal/settings"
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
	AssistantLabel    string
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
	// NewProviderAPIClient constructs a client for non-account providers such
	// as OpenAI-compatible endpoints.
	NewProviderAPIClient func(settings.ActiveProviderSettings) (*api.Client, error)
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

	running          bool
	cancelled        bool // true after Ctrl+C; cleared when next turn starts
	cancelTurn       context.CancelFunc
	streaming        string
	apiRetryStatus   string
	turnID           int               // incremented each turn; agentDoneMsg with stale ID is ignored
	turnStarted      time.Time         // wall time when the current agent turn started
	turnAssistant    string            // display label captured for the provider answering the current turn
	turnProvider     string            // provider/model captured for transcript display metadata
	turnProviderKind string            // provider kind captured for transcript display metadata
	pendingMessages  []string          // messages typed while agent is running; drained after turn ends
	questionAsk      *questionAskState // non-nil when AskUserQuestion is waiting for user input

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
		if provider.Kind == settings.ProviderKindMCP {
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

	m.applyEffectiveProviderForMode()
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

// mcpApprovalMsg is sent on startup when project-scope MCP servers need
// user approval. The Update handler opens a picker per server, sequentially.
type mcpApprovalMsg struct {
	pending []string
}

// coordTickMsg fires every second whenever active sub-agent tasks exist,
// so the coordinator footer panel re-renders with updated elapsed times.
type coordTickMsg struct{}

// buddyTickMsg is sent on each companion animation frame tick.
type buddyTickMsg struct{}
