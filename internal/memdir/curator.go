package memdir

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	curatorStateFile    = "curator-state.json"
	curatorMinHours     = 168 // 7 days
	curatorMinSessions  = 10
	curatorLockFileName = ".curator.lock"
)

// CuratorConfig holds tuning parameters.
type CuratorConfig struct {
	// MinAgeHours is the minimum hours since the last curator run before firing
	// again. Default 168 (7 days).
	MinAgeHours int
	// MinSessionCount is the minimum number of sessions since the last run.
	// Whichever gate (time or sessions) fires first triggers the curator.
	MinSessionCount int
}

var defaultCuratorConfig = CuratorConfig{
	MinAgeHours:     curatorMinHours,
	MinSessionCount: curatorMinSessions,
}

// curatorState is the JSON structure persisted to curator-state.json.
type curatorState struct {
	LastRun           string `json:"last_run"`
	SessionCountAtRun int    `json:"session_count_at_run"`
}

// ShouldRunCurator returns true if enough time or sessions have passed since
// the last curator run. State is tracked in
// ~/.conduit/projects/<slug>/curator-state.json.
func ShouldRunCurator(cwd, projectDir string) bool {
	_ = cwd // reserved for future memory-path gating; not needed for session/time gates
	return shouldRunCurator(projectDir, defaultCuratorConfig, time.Now())
}

func shouldRunCurator(projectDir string, cfg CuratorConfig, now time.Time) bool {
	// Resolve effective thresholds.
	minHours := cfg.MinAgeHours
	if minHours <= 0 {
		minHours = curatorMinHours
	}
	minSessions := cfg.MinSessionCount
	if minSessions <= 0 {
		minSessions = curatorMinSessions
	}

	// Check for an in-flight curator lock; treat locks older than 4 hours as
	// stale (crashed curator).
	lockPath := filepath.Join(projectDir, curatorLockFileName)
	if fi, err := os.Stat(lockPath); err == nil {
		if now.Sub(fi.ModTime()) < 4*time.Hour {
			return false
		}
		// Stale lock — remove it.
		_ = os.Remove(lockPath)
	}

	state, err := loadCuratorState(projectDir)
	if err != nil {
		// No state file yet: seed it with the current session count so we
		// start the clock from now rather than firing immediately.
		count := countAllSessions(projectDir)
		seedState := curatorState{
			LastRun:           now.UTC().Format(time.RFC3339),
			SessionCountAtRun: count,
		}
		_ = saveCuratorState(projectDir, seedState)
		return false
	}

	// Time gate.
	if state.LastRun != "" {
		last, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(state.LastRun))
		if parseErr == nil {
			hoursSince := now.Sub(last).Hours()
			if hoursSince >= float64(minHours) {
				return true
			}
		}
	}

	// Session gate: if enough new sessions have accumulated since last run, fire.
	currentCount := countAllSessions(projectDir)
	sessionsSinceRun := currentCount - state.SessionCountAtRun
	return sessionsSinceRun >= minSessions
}

// RunCurator spawns a one-shot restricted background agent with the curator
// prompt. runAgent is the same callback type used by dream.go.
func RunCurator(ctx context.Context, cwd, projectDir string, runAgent func(ctx context.Context, prompt string) (string, error)) error {
	// Acquire lock.
	lockPath := filepath.Join(projectDir, curatorLockFileName)
	if err := os.WriteFile(lockPath, []byte(time.Now().Format(time.RFC3339)), 0o644); err != nil {
		return fmt.Errorf("curator: acquire lock: %w", err)
	}
	defer func() {
		_ = os.Remove(lockPath)
		count := countAllSessions(projectDir)
		_ = saveCuratorState(projectDir, curatorState{
			LastRun:           time.Now().UTC().Format(time.RFC3339),
			SessionCountAtRun: count,
		})
	}()

	prompt := buildCuratorPrompt(cwd, projectDir)
	_, err := runAgent(ctx, prompt)
	return err
}

// buildCuratorPrompt returns the weekly maintenance prompt for the curator agent.
func buildCuratorPrompt(cwd, projectDir string) string {
	memDir := Path(cwd)
	memFile := filepath.Join(memDir, EntrypointName)
	return fmt.Sprintf(`You are performing a weekly maintenance pass on this project's memory and skills.

## Memory maintenance

Memory file: %s
Memory directory: %s

Read MEMORY.md, then consolidate and prune it in place:
- Merge duplicate or near-duplicate entries into one
- Remove entries that are now obviously stale or contradicted by later entries
- Keep entries concise — one fact per line
- Do not add new facts, only consolidate

Use the Read and Write tools to update memory files directly. MEMORY.md is an index — each entry should be one line under ~150 characters.

## Skill maintenance

Use the SkillManage tool (actions: list, view, update) to review skills:
- List all available skills with action="list"
- View each skill body with action="view" to check accuracy and relevance
- Archive skills that are clearly obsolete by rewriting their body to start with "# ARCHIVED:" using action="update"
- Merge skill pairs that address the same workflow by combining their content into one and archiving the other
- Keep skill bodies under 30 lines where possible

Be conservative: when in doubt, keep rather than delete. Prefer merging into an existing skill over creating a new one.

Project directory (session transcripts, for context): %s

Return a brief summary of what you consolidated, updated, or pruned. If nothing changed, say so.`,
		memFile, memDir, projectDir)
}

// loadCuratorState reads the curator state from projectDir/curator-state.json.
func loadCuratorState(projectDir string) (curatorState, error) {
	path := filepath.Join(projectDir, curatorStateFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return curatorState{}, fmt.Errorf("curator: read state: %w", err)
	}
	var s curatorState
	if err := json.Unmarshal(data, &s); err != nil {
		return curatorState{}, fmt.Errorf("curator: parse state: %w", err)
	}
	return s, nil
}

// saveCuratorState writes the curator state to projectDir/curator-state.json.
func saveCuratorState(projectDir string, s curatorState) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("curator: marshal state: %w", err)
	}
	path := filepath.Join(projectDir, curatorStateFile)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("curator: write state: %w", err)
	}
	return nil
}

// countAllSessions counts all JSONL session files in projectDir regardless of
// modification time (used for the session-count gate).
func countAllSessions(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			count++
		}
	}
	return count
}
