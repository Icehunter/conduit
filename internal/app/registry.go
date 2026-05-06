package app

import (
	"github.com/icehunter/conduit/internal/agent"
	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/lsp"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/tools/askusertool"
	"github.com/icehunter/conduit/internal/tools/bashtool"
	"github.com/icehunter/conduit/internal/tools/configtool"
	"github.com/icehunter/conduit/internal/tools/fileedittool"
	"github.com/icehunter/conduit/internal/tools/filereadtool"
	"github.com/icehunter/conduit/internal/tools/filewritetool"
	"github.com/icehunter/conduit/internal/tools/globtool"
	"github.com/icehunter/conduit/internal/tools/greptool"
	"github.com/icehunter/conduit/internal/tools/localimplementtool"
	lsptool "github.com/icehunter/conduit/internal/tools/lsp"
	"github.com/icehunter/conduit/internal/tools/mcpauthtool"
	"github.com/icehunter/conduit/internal/tools/mcpresourcetool"
	"github.com/icehunter/conduit/internal/tools/mcptool"
	"github.com/icehunter/conduit/internal/tools/notebookedittool"
	"github.com/icehunter/conduit/internal/tools/planmodetool"
	"github.com/icehunter/conduit/internal/tools/repltool"
	"github.com/icehunter/conduit/internal/tools/sleeptool"
	"github.com/icehunter/conduit/internal/tools/syntheticoutputtool"
	"github.com/icehunter/conduit/internal/tools/tasktool"
	"github.com/icehunter/conduit/internal/tools/todowritetool"
	"github.com/icehunter/conduit/internal/tools/toolsearchtool"
	"github.com/icehunter/conduit/internal/tools/webfetchtool"
	"github.com/icehunter/conduit/internal/tools/websearchtool"
	"github.com/icehunter/conduit/internal/tools/worktreetool"
)

// BuildSkillEntries converts loaded plugin commands + bundled skills into
// SkillEntry values for the system prompt skill listing.
func BuildSkillEntries(ps []*plugins.Plugin) []agent.SkillEntry {
	var entries []agent.SkillEntry
	// Bundled built-in skills first.
	loader := plugins.NewSkillLoader(ps)
	for _, cmd := range loader.BundledCommands() {
		entries = append(entries, agent.SkillEntry{
			Name:        "/" + cmd.QualifiedName,
			Description: cmd.Description,
		})
	}
	// Plugin commands.
	for _, p := range ps {
		for _, cmd := range p.Commands {
			entries = append(entries, agent.SkillEntry{
				Name:        cmd.QualifiedName,
				Description: cmd.Description,
			})
		}
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
	EnterWorktree *worktreetool.EnterWorktree
	ExitWorktree  *worktreetool.ExitWorktree

	// SessionEnv is passed directly to bashtool.New so that subprocess
	// environment injection is stored on the Tool instance rather than a
	// package-level global.
	SessionEnv map[string]string
}

// BuildRegistry builds the tool registry, including MCP server tools.
func BuildRegistry(client *api.Client, mcpManager *mcp.Manager, lspManager *lsp.Manager, rOpts *RegistryOpts, implementProvider func() *settings.ActiveProviderSettings) *tool.Registry {
	reg := tool.NewRegistry()
	var sessionEnv map[string]string
	if rOpts != nil {
		sessionEnv = rOpts.SessionEnv
	}
	reg.Register(bashtool.New(sessionEnv))
	reg.Register(fileedittool.New())
	reg.Register(filereadtool.New())
	reg.Register(filewritetool.New())
	reg.Register(globtool.New())
	reg.Register(greptool.New())
	reg.Register(notebookedittool.New())
	reg.Register(repltool.New())
	reg.Register(sleeptool.New())
	reg.Register(tasktool.NewCreate())
	reg.Register(tasktool.NewGet())
	reg.Register(tasktool.NewList())
	reg.Register(tasktool.NewUpdate())
	reg.Register(tasktool.NewOutput())
	reg.Register(tasktool.NewStop())
	reg.Register(todowritetool.New())
	reg.Register(toolsearchtool.New(reg))
	reg.Register(webfetchtool.New())
	reg.Register(websearchtool.New(client))
	reg.Register(lsptool.New(lspManager))
	reg.Register(&configtool.ConfigTool{})
	reg.Register(&mcpresourcetool.ListMcpResources{Manager: mcpManager})
	reg.Register(&mcpresourcetool.ReadMcpResource{Manager: mcpManager})
	if _, ok := localimplementtool.ResolveConfig(mcpManager, resolveImplementProvider(implementProvider)); ok {
		reg.Register(localimplementtool.NewDynamic(mcpManager, func() (localimplementtool.Config, bool) {
			return localimplementtool.ResolveConfig(mcpManager, resolveImplementProvider(implementProvider))
		}))
	}
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
	}
	// Register MCP server tools (if any servers are configured).
	if mcpManager != nil {
		mcptool.RegisterAll(reg, mcpManager)
		// For each HTTP/SSE server in the StatusNeedsAuth state, register
		// the per-server pseudo-tool so the model can trigger OAuth itself.
		urls := make(map[string]string)
		for _, srv := range mcpManager.Servers() {
			if srv.Status == mcp.StatusNeedsAuth && srv.Config.URL != "" {
				urls[srv.Name] = srv.Config.URL
			}
		}
		mcpauthtool.RegisterPending(reg, mcpManager, urls)
	}
	return reg
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
