// Derived from RTK (https://github.com/rtk-ai/rtk).
// Copyright 2024 rtk-ai and rtk-ai Labs
// Licensed under the Apache License, Version 2.0; see LICENSE-APACHE.
// This file has been modified from the original Rust source.

package rtk

import (
	"regexp"
	"strings"
)

// rule maps a command pattern to a filter function and metadata.
type rule struct {
	re       *regexp.Regexp
	category string
	filter   func(cmd, output string) string
}

// rules is the ordered list of RTK rewrite rules.
// Mirrors discover/registry.rs RULES (75 rules) — first match wins.
var rules = buildRules()

func buildRules() []*rule {
	return []*rule{
		// ── Git ──────────────────────────────────────────────────────────────
		{re: re(`^(?:git|yadm)\s+(?:-[Cc]\s+\S+\s+)*(?:status|log|diff|show|add|commit|push|pull|branch|fetch|stash|worktree)`), category: "Git", filter: filterGit},

		// ── GitHub CLI ───────────────────────────────────────────────────────
		{re: re(`^gh\s+(?:pr|issue|run|repo|api|release)`), category: "GitHub", filter: filterGH},

		// ── GitLab CLI ───────────────────────────────────────────────────────
		{re: re(`^glab\s+(?:mr|issue|ci|pipeline|api|release)`), category: "GitLab", filter: filterGH}, // same shape as gh

		// ── Graphite (gt) ────────────────────────────────────────────────────
		{re: re(`^gt\s+`), category: "GitHub", filter: filterTruncate},

		// ── ls / find / tree ─────────────────────────────────────────────────
		{re: re(`^ls(\s|$)`), category: "Files", filter: filterLS},
		{re: re(`^find\s+`), category: "Files", filter: filterFind},
		{re: re(`^tree(\s|$)`), category: "Files", filter: filterTree},

		// ── cat/head/tail ────────────────────────────────────────────────────
		{re: re(`^(?:cat|head|tail)\s+`), category: "Files", filter: filterReadFile},

		// ── grep / rg ────────────────────────────────────────────────────────
		{re: re(`^(?:rg|grep)\s+`), category: "Files", filter: filterGrep},

		// ── ast-grep / sg ────────────────────────────────────────
		{re: re(`^(?:ast-grep|sg)\s+`), category: "AST", filter: filterAstGrep},

		// ── diff ─────────────────────────────────────────────────────────────
		{re: re(`^diff\s+`), category: "Files", filter: filterDiff},

		// ── npm / pnpm / npx / yarn / bun ────────────────────────────────────
		{re: re(`^pnpm\s+(?:exec|i|install|list|ls|outdated|run|run-script)`), category: "PackageManager", filter: filterNpm},
		{re: re(`^npm\s+(?:exec|run|run-script|rum|urn|x)(\s|$)`), category: "PackageManager", filter: filterNpm},
		{re: re(`^npx\s+`), category: "PackageManager", filter: filterNpmTest},
		{re: re(`^(?:yarn|bun)\s+(?:test|run|install|build)`), category: "PackageManager", filter: filterNpm},

		// ── JS tools: vitest / jest / eslint / tsc / prettier / playwright ───
		{re: re(`^(?:npx\s+)?(?:vitest|jest)(\s|$)`), category: "Tests", filter: filterVitest},
		{re: re(`^(?:npx\s+)?(?:eslint)\s+`), category: "JS", filter: filterESLint},
		{re: re(`^(?:npx\s+)?tsc(\s|$)`), category: "JS", filter: filterESLint},
		{re: re(`^(?:npx\s+)?prettier\s+`), category: "JS", filter: filterTruncate},
		{re: re(`^(?:npx\s+)?playwright\s+`), category: "Tests", filter: filterPlaywright},
		{re: re(`^(?:npx\s+)?prisma\s+`), category: "JS", filter: filterTruncate},
		{re: re(`^(?:npx\s+)?next\s+`), category: "JS", filter: filterBuildOutput},

		// ── Go ───────────────────────────────────────────────────────────────
		{re: re(`^go\s+(?:test|build|vet|run|generate|mod)`), category: "Go", filter: filterGo},
		{re: re(`^(?:golangci-lint|golangci)\s+run(\s|$)`), category: "Go", filter: filterGolangciLint},

		// ── Rust / Cargo ─────────────────────────────────────────────────────
		{re: re(`^cargo\s+(?:build|test|clippy|check|fmt|install)`), category: "Cargo", filter: filterCargo},

		// ── Python ───────────────────────────────────────────────────────────
		{re: re(`^(?:python3?\s+-m\s+)?pytest(\s|$)`), category: "Tests", filter: filterPytest},
		{re: re(`^(?:python3?\s+-m\s+)?mypy(\s|$)`), category: "Python", filter: filterPythonLint},
		{re: re(`^ruff\s+(?:check|format)`), category: "Python", filter: filterPythonLint},
		{re: re(`^(?:pip3?|uv\s+pip)\s+(?:list|outdated|install|show)`), category: "Python", filter: filterTruncate},
		{re: re(`^uv\s+(?:sync|pip\s+install)\b`), category: "Python", filter: filterBuildOutput},

		// ── Ruby ─────────────────────────────────────────────────────────────
		{re: re(`^(?:bundle\s+exec\s+)?(?:bin/)?(?:rake|rails)\s+test`), category: "Tests", filter: filterRubyTest},
		{re: re(`^(?:bundle\s+exec\s+)?rspec(\s|$)`), category: "Tests", filter: filterRubyTest},
		{re: re(`^(?:bundle\s+exec\s+)?rubocop(\s|$)`), category: "Ruby", filter: filterRuboCop},
		{re: re(`^bundle\s+(?:install|update)\b`), category: "Ruby", filter: filterBuildOutput},

		// ── .NET ─────────────────────────────────────────────────────────────
		{re: re(`^dotnet\s+build\b`), category: "DotNet", filter: filterDotnetBuild},
		{re: re(`^dotnet\s+test\b`), category: "Tests", filter: filterDotnetTest},

		// ── Cloud / Infra ─────────────────────────────────────────────────────
		{re: re(`^aws\s+`), category: "Infra", filter: filterAWS},
		{re: re(`^gcloud\b`), category: "Infra", filter: filterTruncate},
		{re: re(`^docker\s+(?:ps|images|logs|run|exec|build|compose\s+(?:ps|logs|build))`), category: "Infra", filter: filterDocker},
		{re: re(`^kubectl\s+(?:get|logs|describe|apply)`), category: "Infra", filter: filterKubectl},
		{re: re(`^helm\b`), category: "Infra", filter: filterTruncate},
		{re: re(`^terraform\s+plan`), category: "Infra", filter: filterTerraform},
		{re: re(`^tofu\s+(?:fmt|init|plan|validate)(\s|$)`), category: "Infra", filter: filterTerraform},
		{re: re(`^ansible-playbook\b`), category: "Infra", filter: filterTruncate},
		{re: re(`^psql(\s|$)`), category: "Infra", filter: filterTruncate},

		// ── Network tools ─────────────────────────────────────────────────────
		{re: re(`^curl\s+`), category: "Network", filter: filterCurl},
		{re: re(`^wget\s+`), category: "Network", filter: filterTruncate},
		{re: re(`^ping\b`), category: "Network", filter: filterPing},

		// ── Build systems ─────────────────────────────────────────────────────
		{re: re(`^make\b`), category: "Build", filter: filterMake},
		{re: re(`^mvn\s+(?:compile|package|clean|install)\b`), category: "Build", filter: filterMaven},
		{re: re(`^swift\s+(?:build|test)\b`), category: "Build", filter: filterSwift},
		{re: re(`^mix\s+(?:compile|format)(\s|$)`), category: "Build", filter: filterBuildOutput},
		{re: re(`^composer\s+(?:install|update|require)\b`), category: "Build", filter: filterBuildOutput},
		{re: re(`^poetry\s+(?:install|lock|update)\b`), category: "Build", filter: filterBuildOutput},
		{re: re(`^pio\s+run`), category: "Build", filter: filterBuildOutput},

		// ── Linters / formatters ──────────────────────────────────────────────
		{re: re(`^shellcheck\b`), category: "Lint", filter: filterPythonLint},
		{re: re(`^markdownlint\b`), category: "Lint", filter: filterPythonLint},
		{re: re(`^yamllint\b`), category: "Lint", filter: filterPythonLint},
		{re: re(`^hadolint\b`), category: "Lint", filter: filterPythonLint},
		{re: re(`^pre-commit\b`), category: "Lint", filter: filterBuildOutput},

		// ── System utilities ──────────────────────────────────────────────────
		{re: re(`^ps(\s|$)`), category: "System", filter: filterTruncate},
		{re: re(`^df(\s|$)`), category: "System", filter: filterTruncate},
		{re: re(`^du\b`), category: "System", filter: filterTruncate},
		{re: re(`^wc(\s|$)`), category: "System", filter: filterTruncate},
		{re: re(`^systemctl\s+status\b`), category: "System", filter: filterSystemctl},
		{re: re(`^brew\s+(?:install|upgrade)\b`), category: "System", filter: filterBuildOutput},
		{re: re(`^rsync\b`), category: "System", filter: filterTruncate},

		// ── Misc ──────────────────────────────────────────────────────────────
		{re: re(`^trunk\s+build`), category: "Build", filter: filterBuildOutput},
	}
}

func re(pattern string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)` + pattern)
}

// classify finds the first matching rule for cmd, or nil if none match.
func classify(cmd string) *rule {
	// Strip leading env var assignments: FOO=bar cmd ...
	bare := cmd
	for {
		parts := strings.SplitN(bare, " ", 2)
		if len(parts) < 2 {
			break
		}
		if idx := strings.Index(parts[0], "="); idx > 0 {
			bare = strings.TrimSpace(parts[1])
		} else {
			break
		}
	}

	for _, r := range rules {
		if r.re.MatchString(bare) {
			return r
		}
	}
	return nil
}
