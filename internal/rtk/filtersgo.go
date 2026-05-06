// Derived from RTK (https://github.com/rtk-ai/rtk).
// Copyright 2024 rtk-ai and rtk-ai Labs
// Licensed under the Apache License, Version 2.0; see LICENSE-APACHE.
// This file has been modified from the original Rust source.

package rtk

import (
	"regexp"
	"strings"
)

// ── Go ────────────────────────────────────────────────────────────────────────

func filterGo(cmd, output string) string {
	sub := subcommand(cmd)
	switch sub {
	case "test":
		return filterGoTest(output)
	case "build", "vet", "run":
		return filterGoBuild(output)
	default:
		return truncateLines(output, 80)
	}
}

func filterGoTest(output string) string {
	var out []string
	inPanic := false
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "panic:") {
			inPanic = true
		}
		if inPanic {
			out = append(out, line)
			if line == "" {
				inPanic = false
			}
			continue
		}
		upper := strings.ToUpper(line)
		if hasPrefix(upper, "--- FAIL", "FAIL") ||
			hasPrefix(line, "ok ", "=== RUN") ||
			hasAny(line, "coverage:", "error:", "failed", "panic") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		lines := strings.Split(strings.TrimSpace(output), "\n")
		return lines[len(lines)-1]
	}
	return strings.Join(out, "\n")
}

func filterGoBuild(output string) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if hasAny(line, "error", "undefined", "cannot", "declared and not used", "syntax error") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return truncateLines(output, 30)
	}
	return strings.Join(out, "\n")
}

func filterGolangciLint(_ string, output string) string {
	fileLineRe := regexp.MustCompile(`^\S+\.go:\d+`)
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if fileLineRe.MatchString(line) || hasAny(line, "issues found", "fail") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return truncateLines(output, 50)
	}
	return strings.Join(out, "\n")
}
