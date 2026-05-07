// Derived from RTK (https://github.com/rtk-ai/rtk).
// Copyright 2024 rtk-ai and rtk-ai Labs
// Licensed under the Apache License, Version 2.0; see LICENSE-APACHE.
// This file has been modified from the original Rust source.

package rtk

import (
	"regexp"
	"strings"
)

// ── Cargo ─────────────────────────────────────────────────────────────────────

func filterCargo(cmd, output string) string {
	sub := subcommand(cmd)
	switch sub {
	case "test", "nextest":
		return filterCargoTest(output)
	case "build", "check", "fmt":
		return filterCargoBuildOutput(output)
	case "clippy":
		return filterCargoBuildOutput(output)
	default:
		return truncateLines(output, 80)
	}
}

func filterCargoTest(output string) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if hasAny(line, "failed", "panicked", "thread '") ||
			hasPrefix(line, "test result:", "---- ") ||
			hasAny(line, "failures:") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		lines := strings.Split(strings.TrimSpace(output), "\n")
		return lines[len(lines)-1]
	}
	return strings.Join(out, "\n")
}

func filterCargoBuildOutput(output string) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if hasPrefix(line, "error", "warning", " -->", "  |") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return truncateLines(output, 20)
	}
	return strings.Join(out, "\n")
}

// ── Python ────────────────────────────────────────────────────────────────────

func filterPytest(_ string, output string) string {
	var out []string
	inSummary := false
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "=") && strings.Contains(line, "short test summary") {
			inSummary = true
		}
		if inSummary || hasPrefix(line, "FAILED", "ERROR") ||
			hasAny(line, "assertionerror") || hasPrefix(line, "E ") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		lines := strings.Split(strings.TrimSpace(output), "\n")
		return lines[len(lines)-1]
	}
	return strings.Join(out, "\n")
}

func filterPythonLint(_ string, output string) string { return truncateLines(output, 100) }

// ── Ruby ──────────────────────────────────────────────────────────────────────

func filterRubyTest(_ string, output string) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if hasAny(line, "failure:", "error:", "failed", "examples,") ||
			hasPrefix(line, "F", "E") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		lines := strings.Split(strings.TrimSpace(output), "\n")
		return lines[len(lines)-1]
	}
	return strings.Join(out, "\n")
}

func filterRuboCop(_ string, output string) string {
	var out []string
	fileRe := regexp.MustCompile(`\.(rb|rake):\d+`)
	for _, line := range strings.Split(output, "\n") {
		if fileRe.MatchString(line) || hasAny(line, "offense", "corrected") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return truncateLines(output, 50)
	}
	return strings.Join(out, "\n")
}

// ── .NET ──────────────────────────────────────────────────────────────────────

func filterDotnetBuild(_ string, output string) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if hasAny(line, "error", "warning", "failed") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return truncateLines(output, 20)
	}
	return strings.Join(out, "\n")
}

func filterDotnetTest(_ string, output string) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if hasAny(line, "failed", "passed", "error", "x ") ||
			strings.HasPrefix(line, "Failed!") || strings.HasPrefix(line, "Passed!") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		lines := strings.Split(strings.TrimSpace(output), "\n")
		return lines[len(lines)-1]
	}
	return strings.Join(out, "\n")
}

// ── Filesystem ────────────────────────────────────────────────────────────────

func filterLS(_ string, output string) string { return truncateLines(output, 200) }

func filterFind(_ string, output string) string { return truncateLines(output, 200) }

func filterTree(_ string, output string) string { return truncateLines(output, 150) }

func filterReadFile(_ string, output string) string { return truncateLines(output, 300) }

func filterGrep(_ string, output string) string { return truncateLines(output, 200) }

func filterDiff(_ string, output string) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if !strings.HasPrefix(line, "index ") {
			out = append(out, line)
		}
	}
	return truncateLines(strings.Join(out, "\n"), 500)
}

// ── Cloud / Infra ─────────────────────────────────────────────────────────────

func filterAWS(_ string, output string) string {
	// Strip access keys / secrets from output.
	// Covers AKIA (long-term) and ASIA (STS/short-term) access key IDs,
	// secret access key and session token in INI and JSON forms.
	secretRe := regexp.MustCompile(
		`(?i)(AKIA[0-9A-Z]{16}` +
			`|ASIA[0-9A-Z]{16}` +
			`|aws_secret_access_key\s*=\s*\S+` +
			`|aws_session_token\s*=\s*\S+)`)
	output = secretRe.ReplaceAllString(output, "[REDACTED]")
	// Redact JSON keys: "SecretAccessKey":"..." and "SessionToken":"..."
	jsonRe := regexp.MustCompile(`(?i)("SecretAccessKey"\s*:\s*)"[^"]*"`)
	output = jsonRe.ReplaceAllString(output, `$1"[REDACTED]"`)
	jsonRe2 := regexp.MustCompile(`(?i)("SessionToken"\s*:\s*)"[^"]*"`)
	output = jsonRe2.ReplaceAllString(output, `$1"[REDACTED]"`)
	return truncateLines(output, 150)
}

func filterDocker(_ string, output string) string { return truncateLines(output, 100) }

func filterKubectl(_ string, output string) string { return truncateLines(output, 100) }

func filterTerraform(_ string, output string) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if hasAny(line, "error", "must be replaced", "will be destroyed", "plan:", "changes to") ||
			hasPrefix(line, "+ ", "- ", "~ ", "Plan:", "Error") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return truncateLines(output, 100)
	}
	return strings.Join(out, "\n")
}

// ── Network ───────────────────────────────────────────────────────────────────

func filterCurl(_ string, output string) string { return truncateLines(output, 200) }

func filterPing(_ string, output string) string { return truncateLines(output, 20) }

// ── Build systems ─────────────────────────────────────────────────────────────

func filterMake(_ string, output string) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if hasAny(line, "error:", "*** ") || hasPrefix(line, "make[") && hasAny(line, "error") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return truncateLines(output, 100)
	}
	return strings.Join(out, "\n")
}

func filterMaven(_ string, output string) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if hasAny(line, "[error]", "[warning]", "build failure", "build success") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return truncateLines(output, 80)
	}
	return strings.Join(out, "\n")
}

func filterSwift(_ string, output string) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		if hasAny(line, "error:", "warning:", "passed", "failed") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return truncateLines(output, 80)
	}
	return strings.Join(out, "\n")
}

func filterBuildOutput(_ string, output string) string { return truncateLines(output, 80) }

// ── System utilities ──────────────────────────────────────────────────────────

func filterSystemctl(_ string, output string) string { return truncateLines(output, 50) }
