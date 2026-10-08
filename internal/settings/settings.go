// Package settings loads and merges Conduit and Claude-compatible settings files.
//
// Priority order (later overrides earlier — most-specific scope wins):
//  1. ~/.claude/settings.json                (first-run import only when conduit.json is absent)
//  2. ~/.conduit/conduit.json                (Conduit-owned user-global config)
//  3. <project>/.claude/settings.json        (Claude project shared)
//  4. <project>/.claude/settings.local.json  (Claude project local, read-only import)
//  5. <project>/.conduit/settings.json       (Conduit project shared)
//  6. <project>/.conduit/settings.local.json (Conduit project local, gitignored)
//
// Mirrors src/utils/config.ts and src/utils/settings/settings.ts.
package settings

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	internalmodel "github.com/icehunter/conduit/internal/model"
)

// Permissions is the permissions section of a settings file.
type Permissions struct {
	Allow          []string `json:"allow"`
	Deny           []string `json:"deny"`
	Ask            []string `json:"ask"`
	DefaultMode    string   `json:"defaultMode"`
	AdditionalDirs []string `json:"additionalDirectories"`
}

// Hook is one hook entry. Type determines which fields are used.
// Mirrors src/schemas/hooks.ts (BashCommandHookSchema, HttpHookSchema,
// PromptHookSchema, AgentHookSchema).
type Hook struct {
	// Common fields
	Type          string `json:"type"`                    // "command" | "http" | "prompt" | "agent"
	StatusMessage string `json:"statusMessage,omitempty"` // spinner text while running
	If            string `json:"if,omitempty"`            // permission rule to gate firing
	TimeoutSecs   int    `json:"timeout,omitempty"`       // per-hook timeout override (seconds)
	Once          bool   `json:"once,omitempty"`          // remove after first execution
	Async         bool   `json:"async,omitempty"`         // fire-and-forget (non-blocking)

	// type="command"
	Command string `json:"command,omitempty"` // shell command

	// type="http"
	URL            string            `json:"url,omitempty"`            // POST target
	Headers        map[string]string `json:"headers,omitempty"`        // extra headers
	AllowedEnvVars []string          `json:"allowedEnvVars,omitempty"` // vars to interpolate in headers

	// type="prompt" | "agent"
	Prompt string `json:"prompt,omitempty"` // LLM prompt (may contain $ARGUMENTS)
	Model  string `json:"model,omitempty"`  // model override
}

// HookMatcher is a matcher + hooks pair.
type HookMatcher struct {
	Matcher    string `json:"matcher"` // tool name or glob, "" = all
	Hooks      []Hook `json:"hooks"`
	SourceFile string `json:"-"` // path of the settings file this came from; not serialized
	PluginRoot string `json:"-"` // plugin install dir; non-empty for plugin-sourced hooks
}

// IsProjectLocal reports whether this matcher came from a project-scoped
// settings file (i.e. <cwd>/.claude/settings.json or settings.local.json).
// User-global hooks (~/.claude/settings.json, ~/.conduit/conduit.json) return false.
func (h HookMatcher) IsProjectLocal(cwd string) bool {
	if h.SourceFile == "" || cwd == "" {
		return false
	}
	clauDir := filepath.Join(cwd, ".claude") + string(filepath.Separator)
	return strings.HasPrefix(h.SourceFile, clauDir)
}

// HooksSettings is the hooks section.
type HooksSettings struct {
	PreToolUse    []HookMatcher `json:"PreToolUse"`
	PostToolUse   []HookMatcher `json:"PostToolUse"`
	SessionStart  []HookMatcher `json:"SessionStart"`
	Stop          []HookMatcher `json:"Stop"`
	TeammateIdle  []HookMatcher `json:"TeammateIdle"`
	TaskCreated   []HookMatcher `json:"TaskCreated"`
	TaskCompleted []HookMatcher `json:"TaskCompleted"`
	// UserPromptSubmit hooks run before a prompt reaches the model and can
	// block it, add context or rewrite it. Matchers are ignored.
	UserPromptSubmit []HookMatcher `json:"UserPromptSubmit"`
}

// Settings is the parsed content of one settings file.
type Settings struct {
	Permissions Permissions   `json:"permissions"`
	Hooks       HooksSettings `json:"hooks"`
	// Env holds extra environment variables for tool execution.
	Env map[string]string `json:"env"`
	// EnabledPlugins mirrors the real Claude Code enabledPlugins field.
	// Key is "pluginName@marketplace", value is true/false.
	EnabledPlugins map[string]bool `json:"enabledPlugins,omitempty"`
	// OnboardingComplete is set to true after the first-run welcome screen
	// is dismissed. Mirrors CC's projectOnboardingState gate so returning
	// users skip the welcome.
	OnboardingComplete bool `json:"onboardingComplete,omitempty"`
	// MCP project-scope approval gate (mirrors CC's MCPServerApprovalDialog).
	// A server loaded from .mcp.json is allowed to connect only if its name
	// is in EnabledMcpjsonServers OR EnableAllProjectMcpServers is true.
	// Names in DisabledMcpjsonServers are never connected.
	EnabledMcpjsonServers      []string `json:"enabledMcpjsonServers,omitempty"`
	DisabledMcpjsonServers     []string `json:"disabledMcpjsonServers,omitempty"`
	EnableAllProjectMcpServers bool     `json:"enableAllProjectMcpServers,omitempty"`
	// Model is the preferred model name (e.g. "claude-opus-4-8").
	Model string `json:"model,omitempty"`
	// Providers is conduit's named provider registry. Roles reference these
	// keys so main/planning/background/implement can choose independently.
	Providers map[string]ActiveProviderSettings `json:"providers,omitempty"`
	// Roles maps role names such as "default" or "implement" to provider keys.
	Roles map[string]string `json:"roles,omitempty"`
	// OutputStyle is the active output style name, persisted across sessions.
	OutputStyle string `json:"outputStyle,omitempty"`
	// Theme is the active palette name (dark|light|dark-daltonized|
	// light-daltonized|dark-ansi|light-ansi). Matches Claude Code's
	// THEME_NAMES so settings.json values are interchangeable.
	Theme string `json:"theme,omitempty"`
	// UsageStatusEnabled enables the conduit-only footer that fetches and
	// renders Claude plan usage windows.
	UsageStatusEnabled bool `json:"usageStatusEnabled,omitempty"`
	// ThemeOverrides applies per-field color tweaks on top of the named
	// theme. Keys are lowercase Palette field names (e.g. "accent",
	// "success"); values are #RRGGBB hex or ANSI 0-15 codes.
	// conduit-only — Claude Code ignores this field.
	ThemeOverrides map[string]string `json:"themeOverrides,omitempty"`
	// Themes is a map of custom theme name → field map (same shape as
	// themeOverrides). Lets users define entirely new themes selectable
	// via /theme. conduit-only.
	Themes map[string]map[string]string `json:"themes,omitempty"`
}

// Merged is the result of loading and merging all settings layers.
type Merged struct {
	// Allow is the combined allow list (user + project + local).
	Allow []string
	// Deny is the combined deny list.
	Deny []string
	// Ask is the combined ask list.
	Ask []string
	// DefaultMode is the effective default permission mode.
	DefaultMode string
	// Hooks is the merged hooks configuration.
	Hooks HooksSettings
	// Env is the merged environment map.
	Env map[string]string
	// AdditionalDirs is the merged set of additional allowed dirs.
	AdditionalDirs []string
	// Model is the preferred model override from settings (last layer wins).
	Model string
	// Providers is the merged conduit provider registry.
	Providers map[string]ActiveProviderSettings
	// Roles maps role names to provider keys.
	Roles map[string]string
	// ProviderChains maps role names to ordered provider-key slices for
	// automatic failover when a provider returns 429/503/529.
	ProviderChains map[string][]string
	// OutputStyle is the active output style name (last layer wins).
	OutputStyle string
	// Theme is the active palette name (last layer wins).
	Theme string
	// UsageStatusEnabled enables the conduit-only plan usage footer.
	UsageStatusEnabled bool
	// ThemeOverrides is the per-field color override map (last layer wins).
	ThemeOverrides map[string]string
	// Themes is the merged custom theme map (last layer wins per name).
	Themes map[string]map[string]string
	// OnboardingComplete is true once any layer sets it.
	OnboardingComplete bool
	// MCP project-scope approval gate. Last layer wins.
	EnabledMcpjsonServers      []string
	DisabledMcpjsonServers     []string
	EnableAllProjectMcpServers bool

	// CouncilProviders is the list of provider keys that participate in
	// council-mode debates. Empty means council mode is disabled/unconfigured.
	CouncilProviders []string `json:"councilProviders,omitempty"`
	// CouncilMaxRounds is the maximum number of debate rounds before synthesis.
	// Zero means use the default (4).
	CouncilMaxRounds int `json:"councilMaxRounds,omitempty"`
	// CouncilMemberTimeoutSec caps each per-member sub-agent call. Zero → default (30s).
	CouncilMemberTimeoutSec int `json:"councilMemberTimeoutSec,omitempty"`
	// CouncilSynthesizer is the provider key used for synthesis. Empty → parent loop's background model.
	CouncilSynthesizer string `json:"councilSynthesizer,omitempty"`
	// CouncilSynthesizerMaxTokens overrides max_tokens for the synthesis call.
	// Council debates produce long transcripts; the parent loop's MaxTokens can
	// truncate the synthesis. Zero → default (8192).
	CouncilSynthesizerMaxTokens int `json:"councilSynthesizerMaxTokens,omitempty"`
	// CouncilConvergenceThreshold is the minimum Jaccard similarity (0–1) that
	// triggers early agreement. Zero or negative disables similarity-based convergence.
	CouncilConvergenceThreshold float64 `json:"councilConvergenceThreshold,omitempty"`
	// CouncilRoles maps provider keys to role names ("architect", "skeptic", "perf-reviewer").
	CouncilRoles map[string]string `json:"councilRoles,omitempty"`

	// CompactionMaxTriggerTokens is compaction.maxTriggerTokens from
	// conduit.json. nil means unset (use the default cap).
	CompactionMaxTriggerTokens *int `json:"-"`

	// SecretRedactionOff is true when conduit.json sets redactSecrets to
	// false. The zero value keeps redaction on.
	SecretRedactionOff bool `json:"-"`
	// RepetitionGuardOff is true when conduit.json sets ttsr.repetitionGuard
	// to false. The zero value keeps the guard on.
	RepetitionGuardOff bool `json:"-"`
	// WebSearchKeyless is webSearch.keyless from conduit.json, normalized to
	// "auto", "always" or "never". Unset or unrecognised values are "auto".
	WebSearchKeyless string `json:"-"`
}

// EffectiveCompactionTriggerCap returns the cap on the auto-compact trigger
// token count: the configured value, internalmodel.DefaultAutoCompactTriggerCap
// when unset, or 0 (uncapped) when configured as zero or negative.
func (s *Merged) EffectiveCompactionTriggerCap() int {
	if s == nil || s.CompactionMaxTriggerTokens == nil {
		return internalmodel.DefaultAutoCompactTriggerCap
	}
	if *s.CompactionMaxTriggerTokens <= 0 {
		return 0
	}
	return *s.CompactionMaxTriggerTokens
}

// EffectiveCouncilMaxRounds returns the configured max debate rounds, defaulting
// to 4 when CouncilMaxRounds is zero or negative.
func (s *Merged) EffectiveCouncilMaxRounds() int {
	if s.CouncilMaxRounds > 0 {
		return s.CouncilMaxRounds
	}
	return 4
}

// EffectiveCouncilMemberTimeout returns the per-member sub-agent timeout.
// Returns 0 (no timeout) when CouncilMemberTimeoutSec is zero or negative,
// which is the default. Set CouncilMemberTimeoutSec > 0 in conduit.json to
// cap runaway members.
func (s *Merged) EffectiveCouncilMemberTimeout() time.Duration {
	if s.CouncilMemberTimeoutSec > 0 {
		return time.Duration(s.CouncilMemberTimeoutSec) * time.Second
	}
	return 0
}

// EffectiveCouncilSynthesizerMaxTokens returns the synthesis max_tokens budget,
// defaulting to 8192 when unset. Synthesis runs after every debate round and
// produces a single coherent plan; an undersized budget silently truncates
// the response and breaks the plan-approval flow.
func (s *Merged) EffectiveCouncilSynthesizerMaxTokens() int {
	if s.CouncilSynthesizerMaxTokens > 0 {
		return s.CouncilSynthesizerMaxTokens
	}
	return 8192
}

// DiffReviewMidTurnThreshold returns the minimum number of staged pending-edit
// files that triggers a mid-turn diff review pause in acceptEditsLive mode.
// Override via CONDUIT_DIFF_REVIEW_THRESHOLD env var; default is 3.
func DiffReviewMidTurnThreshold() int {
	if v := os.Getenv("CONDUIT_DIFF_REVIEW_THRESHOLD"); v != "" {
		n := 0
		for _, c := range v {
			if c < '0' || c > '9' {
				break
			}
			n = n*10 + int(c-'0')
		}
		if n > 0 {
			return n
		}
	}
	return 3
}

// ActiveProviderSettings stores conduit's provider routing selector.
type ActiveProviderSettings struct {
	Kind          string `json:"kind"`
	Model         string `json:"model,omitempty"`
	Account       string `json:"account,omitempty"`
	Credential    string `json:"credential,omitempty"`
	BaseURL       string `json:"baseURL,omitempty"`
	ContextWindow int    `json:"contextWindow,omitempty"`
	Server        string `json:"server,omitempty"`
	DirectTool    string `json:"directTool,omitempty"`
	ImplementTool string `json:"implementTool,omitempty"`
	// ExtraBody carries vendor/model-specific request fields for
	// openai-compatible providers (e.g. llama.cpp/LM Studio's
	// chat_template_kwargs), merged into every request to this provider as a
	// JSON merge patch: objects merge, other values replace, null deletes.
	ExtraBody map[string]any `json:"extraBody,omitempty"`
	// ReasoningReplay says when an openai-compatible provider gets an
	// assistant turn's reasoning back as reasoning_content: "auto" (the
	// default when empty; local/LAN servers and the model families that
	// require it), "on" (every turn that has reasoning) or "off" (never).
	ReasoningReplay string `json:"reasoningReplay,omitempty"`
	// ToolCallFormat is how an openai-compatible provider carries tool calls:
	// "native" (the default when empty; the `tools` request field), "hermes"
	// or "qwen3" (schemas in the system prompt, <tool_call> blocks parsed from
	// the reply, for servers or models without native tool support), or
	// "auto" (native until the server rejects tools).
	ToolCallFormat string `json:"toolCallFormat,omitempty"`
	// ResponseHeaderTimeout is how many seconds an openai-compatible provider
	// may take to send response headers. 0 or unset: no limit (a local server
	// can spend minutes prefilling); a positive value sets one. Hosted
	// ChatGPT/Codex and Copilot clients always use 5 minutes.
	ResponseHeaderTimeout int `json:"responseHeaderTimeout,omitempty"`
}

const (
	ProviderKindClaudeSubscription = "claude-subscription"
	ProviderKindAnthropicAPI       = "anthropic-api"
	ProviderKindOpenAICompatible   = "openai-compatible"
	ProviderKindMCP                = "mcp"

	RoleDefault    = "default"
	RoleMain       = "main"
	RolePlanning   = "planning"
	RoleBackground = "background"
	RoleImplement  = "implement"
)

// Load reads and merges settings from all layers for the given cwd.
func Load(cwd string) (*Merged, error) {
	_ = ensureConduitConfigImported()
	_ = RepairConduitProviderRegistry()
	merged := loadPaths(settingsFiles(cwd))
	merged.Providers, merged.Roles, _ = CanonicalizeProviderRegistry(merged.Providers, merged.Roles)
	// Overlay conduit-only fields from ConduitConfig — these are not part of
	// the CC-compatible Settings struct so loadPaths never sees them.
	if cfg, err := LoadConduitConfig(); err == nil {
		if len(cfg.CouncilProviders) > 0 {
			merged.CouncilProviders = append([]string(nil), cfg.CouncilProviders...)
		}
		if cfg.CouncilMaxRounds > 0 {
			merged.CouncilMaxRounds = cfg.CouncilMaxRounds
		}
		if cfg.CouncilMemberTimeoutSec > 0 {
			merged.CouncilMemberTimeoutSec = cfg.CouncilMemberTimeoutSec
		}
		if cfg.CouncilSynthesizer != "" {
			merged.CouncilSynthesizer = cfg.CouncilSynthesizer
		}
		if cfg.CouncilSynthesizerMaxTokens > 0 {
			merged.CouncilSynthesizerMaxTokens = cfg.CouncilSynthesizerMaxTokens
		}
		if cfg.CouncilConvergenceThreshold > 0 {
			merged.CouncilConvergenceThreshold = cfg.CouncilConvergenceThreshold
		}
		if len(cfg.CouncilRoles) > 0 {
			merged.CouncilRoles = maps.Clone(cfg.CouncilRoles)
		}
		if len(cfg.ProviderChains) > 0 {
			merged.ProviderChains = maps.Clone(cfg.ProviderChains)
		}
		if cfg.Compaction != nil && cfg.Compaction.MaxTriggerTokens != nil {
			v := *cfg.Compaction.MaxTriggerTokens
			merged.CompactionMaxTriggerTokens = &v
		}
		merged.SecretRedactionOff = cfg.RedactSecrets != nil && !*cfg.RedactSecrets
		merged.WebSearchKeyless = WebSearchKeylessAuto
		if cfg.WebSearch != nil {
			switch m := strings.ToLower(strings.TrimSpace(cfg.WebSearch.Keyless)); m {
			case WebSearchKeylessAlways, WebSearchKeylessNever:
				merged.WebSearchKeyless = m
			}
		}
		merged.RepetitionGuardOff = cfg.TTSR != nil && cfg.TTSR.RepetitionGuard != nil && !*cfg.TTSR.RepetitionGuard
	}
	applyConduitProjectState(merged, cwd)
	return merged, nil
}

func applyConduitProjectState(merged *Merged, cwd string) {
	if merged == nil || cwd == "" {
		return
	}
	state, ok, err := LoadConduitProjectState(cwd)
	if err != nil || !ok {
		return
	}
	merged.EnabledMcpjsonServers = append(merged.EnabledMcpjsonServers, state.EnabledMcpjsonServers...)
	merged.DisabledMcpjsonServers = append(merged.DisabledMcpjsonServers, state.DisabledMcpjsonServers...)
	if state.EnableAllProjectMcpServers {
		merged.EnableAllProjectMcpServers = true
	}
}

// loadPaths merges settings from an explicit list of file paths (testable).
func loadPaths(paths []string) *Merged {
	merged := &Merged{
		DefaultMode: "default",
		Env:         make(map[string]string),
		Providers:   make(map[string]ActiveProviderSettings),
		Roles:       make(map[string]string),
	}
	for _, path := range paths {
		s, err := readFile(path)
		if err != nil {
			continue // missing or invalid file is skipped
		}
		// Tag every HookMatcher with the file it was loaded from so callers
		// can distinguish project-local hooks from user-global ones.
		tagHookSource(&s.Hooks, path)
		merged.Allow = append(merged.Allow, s.Permissions.Allow...)
		merged.Deny = append(merged.Deny, s.Permissions.Deny...)
		merged.Ask = append(merged.Ask, s.Permissions.Ask...)
		if s.Permissions.DefaultMode != "" {
			merged.DefaultMode = s.Permissions.DefaultMode
		}
		merged.AdditionalDirs = append(merged.AdditionalDirs, s.Permissions.AdditionalDirs...)
		mergeHooks(&merged.Hooks, &s.Hooks)
		maps.Copy(merged.Env, s.Env)
		if s.Model != "" {
			merged.Model = s.Model
		}
		maps.Copy(merged.Providers, s.Providers)
		maps.Copy(merged.Roles, s.Roles)
		if s.OutputStyle != "" {
			merged.OutputStyle = s.OutputStyle
		}
		if s.Theme != "" {
			merged.Theme = s.Theme
		}
		if s.UsageStatusEnabled {
			merged.UsageStatusEnabled = true
		}
		if len(s.ThemeOverrides) > 0 {
			if merged.ThemeOverrides == nil {
				merged.ThemeOverrides = map[string]string{}
			}
			maps.Copy(merged.ThemeOverrides, s.ThemeOverrides)
		}
		if len(s.Themes) > 0 {
			if merged.Themes == nil {
				merged.Themes = map[string]map[string]string{}
			}
			maps.Copy(merged.Themes, s.Themes)
		}
		if s.OnboardingComplete {
			merged.OnboardingComplete = true
		}
		merged.EnabledMcpjsonServers = append(merged.EnabledMcpjsonServers, s.EnabledMcpjsonServers...)
		merged.DisabledMcpjsonServers = append(merged.DisabledMcpjsonServers, s.DisabledMcpjsonServers...)
		if s.EnableAllProjectMcpServers {
			merged.EnableAllProjectMcpServers = true
		}
	}
	return merged
}

func settingsFiles(cwd string) []string {
	paths := []string{}
	conduitExists := false
	if _, err := os.Stat(ConduitSettingsPath()); err == nil {
		conduitExists = true
	}
	if !conduitExists {
		paths = append(paths, filepath.Join(claudeDir(), "settings.json"))
	} else {
		paths = append(paths, ConduitSettingsPath())
	}
	if cwd != "" {
		paths = append(paths,
			filepath.Join(cwd, ".claude", "settings.json"),
			filepath.Join(cwd, ".claude", "settings.local.json"),
			filepath.Join(cwd, ".conduit", "settings.json"),
			ProjectLocalSettingsPath(cwd),
		)
	}
	return paths
}

func readFile(path string) (*Settings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func mergeHooks(dst, src *HooksSettings) {
	dst.PreToolUse = append(dst.PreToolUse, src.PreToolUse...)
	dst.PostToolUse = append(dst.PostToolUse, src.PostToolUse...)
	dst.SessionStart = append(dst.SessionStart, src.SessionStart...)
	dst.Stop = append(dst.Stop, src.Stop...)
	dst.TeammateIdle = append(dst.TeammateIdle, src.TeammateIdle...)
	dst.TaskCreated = append(dst.TaskCreated, src.TaskCreated...)
	dst.TaskCompleted = append(dst.TaskCompleted, src.TaskCompleted...)
	dst.UserPromptSubmit = append(dst.UserPromptSubmit, src.UserPromptSubmit...)
}

// tagHookSource sets SourceFile on every HookMatcher in h to path.
func tagHookSource(h *HooksSettings, path string) {
	tag := func(ms []HookMatcher) []HookMatcher {
		for i := range ms {
			ms[i].SourceFile = path
		}
		return ms
	}
	h.PreToolUse = tag(h.PreToolUse)
	h.PostToolUse = tag(h.PostToolUse)
	h.SessionStart = tag(h.SessionStart)
	h.Stop = tag(h.Stop)
	h.TeammateIdle = tag(h.TeammateIdle)
	h.TaskCreated = tag(h.TaskCreated)
	h.TaskCompleted = tag(h.TaskCompleted)
	h.UserPromptSubmit = tag(h.UserPromptSubmit)
}

// FilterUntrustedHooks returns a copy of h with project-local matchers removed
// when trusted is false. When trusted is true or h is nil it returns h unchanged.
// "Project-local" means the hook came from <cwd>/.claude/settings.json or
// <cwd>/.claude/settings.local.json — not from user-global config.
func FilterUntrustedHooks(h *HooksSettings, cwd string, trusted bool) *HooksSettings {
	if trusted || h == nil || cwd == "" {
		return h
	}
	filter := func(ms []HookMatcher) []HookMatcher {
		out := make([]HookMatcher, 0, len(ms))
		for _, m := range ms {
			if !m.IsProjectLocal(cwd) {
				out = append(out, m)
			}
		}
		return out
	}
	return &HooksSettings{
		PreToolUse:       filter(h.PreToolUse),
		PostToolUse:      filter(h.PostToolUse),
		SessionStart:     filter(h.SessionStart),
		Stop:             filter(h.Stop),
		TeammateIdle:     filter(h.TeammateIdle),
		TaskCreated:      filter(h.TaskCreated),
		TaskCompleted:    filter(h.TaskCompleted),
		UserPromptSubmit: filter(h.UserPromptSubmit),
	}
}
