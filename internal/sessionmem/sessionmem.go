// Package sessionmem implements per-session memory: a markdown file at
// <projectDir>/<sessionID>/session-memory/summary.md that a sub-agent
// updates periodically with notes about the current conversation.
//
// On --continue / /resume the file is loaded and appended to the loop's
// system blocks so the new turn picks up where the prior one left off.
//
// Mirrors src/services/SessionMemory/sessionMemory.ts (GrowthBook-gated
// in CC; always on here, throttled to every UpdateEveryNTurns end_turns).
package sessionmem

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// UpdateEveryNTurns is the throttle for OnEndTurn updates. CC keys off
// token-window growth; we use a turn count for simplicity. 3 matches
// CC's DEFAULT_SESSION_MEMORY_CONFIG.toolCallsBetweenUpdates.
const UpdateEveryNTurns = 3

const summaryFile = "summary.md"

// initialTemplate is written when the file is first created so the sub-agent
// has a structure to fill in. Mirrors the loadSessionMemoryTemplate output.
const initialTemplate = `# Session Memory

A running summary of this conversation. Updated periodically by a sub-agent.

## Goal
_What we're trying to accomplish in this session._

## Progress
_What's been done so far._

## Open questions
_Things that aren't yet decided._

## Next steps
_What to do next._
`

// Dir returns the session-memory directory for a (cwd, sessionID) pair.
// Layout: <homeProjects>/<sanitized-cwd>/<sessionID>/session-memory/
// where <homeProjects> matches session.ProjectDir's layout.
func Dir(projectDir, sessionID string) string {
	return filepath.Join(projectDir, sessionID, "session-memory")
}

// Path returns the path to the summary.md for a session.
func Path(projectDir, sessionID string) string {
	return filepath.Join(Dir(projectDir, sessionID), summaryFile)
}

// Load reads the session summary file. Returns "" if the file doesn't exist.
func Load(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// EnsureFile creates the session-memory directory and seeds summary.md
// with the initial template if it doesn't exist yet. Returns the file
// path. Idempotent.
func EnsureFile(projectDir, sessionID string) (string, error) {
	dir := Dir(projectDir, sessionID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("sessionmem: mkdir: %w", err)
	}
	path := filepath.Join(dir, summaryFile)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte(initialTemplate), 0o600); err != nil {
			return "", fmt.Errorf("sessionmem: seed: %w", err)
		}
	}
	return path, nil
}

// RunUpdate fires the session-memory sub-agent. The sub-agent receives the
// current summary plus a transcript of the recent conversation and is
// asked to produce an updated summary, which it writes back via FileWrite.
//
// Mirrors src/services/SessionMemory/sessionMemory.ts updateSessionMemory().
func RunUpdate(ctx context.Context, summaryPath, recentTranscript string, runAgent func(context.Context, string) (string, error)) error {
	current, err := Load(summaryPath)
	if err != nil {
		return err
	}
	prompt := buildUpdatePrompt(summaryPath, current, recentTranscript)
	_, err = runAgent(ctx, prompt)
	return err
}

func buildUpdatePrompt(summaryPath, currentSummary, recentTranscript string) string {
	if strings.TrimSpace(currentSummary) == "" {
		currentSummary = "_(empty — initialize from the recent conversation below)_"
	}
	return fmt.Sprintf(`# Session memory update

You are now acting as the session-memory subagent. Update %[1]s with a fresh, accurate summary of the conversation so far.

The summary file is your scratchpad — it should let a future "you" pick up the thread of this session quickly. Keep it under ~200 lines. Prefer bullets over paragraphs.

## Current summary

%[2]s

## Recent conversation

%[3]s

---

Read %[1]s, then write an updated version using FileWrite/FileEdit. Maintain (or improve) the section structure (Goal / Progress / Open questions / Next steps). Drop bullets that are no longer true; add bullets that capture new decisions, blockers, or context.

Do not invent facts not in the conversation above. Do not investigate the codebase to confirm anything — work from this transcript only.

Return a one-line summary of what you updated.`,
		summaryPath, currentSummary, recentTranscript)
}
