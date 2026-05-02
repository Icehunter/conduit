package memdir

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	dreamMinHours    = 24
	dreamMinSessions = 5
	lockFileName     = ".dream.lock"
	lastDreamFile    = ".last_dream"
)

// DreamConfig controls when auto-dream fires.
type DreamConfig struct {
	MinHours    float64
	MinSessions int
}

var defaultDreamConfig = DreamConfig{
	MinHours:    dreamMinHours,
	MinSessions: dreamMinSessions,
}

// ShouldDream checks whether the auto-dream consolidation gate is open.
// Mirrors the gate checks in src/services/autoDream/autoDream.ts:
//  1. Time: hours since last consolidation >= MinHours
//  2. Sessions: transcript count newer than last consolidation >= MinSessions
//  3. Lock: no other dream in progress
func ShouldDream(cwd string, sessionDir string) bool {
	return shouldDream(cwd, sessionDir, defaultDreamConfig, time.Now())
}

func shouldDream(cwd, sessionDir string, cfg DreamConfig, now time.Time) bool {
	memDir := Path(cwd)

	// Check lock — no parallel dreams.
	lockPath := filepath.Join(memDir, lockFileName)
	if fi, err := os.Stat(lockPath); err == nil {
		// Lock older than 2h is stale (crashed dream).
		if now.Sub(fi.ModTime()) < 2*time.Hour {
			return false
		}
		// Stale lock — remove it.
		_ = os.Remove(lockPath)
	}

	// Time gate.
	lastDream := readLastDream(memDir)
	hoursSince := now.Sub(lastDream).Hours()
	if hoursSince < cfg.MinHours {
		return false
	}

	// Session gate — count session files newer than lastDream.
	sessions := countSessionsSince(sessionDir, lastDream)
	return sessions >= cfg.MinSessions
}

// RunDream runs the memory consolidation sub-agent ("dream").
// Mirrors runForkedAgent(buildConsolidationPrompt(...)) in autoDream.ts.
// The runAgent closure is Loop.RunSubAgent from the agent package.
func RunDream(ctx context.Context, cwd, sessionDir string, runAgent func(context.Context, string) (string, error)) error {
	memDir := Path(cwd)

	// Acquire lock.
	lockPath := filepath.Join(memDir, lockFileName)
	if err := os.WriteFile(lockPath, []byte(time.Now().Format(time.RFC3339)), 0o644); err != nil {
		return fmt.Errorf("dream: acquire lock: %w", err)
	}
	defer func() {
		_ = os.Remove(lockPath)
		_ = writeLastDream(memDir, time.Now())
	}()

	prompt := buildConsolidationPrompt(memDir, sessionDir)
	_, err := runAgent(ctx, prompt)
	return err
}

// buildConsolidationPrompt returns the dream consolidation prompt.
// Mirrors buildConsolidationPrompt() in src/services/autoDream/consolidationPrompt.ts.
func buildConsolidationPrompt(memoryRoot, transcriptDir string) string {
	return fmt.Sprintf(`# Dream: Memory Consolidation

You are performing a dream — a reflective pass over your memory files. Synthesize what you've learned recently into durable, well-organized memories so that future sessions can orient quickly.

Memory directory: %[1]s
This directory already exists — write to it directly with the Write tool.

Session transcripts: %[2]s (large JSONL files — grep narrowly, don't read whole files)

---

## Phase 1 — Orient

- List the memory directory to see what already exists
- Read %[3]s to understand the current index
- Skim existing topic files so you improve them rather than creating duplicates

## Phase 2 — Gather recent signal

Look for new information worth persisting. Sources in rough priority order:

1. **Existing memories that drifted** — facts that contradict something you see in the codebase now
2. **Transcript search** — if you need specific context, grep the JSONL transcripts for narrow terms

Don't exhaustively read transcripts. Look only for things you already suspect matter.

## Phase 3 — Consolidate

For each thing worth remembering, write or update a memory file at the top level of the memory directory. Use the memory file format and type conventions from your system prompt's auto-memory section — it's the source of truth for what to save, how to structure it, and what NOT to save.

Focus on:
- Merging new signal into existing topic files rather than creating near-duplicates
- Converting relative dates ("yesterday", "last week") to absolute dates so they remain interpretable after time passes
- Deleting contradicted facts — if today's investigation disproves an old memory, fix it at the source

## Phase 4 — Prune and index

Update %[3]s so it stays under %[4]d lines AND under ~25KB. It's an **index**, not a dump — each entry should be one line under ~150 characters: ` + "`- [Title](file.md) — one-line hook`" + `. Never write memory content directly into it.

- Remove pointers to memories that are now stale, wrong, or superseded
- Add pointers to newly important memories
- Resolve contradictions — if two files disagree, fix the wrong one

---

Return a brief summary of what you consolidated, updated, or pruned. If nothing changed (memories are already tight), say so.`,
		memoryRoot, transcriptDir, EntrypointName, MaxLines)
}

func readLastDream(memDir string) time.Time {
	data, err := os.ReadFile(filepath.Join(memDir, lastDreamFile))
	if err != nil {
		return time.Time{} // zero time — always gate-open on first run
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}
	}
	return t
}

func writeLastDream(memDir string, t time.Time) error {
	return os.WriteFile(filepath.Join(memDir, lastDreamFile), []byte(t.UTC().Format(time.RFC3339)+"\n"), 0o644)
}

// countSessionsSince counts JSONL session files in dir modified after since.
func countSessionsSince(dir string, since time.Time) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		if fi.ModTime().After(since) {
			count++
		}
	}
	return count
}
