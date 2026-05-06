// Derived from RTK (https://github.com/rtk-ai/rtk).
// Copyright 2024 rtk-ai and rtk-ai Labs
// Licensed under the Apache License, Version 2.0; see LICENSE-APACHE.
// This file has been modified from the original Rust source.

package rtk

import "strings"

// ── npm / pnpm / yarn / bun ───────────────────────────────────────────────────

func filterNpm(cmd, output string) string {
	sub := subcommand(cmd)
	if sub == "test" || sub == "run" {
		return filterNpmTest("", output)
	}
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if hasAny(line, "error", "warn") || hasPrefix(line, "npm ERR!") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return truncateLines(output, 30)
	}
	return strings.Join(out, "\n")
}

func filterNpmTest(_ string, output string) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if hasAny(line, "fail", "error", "pass", "✓", "✗", "×", "●") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return truncateLines(output, 50)
	}
	return truncateLines(strings.Join(out, "\n"), 150)
}

// ── Vitest / Jest ─────────────────────────────────────────────────────────────

func filterVitest(_ string, output string) string { return filterNpmTest("", output) }

// ── Playwright ────────────────────────────────────────────────────────────────

func filterPlaywright(_ string, output string) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if hasAny(line, "failed", "passed", "error", "timeout", "expect") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return truncateLines(output, 80)
	}
	return strings.Join(out, "\n")
}

// ── ESLint / tsc ──────────────────────────────────────────────────────────────

func filterESLint(_ string, output string) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return truncateLines(strings.Join(out, "\n"), 200)
}
