package commands

import (
	"bytes"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/icehunter/conduit/internal/plugins"
	"github.com/icehunter/conduit/internal/skillusage"
)

// RegisterSkillsCommand registers /skills and its subcommands.
//
// Subcommands:
//
//	/skills            — list available skills from installed plugins
//	/skills status     — print a table of all skill usage records
//	/skills pin <name> — pin a skill so it is never auto-archived
//	/skills unpin <name> — unpin a skill
//	/skills backups    — list available skill backup snapshots
//	/skills rollback <id> — restore skills from a backup snapshot
func RegisterSkillsCommand(r *Registry, ps []*plugins.Plugin) {
	r.Register(Command{
		Name:        "skills",
		Description: "List available skills from installed plugins",
		Handler: func(args string) Result {
			args = strings.TrimSpace(args)
			if args == "" {
				return skillsList(ps)
			}
			parts := strings.Fields(args)
			sub := parts[0]
			rest := strings.TrimSpace(strings.TrimPrefix(args, sub))
			switch sub {
			case "status":
				return skillsStatus()
			case "pin":
				return skillsPin(rest)
			case "unpin":
				return skillsUnpin(rest)
			case "backups":
				return skillsBackups()
			case "rollback":
				return skillsRollback(rest)
			default:
				return Result{
					Type: "error",
					Text: fmt.Sprintf("Unknown /skills sub-command %q.\n\nUsage:\n  /skills            — list plugin skills\n  /skills status     — show skill usage records\n  /skills pin <name> — pin a skill\n  /skills unpin <name> — unpin a skill\n  /skills backups    — list backup snapshots\n  /skills rollback <id> — restore from a snapshot", sub),
				}
			}
		},
	})
}

// skillsList renders the default /skills output (plugin skill listing).
func skillsList(ps []*plugins.Plugin) Result {
	if len(ps) == 0 {
		return Result{Type: "text", Text: "No plugins installed. Use /plugin to browse and install plugins."}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Available skills (%d installed plugins):\n\n", len(ps))
	total := 0
	for _, p := range ps {
		if len(p.Commands) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "**%s** (%d skill%s):\n", p.Manifest.Name, len(p.Commands), pluralS(len(p.Commands)))
		for _, cmd := range p.Commands {
			desc := cmd.Description
			if len([]rune(desc)) > 70 {
				desc = string([]rune(desc)[:69]) + "…"
			}
			fmt.Fprintf(&sb, "  /%s", cmd.QualifiedName)
			if desc != "" {
				sb.WriteString(" — " + desc)
			}
			sb.WriteByte('\n')
			total++
		}
		sb.WriteByte('\n')
	}
	fmt.Fprintf(&sb, "Total: %d skills. The model will use these automatically when relevant.", total)
	return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
}

// skillsStatus prints a formatted table of all skill usage records.
func skillsStatus() Result {
	records := skillusage.All()
	if len(records) == 0 {
		return Result{Type: "text", Text: "No skill usage records found."}
	}

	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "Name\tScope\tCreated By\tUses\tState\tPinned")
	fmt.Fprintln(w, "────────────────\t──────────────\t──────────\t────\t────────\t──────")
	for _, r := range records {
		pinned := "no"
		if r.Pinned {
			pinned = "yes"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\n",
			r.Name, r.Scope, r.CreatedBy, r.UseCount, r.State, pinned)
	}
	_ = w.Flush()
	return Result{Type: "text", Text: strings.TrimRight(buf.String(), "\n")}
}

// skillsPin pins the named skill.
func skillsPin(name string) Result {
	if name == "" {
		return Result{Type: "error", Text: "Usage: /skills pin <name>"}
	}
	skillusage.Pin(name)
	return Result{Type: "text", Text: fmt.Sprintf("Pinned skill %q.", name)}
}

// skillsUnpin unpins the named skill.
func skillsUnpin(name string) Result {
	if name == "" {
		return Result{Type: "error", Text: "Usage: /skills unpin <name>"}
	}
	skillusage.Unpin(name)
	return Result{Type: "text", Text: fmt.Sprintf("Unpinned skill %q.", name)}
}

// skillsBackups lists available skill backup snapshots.
func skillsBackups() Result {
	backups, err := skillusage.ListBackups()
	if err != nil {
		return Result{Type: "error", Text: fmt.Sprintf("skills backups: %v", err)}
	}
	if len(backups) == 0 {
		return Result{Type: "text", Text: "No skill backups found."}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Skill backups (%d):\n\n", len(backups))
	for _, b := range backups {
		fmt.Fprintf(&sb, "  %-40s  %s\n", b.ID, b.CreatedAt.Format("2006-01-02 15:04:05 UTC"))
	}
	sb.WriteString("\nUse /skills rollback <id> to restore a snapshot.")
	return Result{Type: "text", Text: strings.TrimRight(sb.String(), "\n")}
}

// skillsRollback restores skills from the identified backup snapshot.
func skillsRollback(id string) Result {
	if id == "" {
		return Result{Type: "error", Text: "Usage: /skills rollback <id>"}
	}
	if err := skillusage.Rollback(id); err != nil {
		return Result{Type: "error", Text: fmt.Sprintf("skills rollback: %v", err)}
	}
	return Result{Type: "text", Text: fmt.Sprintf("Skills restored from snapshot %q.\nA pre-rollback backup was saved automatically.", id)}
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
