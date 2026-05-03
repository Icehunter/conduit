package memdir

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// RunExtract spawns a memory-extraction sub-agent over the most recent N
// model-visible messages (user + assistant). Mirrors the post-Stop pattern
// from src/services/extractMemories/extractMemories.ts but is invoked
// manually via /memory extract rather than firing on every Stop event —
// avoids the surprise per-turn cost of CC's auto-extract.
//
// The runAgent closure runs the sub-agent with the constructed prompt; the
// sub-agent writes new/updated files into the auto-memory directory using
// its restricted tool set (read-only + memory-dir-only writes).
//
// recentSummary is a brief textual rendering of the recent conversation
// that the sub-agent will analyze. Conduit doesn't have CC's full Message
// type tree, so callers pass a pre-rendered summary string.
func RunExtract(ctx context.Context, cwd, recentSummary string, runAgent func(context.Context, string) (string, error)) error {
	memDir := Path(cwd)
	files, _ := ScanMemories(cwd)
	manifest := FormatMemoryList(files)
	prompt := buildExtractPrompt(memDir, recentSummary, manifest)
	_, err := runAgent(ctx, prompt)
	return err
}

// buildExtractPrompt mirrors buildExtractAutoOnlyPrompt() in CC, condensed
// for conduit's single auto-memory dir (no team-mem branch). Anchored on
// the recent transcript supplied by the caller and the existing memory
// listing so the sub-agent updates rather than duplicates.
func buildExtractPrompt(memoryRoot, recentSummary, manifest string) string {
	manifestSection := ""
	if strings.TrimSpace(manifest) != "" {
		manifestSection = fmt.Sprintf(`

## Existing memory files

%s

Check this list before writing — update an existing file rather than creating a duplicate.
`, manifest)
	}

	return fmt.Sprintf(`# Memory extraction

You are now acting as the memory extraction subagent. Analyze the recent conversation below and update your persistent memory systems with anything durably useful.

Memory directory: %[1]s
This directory already exists — write to it directly with the Write tool. The MEMORY.md index is at %[2]s.

Available tools: %[3]s, %[4]s, %[5]s, read-only Bash, and FileWrite/FileEdit for paths inside the memory directory only.

You have a limited turn budget. Edit requires a prior Read of the same file, so the efficient strategy is: turn 1 — issue all Reads in parallel for every file you might update; turn 2 — issue all Writes/Edits in parallel. Do not interleave reads and writes.

Use ONLY content from the recent conversation below. Do not investigate or verify content further — no grepping source files, no reading code to confirm a pattern exists, no git commands.%[6]s

---

## Recent conversation

%[7]s

---

## What to save

Refer to your system prompt's auto-memory section for the four-type taxonomy (user / feedback / project / reference) and the file format. Focus on:

- **Surprises** — things that a future session would not infer from current code or git history
- **Corrections** — guidance the user just gave you about how to work
- **Confirmations** — non-obvious approaches the user explicitly approved
- **Facts that change quickly** — current goals, deadlines, who's doing what

Skip:
- Code patterns, file paths, git history (derivable from the repo)
- Debugging recipes (the fix is in the code)
- One-shot task details (the conversation already captured them)

## How to save

Write each new memory to its own file (e.g., `+"`feedback_testing.md`"+`) with frontmatter (name/description/type), then add a one-line entry to MEMORY.md pointing at it. MEMORY.md is an index, not a memory — never paste content into it directly.

Update existing files in place rather than creating duplicates. If you find a memory that's now wrong, fix or remove it.

Return a brief summary of what you wrote, updated, or pruned.`,
		memoryRoot, filepath.Join(memoryRoot, EntrypointName),
		"FileRead", "Grep", "Glob",
		manifestSection, recentSummary)
}
