package tui

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
)

type councilTranscriptArgs struct {
	question  string
	members   []councilMember
	synthesis string
	usage     api.Usage
	costUSD   float64
}

// persistCouncilTranscript writes the debate transcript to
// ~/.conduit/projects/<sha256(cwd)[:12]>/council/<timestamp>.md.
// Best-effort: errors are returned but callers ignore them.
func persistCouncilTranscript(args councilTranscriptArgs) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cwd, _ := os.Getwd()
	h := sha256.Sum256([]byte(cwd))
	projKey := fmt.Sprintf("%x", h[:6])
	dir := filepath.Join(home, ".conduit", "projects", projKey, "council")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	ts := time.Now().UTC().Format("2006-01-02T15-04-05Z")
	path := filepath.Join(dir, ts+".md")

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Council Debate — %s\n\n", ts)
	fmt.Fprintf(&sb, "**Question/Task:** %s\n\n", args.question)
	fmt.Fprintf(&sb, "---\n\n")
	for _, m := range args.members {
		if m.lastResponse == "" {
			continue
		}
		status := "active"
		if !m.active {
			status = "ejected"
		}
		fmt.Fprintf(&sb, "## %s (%s)\n\n%s\n\n", m.label, status, m.lastResponse)
	}
	fmt.Fprintf(&sb, "---\n\n## Synthesis\n\n%s\n\n", args.synthesis)
	fmt.Fprintf(&sb, "---\n\n")
	fmt.Fprintf(&sb, "**Tokens:** %d in / %d out",
		args.usage.InputTokens, args.usage.OutputTokens)
	if args.costUSD > 0 {
		fmt.Fprintf(&sb, " · **Cost:** $%.4f", args.costUSD)
	}
	fmt.Fprintf(&sb, "\n")

	return os.WriteFile(path, []byte(sb.String()), 0o644)
}
