// Package skills provides built-in skills that ship with conduit.
// These mirror the non-KAIROS bundled skills in src/skills/bundled/.
package skills

import "github.com/icehunter/conduit/internal/tools/skilltool"

// Bundled returns the list of built-in skills. Each entry matches the
// skilltool.Command format so it can be merged into a SkillLoader.
func Bundled() []skilltool.Command {
	return []skilltool.Command{
		{
			QualifiedName: "simplify",
			Description:   "Review changed code for reuse, quality, and efficiency, then fix any issues found.",
			Body:          simplifyPrompt,
		},
		{
			QualifiedName: "remember",
			Description:   "Review auto-memory entries and propose promotions to CLAUDE.md or CLAUDE.local.md.",
			Body:          rememberPrompt,
		},
		{
			QualifiedName: "verify",
			Description:   "Run the project's verification suite (build, lint, tests) and surface any failures.",
			Body:          "Run the project's verification commands (e.g. `make verify` or equivalent). Report failures with context. Fix trivial issues automatically; escalate non-trivial ones.",
		},
		{
			QualifiedName: "keybindings-help",
			Description:   "Use when the user wants to customize keyboard shortcuts, rebind keys, add chord bindings, or modify ~/.claude/keybindings.json.",
			Body:          "Help the user customize keybindings. Read ~/.claude/keybindings.json, explain the current bindings, and apply the requested changes following conduit keybinding syntax.",
		},
		{
			QualifiedName: "update-config",
			Description:   "Use to configure the conduit harness via settings.json — hooks, permissions, env vars, and automated behaviors.",
			Body:          "Inspect and modify .claude/settings.json or .claude/settings.local.json to apply the requested configuration change (hook, permission, env var, etc). Validate JSON before writing.",
		},
		{
			QualifiedName: "batch",
			Description:   "Research and plan a large-scale mechanical change, then execute it across many files using parallel agents.",
			Body:          "Analyse the requested change, decompose it into independent units, then execute each unit in parallel via sub-agents. Summarise results when all units complete.",
		},
		{
			QualifiedName: "skill-creator",
			Description:   "Capture the current session's repeatable process into a new skill file for future reuse.",
			Body:          "Review the conversation to identify the repeatable workflow. Write a concise SKILL.md capturing the steps, then save it to .claude/skills/<name>/SKILL.md (project) or ~/.claude/skills/<name>/SKILL.md (personal) as the user prefers.",
		},
	}
}

const simplifyPrompt = `# Simplify: Code Review and Cleanup

Review all changed files for reuse, quality, and efficiency. Fix any issues found.

## Phase 1: Identify Changes

Run ` + "`git diff`" + ` (or ` + "`git diff HEAD`" + ` if there are staged changes) to see what changed. If there are no git changes, review the most recently modified files that the user mentioned or that you edited earlier in this conversation.

## Phase 2: Launch Three Review Agents in Parallel

Use the Task tool to launch all three agents concurrently in a single message. Pass each agent the full diff so it has the complete context.

### Agent 1: Code Reuse Review

For each change:

1. **Search for existing utilities and helpers** that could replace newly written code.
2. **Flag any new function that duplicates existing functionality.**
3. **Flag any inline logic that could use an existing utility** — hand-rolled string manipulation, manual path handling, etc.

### Agent 2: Code Quality Review

Review the same changes for hacky patterns:

1. **Redundant state** duplicating existing state
2. **Parameter sprawl** adding params instead of restructuring
3. **Copy-paste with slight variation** that should be unified
4. **Leaky abstractions** exposing internal details
5. **Unnecessary comments** explaining WHAT instead of WHY

### Agent 3: Efficiency Review

Review the same changes for efficiency:

1. **Unnecessary work**: redundant computations, repeated file reads
2. **Missed concurrency**: sequential operations that could run in parallel
3. **Hot-path bloat**: new blocking work added to startup or hot paths
4. **Memory**: unbounded data structures, missing cleanup, listener leaks

## Phase 3: Fix Issues

Wait for all three agents to complete. Aggregate their findings and fix each issue directly. If a finding is a false positive, note it and move on.

When done, briefly summarize what was fixed (or confirm the code was already clean).
`

const rememberPrompt = `# Memory Review

## Goal
Review the user's memory landscape and produce a clear report of proposed changes, grouped by action type. Do NOT apply changes — present proposals for user approval.

## Steps

### 1. Gather all memory layers
Read CLAUDE.md and CLAUDE.local.md from the project root (if they exist). Your auto-memory content is already in your system prompt — review it there.

### 2. Classify each auto-memory entry

| Destination | What belongs there |
|---|---|
| **CLAUDE.md** | Project conventions all contributors should follow |
| **CLAUDE.local.md** | Personal instructions specific to this user |
| **Stay in auto-memory** | Working notes, temporary context |

### 3. Identify cleanup opportunities
- **Duplicates**: entries already captured in CLAUDE.md or CLAUDE.local.md
- **Outdated**: entries contradicted by newer entries
- **Conflicts**: contradictions between layers

### 4. Present the report

Output a structured report grouped by:
1. **Promotions** — entries to move, with destination and rationale
2. **Cleanup** — duplicates, outdated, conflicts
3. **Ambiguous** — entries needing user input
4. **No action needed**

## Rules
- Present ALL proposals before making any changes
- Do NOT modify files without explicit user approval
`
