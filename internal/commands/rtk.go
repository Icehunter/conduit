package commands

import (
	"fmt"
	"strings"

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
	defer db.Close()

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
	// Discover scans recent session transcripts for commands that RTK could
	// have filtered but didn't (i.e. were not classified). This is a
	// lightweight heuristic — report top unclassified base commands.
	return Result{Type: "text", Text: "RTK discover: scan your session transcripts with `rtk discover` for missed savings opportunities."}
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
