package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/settings"
)

// RegisterMCPCommand registers /mcp — interactive MCP server browser.
func RegisterMCPCommand(r *Registry, manager *mcp.Manager) {
	r.Register(Command{
		Name:        "mcp",
		Description: "Manage MCP server connections",
		Handler: func(args string) Result {
			if manager == nil {
				return Result{Type: "mcp-dialog", Text: ""}
			}

			servers := manager.Servers()

			// /mcp tools — plain text list of all tools across all servers.
			if strings.TrimSpace(strings.ToLower(args)) == "tools" {
				sort.Slice(servers, func(i, j int) bool {
					return servers[i].Name < servers[j].Name
				})
				var sb strings.Builder
				sb.WriteString("MCP Tools:\n\n")
				for _, srv := range servers {
					if srv.Status != mcp.StatusConnected {
						continue
					}
					sb.WriteString(fmt.Sprintf("  %s (%d tools):\n", srv.Name, len(srv.Tools)))
					for _, t := range srv.Tools {
						desc := t.Description
						if len([]rune(desc)) > 60 {
							desc = string([]rune(desc)[:59]) + "…"
						}
						sb.WriteString(fmt.Sprintf("    • %s%s — %s\n",
							mcp.NormalizeServerName(srv.Name), t.Name, desc))
					}
				}
				return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
			}

			// Sort: user servers first, then plugin servers.
			sort.Slice(servers, func(i, j int) bool {
				si, sj := servers[i].Config.Scope, servers[j].Config.Scope
				if si != sj {
					// user/local/project before plugin
					if si == "plugin" {
						return false
					}
					if sj == "plugin" {
						return true
					}
				}
				return servers[i].Name < servers[j].Name
			})

			// Encode as tab-separated lines for the panel to parse.
			// Format: name\tscope\tsource\tstatus\tcommand\targs\ttoolCount\terror
			var lines []string
			for _, srv := range servers {
				scope := scopeLabel(srv.Config.Scope)
				source := srv.Config.Source
				status := string(srv.Status)
				if srv.Disabled {
					status = "disabled"
				}
				cmd := srv.Config.Command
				if cmd == "" {
					cmd = srv.Config.URL
				}
				args := strings.Join(srv.Config.Args, " ")
				toolCount := fmt.Sprintf("%d", len(srv.Tools))
				errStr := srv.Error
				disabled := "0"
				if srv.Disabled {
					disabled = "1"
				}
				// name\tscope\tsource\tstatus\tcommand\targs\ttoolCount\terr\tdisabled
				line := strings.Join([]string{srv.Name, scope, source, status, cmd, args, toolCount, errStr, disabled}, "\t")
				lines = append(lines, line)
			}
			return Result{Type: "mcp-dialog", Text: strings.Join(lines, "\n")}
		},
	})
}

// RegisterMCPApproveCommand registers /mcp-approve, the back-end for the
// startup approval picker. Args: "<server-name> <yes|yes_all|no>". Persists
// the choice to user settings then triggers a reconnect for "yes"/"yes_all".
func RegisterMCPApproveCommand(r *Registry, manager *mcp.Manager, cwd string) {
	r.Register(Command{
		Name:        "mcp-approve",
		Description: "Internal: approve or deny a project-scope MCP server",
		Handler: func(args string) Result {
			parts := strings.SplitN(strings.TrimSpace(args), " ", 2)
			if len(parts) != 2 {
				return Result{Type: "error", Text: "Usage: /mcp-approve <name> <yes|yes_all|no>"}
			}
			name, choice := parts[0], parts[1]
			if err := settings.ApproveMcpjsonServer(name, choice); err != nil {
				return Result{Type: "error", Text: fmt.Sprintf("mcp-approve: %v", err)}
			}
			if (choice == "yes" || choice == "yes_all") && manager != nil {
				_ = manager.Reconnect(context.Background(), name, cwd)
			}
			verb := "Approved"
			if choice == "no" {
				verb = "Denied"
			} else if choice == "yes_all" {
				verb = "Approved (all project servers)"
			}
			return Result{Type: "flash", Text: fmt.Sprintf("%s MCP server: %s", verb, name)}
		},
	})
}

func scopeLabel(scope string) string {
	switch scope {
	case "user", "local":
		return "User"
	case "project":
		return "Project"
	case "plugin":
		return "Built-in"
	default:
		return "User"
	}
}
