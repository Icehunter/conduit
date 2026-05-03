// Package rtk is an in-process port of RTK (Rust Token Killer).
// It intercepts tool output from BashTool and compresses it before the agent
// sees it, saving 60-90% of tokens on common dev operations.
//
// Architecture mirrors RTK's Rust source at /Volumes/Engineering/Icehunter/rtk/src/:
//   - registry.go   — command classification (ports discover/registry.rs)
//   - filters/      — per-category output transformers (ports cmds/*)
//   - ansi.go       — ANSI escape stripping (ports core/utils.rs)
package rtk

import (
	"strings"
)

// Result is returned by Filter.
type Result struct {
	Original   string
	Filtered   string
	SavedBytes int
	SavingsPct float64
	Category   string
}

// IsClassified returns true if the command is handled by a RTK filter rule.
// Used by /rtk discover to find unhandled commands.
func IsClassified(cmd string) bool {
	return classify(strings.TrimSpace(cmd)) != nil
}

// Filter applies RTK compression to the output of the given shell command.
// cmd is the full command string; output is its combined stdout+stderr.
// Returns the (possibly compressed) output and metadata.
func Filter(cmd, output string) Result {
	cmd = strings.TrimSpace(cmd)
	output = stripANSI(output)

	rule := classify(cmd)
	if rule == nil {
		return Result{Original: output, Filtered: output}
	}

	filtered := rule.filter(cmd, output)

	orig := len(output)
	comp := len(filtered)
	saved := orig - comp
	pct := 0.0
	if orig > 0 {
		pct = float64(saved) / float64(orig) * 100
	}

	return Result{
		Original:   output,
		Filtered:   filtered,
		SavedBytes: saved,
		SavingsPct: pct,
		Category:   rule.category,
	}
}
