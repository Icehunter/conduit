package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/rtk"
	"github.com/icehunter/conduit/internal/rtk/track"
)

// RegisterRTKCommands adds /rtk gain and /rtk discover slash commands.
func RegisterRTKCommands(r *Registry) {
	r.Register(Command{
		Name:        "rtk",
		Description: "RTK token savings — usage: /rtk gain | /rtk discover",
		Handler: func(args string) Result {
			sub := strings.TrimSpace(args)
			switch sub {
			case "gain", "":
				return rtkGain()
			case "discover":
				return rtkDiscover()
			default:
				return Result{Type: "error", Text: fmt.Sprintf("Unknown rtk subcommand %q. Usage: /rtk gain | /rtk discover", sub)}
			}
		},
	})
}

func rtkGain() Result {
	db, err := track.Open()
	if err != nil {
		return Result{Type: "error", Text: fmt.Sprintf("RTK: could not open history: %v", err)}
	}
	defer func() { _ = db.Close() }()

	totalOrig, totalFiltered, rows, err := db.Gain()
	if err != nil {
		return Result{Type: "error", Text: fmt.Sprintf("RTK: gain query: %v", err)}
	}
	if rows == 0 {
		return Result{Type: "text", Text: "RTK: no filter history yet. Run some bash commands and check back."}
	}

	saved := totalOrig - totalFiltered
	pct := 0.0
	if totalOrig > 0 {
		pct = float64(saved) / float64(totalOrig) * 100
	}

	return Result{Type: "text", Text: fmt.Sprintf(
		"RTK token savings (%d commands filtered)\n"+
			"  Original : %s\n"+
			"  Filtered : %s\n"+
			"  Saved    : %s (%.1f%%)",
		rows,
		humanBytes(totalOrig),
		humanBytes(totalFiltered),
		humanBytes(saved),
		pct,
	)}
}

func rtkDiscover() Result {
	// Scan recent session JSONL transcripts for Bash tool calls that RTK
	// does not classify (no savings rule). Rank by frequency and report the
	// top candidates as new rule opportunities.
	home, err := os.UserHomeDir()
	if err != nil {
		return Result{Type: "error", Text: "cannot find home directory"}
	}
	projectsDir := filepath.Join(home, ".claude", "projects")
	dirs, _ := os.ReadDir(projectsDir)

	// Count unclassified base commands across all sessions.
	counts := map[string]int{}
	scanned := 0

	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(projectsDir, d.Name()))
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			scanned++
			data, err := os.ReadFile(filepath.Join(projectsDir, d.Name(), f.Name()))
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				var entry struct {
					Type    string          `json:"type"`
					Message json.RawMessage `json:"message"`
				}
				if err := json.Unmarshal([]byte(line), &entry); err != nil {
					continue
				}
				if entry.Type != "message" {
					continue
				}
				// Look for assistant tool_use blocks of type Bash.
				var msg struct {
					Role    string `json:"role"`
					Content []struct {
						Type  string `json:"type"`
						Name  string `json:"name"`
						Input struct {
							Command string `json:"command"`
						} `json:"input"`
					} `json:"content"`
				}
				if err := json.Unmarshal(entry.Message, &msg); err != nil {
					continue
				}
				if msg.Role != "assistant" {
					continue
				}
				for _, block := range msg.Content {
					if block.Type != "tool_use" || block.Name != "Bash" {
						continue
					}
					cmd := strings.TrimSpace(block.Input.Command)
					if cmd == "" || rtk.IsClassified(cmd) {
						continue
					}
					// Extract base command.
					base := strings.Fields(cmd)[0]
					base = filepath.Base(base)
					counts[base]++
				}
			}
		}
	}

	if len(counts) == 0 {
		return Result{Type: "text", Text: fmt.Sprintf("RTK discover: scanned %d sessions — no unclassified Bash commands found. RTK is covering all commands.", scanned)}
	}

	// Sort by frequency descending.
	type pair struct {
		cmd   string
		count int
	}
	var ranked []pair
	for k, v := range counts {
		ranked = append(ranked, pair{k, v})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count != ranked[j].count {
			return ranked[i].count > ranked[j].count
		}
		return ranked[i].cmd < ranked[j].cmd
	})
	if len(ranked) > 15 {
		ranked = ranked[:15]
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "RTK discover — scanned %d sessions\n\n", scanned)
	sb.WriteString("Unclassified commands (potential RTK savings):\n\n")
	for _, p := range ranked {
		fmt.Fprintf(&sb, "  %-20s  %d calls\n", p.cmd, p.count)
	}
	sb.WriteString("\nThese commands have no RTK filter rule. Adding rules would save tokens on future runs.")
	return Result{Type: "text", Text: sb.String()}
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
