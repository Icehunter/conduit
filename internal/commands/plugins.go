package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/skills"
)

// PluginPanelInstalledEntry is one installed plugin entry for the panel.
type PluginPanelInstalledEntry struct {
	ID          string `json:"id"` // "name@marketplace"
	Name        string `json:"name"`
	Marketplace string `json:"marketplace"`
	Version     string `json:"version"`
	Scope       string `json:"scope"`
	Enabled     bool   `json:"enabled"`
	InstallPath string `json:"installPath"`
}

// PluginPanelMarketplaceRow is one marketplace row for the panel.
type PluginPanelMarketplaceRow struct {
	Name        string `json:"name"`
	Source      string `json:"source"` // human-readable source string
	LastUpdated string `json:"lastUpdated"`
	PluginCount int    `json:"pluginCount"` // from marketplace.json, 0 if unavailable
}

// PluginPanelData is the JSON payload sent via "plugin-panel" result.
type PluginPanelData struct {
	Installed    []PluginPanelInstalledEntry `json:"installed"`
	Marketplaces []PluginPanelMarketplaceRow `json:"marketplaces"`
	Errors       []string                    `json:"errors"`
}

// RegisterPluginCommands registers slash commands for all loaded plugins.
func RegisterPluginCommands(r *Registry, ps []*plugins.Plugin) {
	for _, p := range ps {
		for _, cmd := range p.Commands {
			qualifiedName := cmd.QualifiedName
			description := cmd.Description
			body := cmd.Body
			if description == "" {
				description = qualifiedName
			}
			r.Register(Command{
				Name:        qualifiedName,
				Description: description,
				Handler: func(args string) Result {
					text := body
					if args != "" {
						text = text + "\n\nArguments: " + args
					}
					return Result{Type: "prompt", Text: text}
				},
			})
		}
	}
}

// RegisterBundledSkillCommands registers slash commands for built-in skills
// (/simplify, /remember, etc.). These run the skill body as a user prompt,
// same flow as plugin slash commands.
func RegisterBundledSkillCommands(r *Registry) {
	for _, cmd := range skills.Bundled() {
		name := cmd.QualifiedName
		desc := cmd.Description
		body := cmd.Body
		if desc == "" {
			desc = name
		}
		r.Register(Command{
			Name:        name,
			Description: desc,
			Handler: func(args string) Result {
				text := body
				if args != "" {
					text = text + "\n\nArguments: " + args
				}
				return Result{Type: "prompt", Text: text}
			},
		})
	}
}

// RegisterPluginBrowserCommand registers /plugin and its subcommands.
func RegisterPluginBrowserCommand(r *Registry, ps []*plugins.Plugin) {
	r.Register(Command{
		Name:        "plugin",
		Description: "Browse, install, and manage plugins",
		Handler: func(args string) Result {
			args = strings.TrimSpace(args)
			if args == "" {
				return pluginDialogResult(ps)
			}
			parts := strings.Fields(args)
			sub := parts[0]
			rest := strings.TrimSpace(strings.TrimPrefix(args, sub))
			switch sub {
			case "install", "i":
				return pluginInstall(rest)
			case "uninstall", "remove", "rm":
				return pluginUninstall(rest)
			case "list", "ls":
				return pluginList(ps)
			case "marketplace":
				return pluginMarketplace(rest)
			default:
				// Treat as plugin name — show its commands.
				return pluginDetail(ps, sub)
			}
		},
	})
}

// pluginDialogResult encodes all plugin state for the interactive panel.
func pluginDialogResult(ps []*plugins.Plugin) Result {
	data := buildPluginPanelData(ps)
	b, err := json.Marshal(data)
	if err != nil {
		return Result{Type: "error", Text: fmt.Sprintf("plugin panel: %v", err)}
	}
	return Result{Type: "plugin-panel", Text: string(b)}
}

func buildPluginPanelData(_ []*plugins.Plugin) PluginPanelData {
	var data PluginPanelData

	// Load enabled state from user settings.
	enabledMap := loadEnabledPlugins()

	// Installed plugins.
	installed, err := plugins.LoadInstalledPlugins()
	if err != nil {
		data.Errors = append(data.Errors, fmt.Sprintf("load installed: %v", err))
	} else {
		for id, entries := range installed.Plugins {
			name, marketplace := splitPluginID(id)
			for _, e := range entries {
				enabled := enabledMap[id]
				data.Installed = append(data.Installed, PluginPanelInstalledEntry{
					ID:          id,
					Name:        name,
					Marketplace: marketplace,
					Version:     e.Version,
					Scope:       e.Scope,
					Enabled:     enabled,
					InstallPath: e.InstallPath,
				})
				break // one entry per plugin (first/user scope)
			}
		}
		sort.Slice(data.Installed, func(i, j int) bool {
			return data.Installed[i].ID < data.Installed[j].ID
		})
	}

	// Marketplaces.
	known, err := plugins.LoadKnownMarketplaces()
	if err != nil {
		data.Errors = append(data.Errors, fmt.Sprintf("load marketplaces: %v", err))
	} else {
		names := make([]string, 0, len(known))
		for n := range known {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			e := known[n]
			src := e.Source.Repo
			if src == "" {
				src = e.Source.URL
			}
			if src == "" {
				src = e.Source.Path
			}

			// Count plugins if marketplace.json is available.
			pluginCount := 0
			if manifest, err := plugins.LoadMarketplaceManifest(n); err == nil {
				pluginCount = len(manifest.Plugins)
			}

			data.Marketplaces = append(data.Marketplaces, PluginPanelMarketplaceRow{
				Name:        n,
				Source:      src,
				LastUpdated: e.LastUpdated,
				PluginCount: pluginCount,
			})
		}
	}

	return data
}

// loadEnabledPlugins reads Conduit's settings file and returns the enabledPlugins map.
func loadEnabledPlugins() map[string]bool {
	cfg, err := settings.LoadConduitConfig()
	if err != nil || cfg.EnabledPlugins == nil {
		return map[string]bool{}
	}
	return cfg.EnabledPlugins
}

func splitPluginID(id string) (name, marketplace string) {
	at := strings.LastIndex(id, "@")
	if at < 0 {
		return id, "claude-plugins-official"
	}
	return id[:at], id[at+1:]
}

func pluginDetail(ps []*plugins.Plugin, name string) Result {
	for _, p := range ps {
		if strings.EqualFold(p.Manifest.Name, name) {
			var sb strings.Builder
			fmt.Fprintf(&sb, "Plugin: %s", p.Manifest.Name)
			if p.Manifest.Version != "" {
				fmt.Fprintf(&sb, " v%s", p.Manifest.Version)
			}
			sb.WriteByte('\n')
			if p.Manifest.Description != "" {
				sb.WriteString(p.Manifest.Description + "\n")
			}
			fmt.Fprintf(&sb, "\nCommands (%d):\n", len(p.Commands))
			for _, cmd := range p.Commands {
				desc := cmd.Description
				if len([]rune(desc)) > 60 {
					desc = string([]rune(desc)[:59]) + "…"
				}
				fmt.Fprintf(&sb, "  /%s\n    %s\n", cmd.QualifiedName, desc)
			}
			return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
		}
	}
	return Result{Type: "error", Text: fmt.Sprintf("Plugin %q not found. Use /plugin to browse installed plugins.", name)}
}

func pluginInstall(args string) Result {
	if args == "" {
		return Result{Type: "error", Text: "Usage: /plugin install <name>[@marketplace]"}
	}
	parts := strings.Fields(args)
	spec := parts[0]

	cwd, _ := os.Getwd()
	entry, err := plugins.Install(context.Background(), spec, "user", cwd)
	if err != nil {
		return Result{Type: "error", Text: fmt.Sprintf("Install failed: %v", err)}
	}
	return Result{Type: "text", Text: fmt.Sprintf("Installed %s v%s\nPath: %s\n\nRestart conduit to load the new plugin commands.", spec, entry.Version, entry.InstallPath)}
}

func pluginUninstall(args string) Result {
	if args == "" {
		return Result{Type: "error", Text: "Usage: /plugin uninstall <name>[@marketplace]"}
	}
	parts := strings.Fields(args)
	spec := parts[0]

	cwd, _ := os.Getwd()
	if err := plugins.Uninstall(spec, "user", cwd); err != nil {
		return Result{Type: "error", Text: fmt.Sprintf("Uninstall failed: %v", err)}
	}
	return Result{Type: "text", Text: fmt.Sprintf("Uninstalled %s.", spec)}
}

func pluginList(ps []*plugins.Plugin) Result {
	installed, err := plugins.LoadInstalledPlugins()
	if err != nil {
		return Result{Type: "error", Text: fmt.Sprintf("Failed to read plugin registry: %v", err)}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Installed plugins (%d registered, %d loaded):\n\n", len(installed.Plugins), len(ps))

	// Show registered (from installed_plugins.json).
	ids := make([]string, 0, len(installed.Plugins))
	for id := range installed.Plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		entries := installed.Plugins[id]
		for _, e := range entries {
			fmt.Fprintf(&sb, "  %-40s %s (%s)\n", id, e.Version, e.Scope)
		}
	}
	if len(ids) == 0 {
		sb.WriteString("  (none)\n")
	}
	return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
}

func pluginMarketplace(args string) Result {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		return pluginMarketplaceList()
	}
	sub := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = strings.Join(parts[1:], " ")
	}
	switch sub {
	case "add":
		return pluginMarketplaceAdd(rest)
	case "list", "ls":
		return pluginMarketplaceList()
	case "remove", "rm":
		return pluginMarketplaceRemove(rest)
	case "update":
		return pluginMarketplaceUpdate(rest)
	default:
		return Result{Type: "error", Text: "Usage: /plugin marketplace <add|list|remove|update> [args]"}
	}
}

func pluginMarketplaceAdd(args string) Result {
	if args == "" {
		return Result{Type: "error", Text: "Usage: /plugin marketplace add <source> [name]\n  source: owner/repo, https://... or local path"}
	}
	parts := strings.Fields(args)
	source := parts[0]
	name := ""
	if len(parts) > 1 {
		name = parts[1]
	}
	if name == "" {
		// Derive name from source.
		name = deriveMarketplaceName(source)
	}
	if err := plugins.MarketplaceAdd(context.Background(), name, source, nil); err != nil {
		return Result{Type: "error", Text: fmt.Sprintf("marketplace add failed: %v", err)}
	}
	return Result{Type: "text", Text: fmt.Sprintf("Added marketplace %q from %s\nUse /plugin install <name>@%s to install plugins.", name, source, name)}
}

func pluginMarketplaceList() Result {
	known, err := plugins.LoadKnownMarketplaces()
	if err != nil {
		return Result{Type: "error", Text: fmt.Sprintf("Failed to read marketplaces: %v", err)}
	}
	if len(known) == 0 {
		return Result{Type: "text", Text: "No marketplaces configured.\nUse /plugin marketplace add <source> to add one."}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Configured marketplaces (%d):\n\n", len(known))
	names := make([]string, 0, len(known))
	for n := range known {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		e := known[n]
		src := e.Source.Repo
		if src == "" {
			src = e.Source.URL
		}
		if src == "" {
			src = e.Source.Path
		}
		fmt.Fprintf(&sb, "  %-30s %s\n  Updated: %s\n\n", n, src, e.LastUpdated)
	}
	return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
}

func pluginMarketplaceRemove(name string) Result {
	if name == "" {
		return Result{Type: "error", Text: "Usage: /plugin marketplace remove <name>"}
	}
	if err := plugins.MarketplaceRemove(name); err != nil {
		return Result{Type: "error", Text: fmt.Sprintf("marketplace remove: %v", err)}
	}
	return Result{Type: "text", Text: fmt.Sprintf("Removed marketplace %q.", name)}
}

func pluginMarketplaceUpdate(name string) Result {
	if err := plugins.MarketplaceUpdate(context.Background(), name); err != nil {
		return Result{Type: "error", Text: fmt.Sprintf("marketplace update: %v", err)}
	}
	if name == "" {
		return Result{Type: "text", Text: "All marketplaces updated."}
	}
	return Result{Type: "text", Text: fmt.Sprintf("Marketplace %q updated.", name)}
}

func deriveMarketplaceName(source string) string {
	// "anthropics/claude-plugins-official" → "claude-plugins-official"
	if idx := strings.LastIndex(source, "/"); idx >= 0 {
		return source[idx+1:]
	}
	// Strip .git suffix.
	name := strings.TrimSuffix(source, ".git")
	// Use last path component.
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}
