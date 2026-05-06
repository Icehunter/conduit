// Derived from RTK (https://github.com/rtk-ai/rtk).
// Copyright 2024 rtk-ai and rtk-ai Labs
// Licensed under the Apache License, Version 2.0; see LICENSE-APACHE.
// This file has been modified from the original Rust source.

package rtk

import "strings"

// ── Git ───────────────────────────────────────────────────────────────────────

func filterGit(cmd, output string) string {
	sub := subcommand(cmd)
	switch sub {
	case "log":
		return filterGitLog(output)
	case "diff", "show":
		return filterGitDiff(output)
	case "status":
		return truncateLines(output, 80)
	default:
		return truncateLines(output, 100)
	}
}

func filterGitLog(output string) string {
	// Collapse verbose log to one line per commit: "<hash7> <subject>"
	var out []string
	var currentHash string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "commit ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && len(fields[1]) >= 7 {
				currentHash = fields[1][:7]
			}
		} else if strings.HasPrefix(line, "Author:") || strings.HasPrefix(line, "Date:") || line == "" {
			continue
		} else if currentHash != "" {
			subject := strings.TrimSpace(line)
			if subject != "" {
				out = append(out, currentHash+" "+subject)
				currentHash = ""
			}
		} else {
			// Already compact (--oneline)
			out = append(out, line)
		}
	}
	return truncateLines(strings.Join(out, "\n"), 50)
}

func filterGitDiff(output string) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "index ") {
			continue // skip blob hash lines
		}
		out = append(out, line)
	}
	return truncateLines(strings.Join(out, "\n"), 500)
}

// ── GitHub / GitLab CLI ───────────────────────────────────────────────────────

func filterGH(cmd, output string) string {
	sub := subcommand(cmd)
	if sub == "run" {
		return filterGHRun(output)
	}
	return truncateLines(output, 150)
}

func filterGHRun(output string) string {
	out := keepMatching(output,
		func(l string) bool { return hasAny(l, "fail", "error", "success", "complet", "conclusion") },
		func(l string) bool { return hasPrefix(l, "✓", "✗", "X ") },
	)
	if out == "" {
		return truncateLines(output, 100)
	}
	return out
}
