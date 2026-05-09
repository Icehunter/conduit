package tui

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/decisionlog"
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

	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return err
	}

	// Auto-record a decision entry so future sessions inherit the council verdict.
	// Best-effort: log and continue — a decision journal write failure must not
	// block the user from seeing or approving the synthesised plan.
	if entry, ok := extractCouncilDecision(args, path); ok {
		_ = decisionlog.Append(cwd, entry) // best-effort; failure is non-fatal
	}

	return nil
}

// extractCouncilDecision derives a decisionlog.Entry from a completed council
// transcript. Returns (entry, true) when the synthesis is non-empty; (zero, false)
// otherwise so callers can skip the write gracefully.
func extractCouncilDecision(args councilTranscriptArgs, transcriptPath string) (decisionlog.Entry, bool) {
	synthesis := strings.TrimSpace(args.synthesis)
	if synthesis == "" {
		return decisionlog.Entry{}, false
	}

	// Scope: first ~80 chars of the question, stripped of newlines.
	scope := strings.ReplaceAll(args.question, "\n", " ")
	scope = strings.TrimSpace(scope)
	if len(scope) > 80 {
		scope = scope[:79] + "…"
	}

	// Summary: first sentence or paragraph of the synthesis, capped at 240 runes.
	summary := firstSentence(synthesis)

	// Why: cite the council debate itself.
	why := fmt.Sprintf("council synthesis from %d member(s)", countActiveMembers(args.members))
	if transcriptPath != "" {
		why += fmt.Sprintf("; transcript: %s", filepath.Base(transcriptPath))
	}

	return decisionlog.Entry{
		Kind:           decisionlog.KindChose,
		Scope:          scope,
		Summary:        summary,
		Why:            why,
		RelatedCouncil: transcriptPath,
	}, true
}

// firstSentence extracts the first sentence from text, capped at 240 runes.
// Splits on ". ", "! ", "? ", or the first newline.
func firstSentence(text string) string {
	text = strings.TrimSpace(text)
	for i, r := range text {
		if r == '\n' {
			text = text[:i]
			break
		}
		if (r == '.' || r == '!' || r == '?') && i+1 < len(text) && text[i+1] == ' ' {
			text = text[:i+1]
			break
		}
	}
	runes := []rune(strings.TrimSpace(text))
	if len(runes) > 240 {
		return string(runes[:239]) + "…"
	}
	return string(runes)
}

// countActiveMembers returns the number of council members who contributed a response.
func countActiveMembers(members []councilMember) int {
	n := 0
	for _, m := range members {
		if m.lastResponse != "" {
			n++
		}
	}
	return n
}
