package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/icehunter/conduit/internal/planusage"
	"github.com/icehunter/conduit/internal/settings"
)

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

// coordTick schedules the next coordinator tick — only resubscribes when
// there's still at least one in_progress task to display.
func coordTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return coordTickMsg{} })
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
