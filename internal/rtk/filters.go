package rtk

import (
	"regexp"
	"strings"
)

// ── Generic helpers ───────────────────────────────────────────────────────────

func truncateLines(output string, maxLines int) string {
	lines := strings.Split(output, "\n")
	if len(lines) <= maxLines {
		return output
	}
	dropped := len(lines) - maxLines
	return strings.Join(lines[:maxLines], "\n") + "\n[" + itoa(dropped) + " lines omitted by RTK]"
}

func filterTruncate(_ string, output string) string { return truncateLines(output, 150) }

func subcommand(cmd string) string {
	parts := strings.Fields(cmd)
	for i := 1; i < len(parts); i++ {
		if !strings.HasPrefix(parts[i], "-") {
			return parts[i]
		}
	}
	return ""
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// keepMatching returns only lines where any predicate returns true.
func keepMatching(output string, tests ...func(string) bool) string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		for _, t := range tests {
			if t(line) {
				out = append(out, line)
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

func hasAny(line string, subs ...string) bool {
	l := strings.ToLower(line)
	for _, s := range subs {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

func hasPrefix(line string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}


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

// ── Vitest / Jest ──────────────────────────────────────────────────────────────

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

// ── ls ────────────────────────────────────────────────────────────────────────

func filterLS(_ string, output string) string { return truncateLines(output, 200) }

// ── find ─────────────────────────────────────────────────────────────────────

func filterFind(_ string, output string) string { return truncateLines(output, 200) }

// ── tree ─────────────────────────────────────────────────────────────────────

func filterTree(_ string, output string) string { return truncateLines(output, 150) }

// ── cat/head/tail ─────────────────────────────────────────────────────────────

func filterReadFile(_ string, output string) string { return truncateLines(output, 300) }

// ── grep / rg ─────────────────────────────────────────────────────────────────

func filterGrep(_ string, output string) string { return truncateLines(output, 200) }

// ── diff ─────────────────────────────────────────────────────────────────────

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
	secretRe := regexp.MustCompile(`(?i)(AKIA[0-9A-Z]{16}|aws_secret_access_key\s*=\s*\S+)`)
	output = secretRe.ReplaceAllString(output, "[REDACTED]")
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

