package app

import (
	"runtime"
	"strings"
	"sync"

	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/kernel"
	"github.com/icehunter/conduit/internal/lsp"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/pendingedits"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/provider/codex"
	"github.com/icehunter/conduit/internal/readtracker"
	"github.com/icehunter/conduit/internal/secure"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/team"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/tools/askusertool"
	"github.com/icehunter/conduit/internal/tools/astgreptool"
	"github.com/icehunter/conduit/internal/tools/automodetool"
	"github.com/icehunter/conduit/internal/tools/bashtool"
	"github.com/icehunter/conduit/internal/tools/ccrtool"
	"github.com/icehunter/conduit/internal/tools/configtool"
	"github.com/icehunter/conduit/internal/tools/decisiontool"
	"github.com/icehunter/conduit/internal/tools/fileedittool"
	"github.com/icehunter/conduit/internal/tools/filereadtool"
	"github.com/icehunter/conduit/internal/tools/filewritetool"
	"github.com/icehunter/conduit/internal/tools/globtool"
	"github.com/icehunter/conduit/internal/tools/greptool"
	"github.com/icehunter/conduit/internal/tools/hashedittool"
	"github.com/icehunter/conduit/internal/tools/localhelpertool"
	"github.com/icehunter/conduit/internal/tools/localimplementtool"
	lsptool "github.com/icehunter/conduit/internal/tools/lsp"
	"github.com/icehunter/conduit/internal/tools/mcpauthtool"
	"github.com/icehunter/conduit/internal/tools/mcpresourcetool"
	"github.com/icehunter/conduit/internal/tools/mcptool"
	"github.com/icehunter/conduit/internal/tools/notebookedittool"
	"github.com/icehunter/conduit/internal/tools/planmodetool"
	"github.com/icehunter/conduit/internal/tools/repltool"
	"github.com/icehunter/conduit/internal/tools/sendmessagetool"
	"github.com/icehunter/conduit/internal/tools/sessionsearchtool"
	"github.com/icehunter/conduit/internal/tools/sleeptool"
	"github.com/icehunter/conduit/internal/tools/syntheticoutputtool"
	"github.com/icehunter/conduit/internal/tools/tasktool"
	"github.com/icehunter/conduit/internal/tools/todowritetool"
	"github.com/icehunter/conduit/internal/tools/toolsearchtool"
	"github.com/icehunter/conduit/internal/tools/webfetchtool"
	"github.com/icehunter/conduit/internal/tools/websearchtool"
	"github.com/icehunter/conduit/internal/tools/winshelltool"
	"github.com/icehunter/conduit/internal/tools/worktreetool"
	braveprovider "github.com/icehunter/conduit/internal/websearch/brave"
	codexsearch "github.com/icehunter/conduit/internal/websearch/codex"
	duckduckgosearch "github.com/icehunter/conduit/internal/websearch/duckduckgo"
	mojeeksearch "github.com/icehunter/conduit/internal/websearch/mojeek"
	parallelsearch "github.com/icehunter/conduit/internal/websearch/parallel"
)

// BuildAgentEntries converts loaded plugin agents into AgentEntry values for
// the system prompt agent types listing (the "Available agent types" reminder).
func BuildAgentEntries(ps []*plugins.Plugin) []agent.AgentEntry {
	var entries []agent.AgentEntry
	for _, p := range ps {
		for _, a := range p.Agents {
			entries = append(entries, agent.AgentEntry{
				Name:        a.QualifiedName,
				Description: a.Description,
			})
		}
	}
	return entries
}

// BuildSkillEntries converts loaded plugin commands, plugin skills, and
// bundled skills into SkillEntry values for the system prompt skill listing.
func BuildSkillEntries(ps []*plugins.Plugin) []agent.SkillEntry {
	loader := plugins.NewSkillLoader(ps, "")
	var entries []agent.SkillEntry

	// Bundled built-in skills first.
	for _, cmd := range loader.BundledCommands() {
		entries = append(entries, agent.SkillEntry{
			Name:        "/" + cmd.QualifiedName,
			Description: cmd.Description,
		})
	}
	// Plugin slash commands.
	for _, p := range ps {
		for _, cmd := range p.Commands {
			entries = append(entries, agent.SkillEntry{
				Name:        cmd.QualifiedName,
				Description: cmd.Description,
			})
		}
	}
	// Plugin skills (SKILL.md).
	for _, sk := range loader.PluginSkills() {
		entries = append(entries, agent.SkillEntry{
			Name:        sk.QualifiedName,
			Description: sk.Description,
		})
	}
	return entries
}

// RegistryOpts holds optional callbacks wired after the TUI program starts.
// These are nil in --print mode (no interactive terminal).
type RegistryOpts struct {
	EnterPlan     *planmodetool.EnterPlanMode
	ExitPlan      *planmodetool.ExitPlanMode
	AskUser       *askusertool.AskUserQuestion
	Synthetic     *syntheticoutputtool.SyntheticOutput
	EnterAutoMode *automodetool.EnterAutoMode
	ExitAutoMode  *automodetool.ExitAutoMode
	EnterWorktree *worktreetool.EnterWorktree
	ExitWorktree  *worktreetool.ExitWorktree

	// Stager, when non-nil, routes file-write tool output through the
	// diff-first review gate instead of writing to disk immediately.
	// Set only for the foreground agent loop in acceptEdits mode.
	// Sub-agent registries leave this nil so they write directly.
	Stager pendingedits.Stager

	// SessionEnv is passed directly to bashtool.New so that subprocess
	// environment injection is stored on the Tool instance rather than a
	// package-level global.
	SessionEnv map[string]string

	// KernelManager and SessionID enable persistent Python/Node kernels for
	// the REPL tool. When non-nil, Python and Node executions share a
	// long-lived interpreter process per (SessionID, lang) pair so that
	// variables persist across calls. Bash always uses subprocess-per-call.
	KernelManager *kernel.Manager
	SessionID     string
}

// BuildRegistry builds the tool registry, including MCP server tools. With a
// manager it also installs the manager's change hook, so the registry's MCP
// tools track servers as they connect and disconnect (see SyncMCPTools).
func BuildRegistry(client *api.Client, mcpManager *mcp.Manager, lspManager *lsp.Manager, rOpts *RegistryOpts, implementProvider func() *settings.ActiveProviderSettings) *tool.Registry {
	reg := tool.NewRegistry()
	var sessionEnv map[string]string
	var stager pendingedits.Stager
	var kernelMgr *kernel.Manager
	var sessionID string
	if rOpts != nil {
		sessionEnv = rOpts.SessionEnv
		stager = rOpts.Stager
		kernelMgr = rOpts.KernelManager
		sessionID = rOpts.SessionID
	}
	// On Windows, register the PowerShell Shell tool; on Unix/macOS use Bash.
	if runtime.GOOS == "windows" {
		reg.Register(winshelltool.New(sessionEnv))
	} else {
		reg.Register(bashtool.New(sessionEnv))
	}
	// One read tracker per registry (so per session) backs the
	// read-before-write guard. Sub-agent registries are views over this one
	// and share these tool instances, hence the tracker.
	reads := readtracker.New()
	reg.Register(fileedittool.NewWithStager(stager).WithTracker(reads))
	reg.Register(hashedittool.NewWithStager(stager).WithTracker(reads))
	reg.Register(filereadtool.New().WithTracker(reads))
	reg.Register(astgreptool.NewWithStager(stager).WithTracker(reads))
	reg.Register(filewritetool.NewWithStager(stager).WithTracker(reads))
	reg.Register(globtool.New())
	reg.Register(ccrtool.New())
	reg.Register(greptool.New())
	reg.Register(notebookedittool.New().WithTracker(reads))
	if kernelMgr != nil {
		reg.Register(repltool.NewWithKernelManager(kernelMgr, sessionID))
	} else {
		reg.Register(repltool.New())
	}
	reg.Register(sleeptool.New())
	reg.Register(tasktool.NewCreate())
	reg.Register(tasktool.NewGet())
	reg.Register(tasktool.NewList())
	reg.Register(tasktool.NewUpdate())
	reg.Register(tasktool.NewOutput())
	reg.Register(tasktool.NewStop())
	reg.Register(todowritetool.New())
	reg.Register(decisiontool.New())
	reg.Register(toolsearchtool.New(reg))
	reg.Register(webfetchtool.New())
	// Order: Brave (key), Anthropic-native (Anthropic-capable clients only),
	// the Codex subscription, then the keyless tier, which webSearch.keyless
	// gates (auto: only when nothing else is configured).
	searchStore := secure.NewDefault()
	reg.Register(websearchtool.NewWithProviders(client,
		braveprovider.New(searchStore),
	).WithFallbackProviders(
		codexsearch.New(searchStore).WithModel(codexSearchModel),
	).WithKeylessProviders(webSearchKeylessMode,
		parallelsearch.New(),
		duckduckgosearch.New(),
		mojeeksearch.New(),
	))
	reg.Register(sessionsearchtool.New(sessionsearchtool.WithCurrentSession(func() string { return sessionID })))
	reg.Register(lsptool.New(lspManager))
	reg.Register(&configtool.ConfigTool{})
	reg.Register(&mcpresourcetool.ListMcpResources{Manager: mcpManager})
	reg.Register(&mcpresourcetool.ReadMcpResource{Manager: mcpManager})
	// Agent Teams: always register SendMessage so the toggle works live.
	// Execute is gated on team.IsActive() so it's a no-op when teams are off.
	// Teammates get a per-sender instance injected via SpawnTeammate ExtraTools.
	reg.Register(sendmessagetool.New(team.Default))
	// Interactive tools; callbacks are wired by the TUI after prog.Start().
	if rOpts != nil && rOpts.EnterWorktree != nil {
		reg.Register(rOpts.EnterWorktree)
		reg.Register(rOpts.ExitWorktree)
	}
	if rOpts != nil {
		reg.Register(rOpts.EnterPlan)
		reg.Register(rOpts.ExitPlan)
		reg.Register(rOpts.AskUser)
		reg.Register(rOpts.Synthetic)
		if rOpts.EnterAutoMode != nil {
			reg.Register(rOpts.EnterAutoMode)
		}
		if rOpts.ExitAutoMode != nil {
			reg.Register(rOpts.ExitAutoMode)
		}
	}
	if mcpManager != nil {
		followMCPTools(reg, mcpManager, implementProvider)
	}
	return reg
}

// followMCPTools registers reg's MCP-derived tools and keeps them current:
// servers connect in the background and can be reconnected, authenticated,
// added or removed mid-session, so the manager's change hook re-syncs on each
// change. The hook goes in before the first sync so no change can land
// between them unseen. This makes reg the registry that follows mgr; it
// replaces any change hook mgr already had.
func followMCPTools(reg *tool.Registry, mgr *mcp.Manager, implementProvider func() *settings.ActiveProviderSettings) {
	mgr.SetOnChange(func() { SyncMCPTools(reg, mgr, implementProvider) })
	SyncMCPTools(reg, mgr, implementProvider)
}

// mcpSyncMu serializes SyncMCPTools. Each call reads the manager while
// holding it, so when change notifications overlap, the last call to run
// installs the newest state rather than an older snapshot.
var mcpSyncMu sync.Mutex

// SyncMCPTools brings reg's MCP-derived tools in line with mgr's current
// state: a tool for every tool a connected server lists (dropping those of
// servers that are disabled, removed, reconnecting or failed), an
// authenticate pseudo-tool for each remote server awaiting OAuth, and
// LocalImplement / LocalHelper while a connected server provides them. Every
// other tool in reg is left alone. Safe to call concurrently.
//
// MCP tools are deferred behind ToolSearch, so their churn leaves the request's
// tool list unchanged; the authenticate and Local* tools are always sent, and
// adding or removing one changes the cached prompt prefix.
func SyncMCPTools(reg *tool.Registry, mgr *mcp.Manager, implementProvider func() *settings.ActiveProviderSettings) {
	if reg == nil || mgr == nil {
		return
	}
	mcpSyncMu.Lock()
	defer mcpSyncMu.Unlock()

	var want []tool.Tool
	// The model triggers OAuth itself through these. A server's real tools
	// are added after them, so they win any name clash.
	for _, srv := range mgr.Servers() {
		if srv.Status == mcp.StatusNeedsAuth && srv.Config.URL != "" {
			want = append(want, mcpauthtool.New(mgr, srv.Name, srv.Config.URL))
		}
	}
	for _, nt := range mgr.AllTools() {
		want = append(want, mcptool.New(mgr, nt))
	}
	resolveImplement := func() (localimplementtool.Config, bool) {
		return localimplementtool.ResolveConfig(mgr, resolveImplementProvider(implementProvider))
	}
	if _, ok := resolveImplement(); ok {
		want = append(want, localimplementtool.NewDynamic(mgr, resolveImplement))
	}
	resolveHelper := func() (localhelpertool.Config, bool) { return localhelpertool.ResolveConfig(mgr) }
	if _, ok := resolveHelper(); ok {
		want = append(want, localhelpertool.NewDynamic(mgr, resolveHelper))
	}
	reg.Reconcile(isMCPDerived, want)
}

// isMCPDerived reports whether t is one of the tools SyncMCPTools manages.
func isMCPDerived(t tool.Tool) bool {
	switch t.(type) {
	case *mcptool.MCPTool, *localimplementtool.Tool, *localhelpertool.Tool:
		return true
	}
	return mcpauthtool.IsAuthTool(t)
}

func resolveImplementProvider(fn func() *settings.ActiveProviderSettings) *settings.ActiveProviderSettings {
	if fn == nil {
		return nil
	}
	return fn()
}

func ClaudeRoleModel(cwd, role, fallback string) string {
	latest, err := settings.Load(cwd)
	if err != nil {
		return fallback
	}
	provider, ok := latest.ProviderForRole(role)
	if !ok || provider == nil || provider.Kind == "mcp" || provider.Model == "" {
		return fallback
	}
	return provider.Model
}

// webSearchKeylessMode reads webSearch.keyless from conduit.json on each
// search so a settings change applies without a restart.
func webSearchKeylessMode() string {
	m, err := settings.Load("")
	if err != nil || m == nil {
		return settings.WebSearchKeylessAuto
	}
	return m.WebSearchKeyless
}

// codexSearchModel returns the model of the default-role provider when that
// provider is a Codex one, else "" so the search provider uses its default.
func codexSearchModel() string {
	m, err := settings.Load("")
	if err != nil || m == nil {
		return ""
	}
	p, ok := m.Providers[m.Roles["default"]]
	if !ok || !(strings.HasPrefix(p.Credential, codex.ProviderID) || strings.Contains(p.BaseURL, "chatgpt.com/backend-api/codex")) {
		return ""
	}
	return p.Model
}
