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
	PreToolUse   []HookMatcher `json:"PreToolUse"`
	PostToolUse  []HookMatcher `json:"PostToolUse"`
	SessionStart []HookMatcher `json:"SessionStart"`
	Stop         []HookMatcher `json:"Stop"`
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
	// Model is the preferred model name (e.g. "claude-opus-4-7").
	Model string `json:"model,omitempty"`
	// ActiveProvider is conduit's provider routing selector. It is intentionally
	// richer than "model" so API-key, OAuth/subscription, and MCP-backed
	// providers can carry their own auth and transport fields.
	ActiveProvider *ActiveProviderSettings `json:"activeProvider,omitempty"`
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
	// ActiveProvider is the effective conduit provider routing selector.
	ActiveProvider *ActiveProviderSettings
	// Providers is the merged conduit provider registry.
	Providers map[string]ActiveProviderSettings
	// Roles maps role names to provider keys.
	Roles map[string]string
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
	// CouncilConvergenceThreshold is the minimum Jaccard similarity (0–1) that
	// triggers early agreement. Zero or negative disables similarity-based convergence.
	CouncilConvergenceThreshold float64 `json:"councilConvergenceThreshold,omitempty"`
	// CouncilRoles maps provider keys to role names ("architect", "skeptic", "perf-reviewer").
	CouncilRoles map[string]string `json:"councilRoles,omitempty"`
}

// EffectiveCouncilMaxRounds returns the configured max debate rounds, defaulting
// to 4 when CouncilMaxRounds is zero or negative.
func (s *Merged) EffectiveCouncilMaxRounds() int {
	if s.CouncilMaxRounds > 0 {
		return s.CouncilMaxRounds
	}
	return 4
}

// EffectiveCouncilMemberTimeout returns the per-member sub-agent timeout,
// defaulting to 30 seconds when CouncilMemberTimeoutSec is zero or negative.
func (s *Merged) EffectiveCouncilMemberTimeout() time.Duration {
	if s.CouncilMemberTimeoutSec > 0 {
		return time.Duration(s.CouncilMemberTimeoutSec) * time.Second
	}
	return 30 * time.Second
}

// ActiveProviderSettings stores conduit's provider routing selector.
type ActiveProviderSettings struct {
	Kind          string `json:"kind"`
	Model         string `json:"model,omitempty"`
	Account       string `json:"account,omitempty"`
	Credential    string `json:"credential,omitempty"`
	BaseURL       string `json:"baseURL,omitempty"`
	Server        string `json:"server,omitempty"`
	DirectTool    string `json:"directTool,omitempty"`
	ImplementTool string `json:"implementTool,omitempty"`
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
		if cfg.CouncilConvergenceThreshold > 0 {
			merged.CouncilConvergenceThreshold = cfg.CouncilConvergenceThreshold
		}
		if len(cfg.CouncilRoles) > 0 {
			merged.CouncilRoles = maps.Clone(cfg.CouncilRoles)
		}
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
		for k, v := range s.Env {
			merged.Env[k] = v
		}
		if s.Model != "" {
			merged.Model = s.Model
		}
		if s.ActiveProvider != nil {
			cp := *s.ActiveProvider
			merged.ActiveProvider = &cp
		}
		for k, v := range s.Providers {
			merged.Providers[k] = v
		}
		for k, v := range s.Roles {
			merged.Roles[k] = v
		}
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
			for k, v := range s.ThemeOverrides {
				merged.ThemeOverrides[k] = v
			}
		}
		if len(s.Themes) > 0 {
			if merged.Themes == nil {
				merged.Themes = map[string]map[string]string{}
			}
			for k, v := range s.Themes {
				merged.Themes[k] = v
			}
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
		PreToolUse:   filter(h.PreToolUse),
		PostToolUse:  filter(h.PostToolUse),
		SessionStart: filter(h.SessionStart),
		Stop:         filter(h.Stop),
	}
}
