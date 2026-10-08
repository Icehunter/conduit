package memdir

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/skillusage"
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
	// StaleDays, ArchiveDays and PurgeDays tune the skill lifecycle: days
	// since last use before a skill goes stale and is archived, and days
	// after archiving before the archived copy is deleted. Zero values use
	// the skillusage defaults (14 / 30 / 90).
	StaleDays   int
	ArchiveDays int
	PurgeDays   int
}

var defaultCuratorConfig = CuratorConfig{
	MinAgeHours:     curatorMinHours,
	MinSessionCount: curatorMinSessions,
	StaleDays:       skillusage.DefaultStaleDays,
	ArchiveDays:     skillusage.DefaultArchiveDays,
	PurgeDays:       skillusage.DefaultPurgeDays,
}

// ErrCuratorBusy is returned by RunCurator when another process holds the
// curator lock.
var ErrCuratorBusy = errors.New("curator: another curator run holds the lock")

// tryCuratorLock takes the exclusive, cross-process curator lock. The lock is
// a kernel advisory lock on the lock file, so it is released automatically if
// the holder crashes: a leftover lock file never blocks a later run.
func tryCuratorLock(projectDir string) (release func(), err error) {
	fl := flock.New(filepath.Join(projectDir, curatorLockFileName))
	locked, err := fl.TryLock()
	if err != nil {
		return nil, fmt.Errorf("curator: acquire lock: %w", err)
	}
	if !locked {
		return nil, ErrCuratorBusy
	}
	return func() { _ = fl.Unlock() }, nil
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

	// Another process mid-run holds the lock; probe and release immediately.
	release, err := tryCuratorLock(projectDir)
	if err != nil {
		return false
	}
	release()

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
	return runCurator(ctx, cwd, projectDir, defaultCuratorConfig, time.Now(), runAgent)
}

func runCurator(ctx context.Context, cwd, projectDir string, cfg CuratorConfig, now time.Time, runAgent func(ctx context.Context, prompt string) (string, error)) error {
	release, err := tryCuratorLock(projectDir)
	if err != nil {
		return err
	}
	defer func() {
		release()
		count := countAllSessions(projectDir)
		_ = saveCuratorState(projectDir, curatorState{
			LastRun:           time.Now().UTC().Format(time.RFC3339),
			SessionCountAtRun: count,
		})
	}()

	// Snapshot skills before mutating. Archiving and purging move or delete
	// whole skill directories, so without a restore point they are skipped.
	snapshotOK := true
	if _, err := skillusage.Snapshot(); err != nil {
		snapshotOK = false
		log.Printf("curator: pre-run snapshot failed; skipping archive and purge this run: %v", err)
	} else {
		skillusage.PruneBackups(5)
	}

	// Apply deterministic lifecycle transitions (no LLM required).
	staleDays, archiveDays, purgeDays := cfg.StaleDays, cfg.ArchiveDays, cfg.PurgeDays
	if staleDays <= 0 {
		staleDays = skillusage.DefaultStaleDays
	}
	if archiveDays <= 0 {
		archiveDays = skillusage.DefaultArchiveDays
	}
	if purgeDays <= 0 {
		purgeDays = skillusage.DefaultPurgeDays
	}
	if snapshotOK {
		for _, tr := range skillusage.ApplyTransitions(now, staleDays, archiveDays) {
			if tr.To == "archived" {
				archiveAgentSkill(tr.Name, now)
			}
		}
		for _, name := range skillusage.PurgeArchived(now, purgeDays) {
			purgeArchivedSkill(name)
		}
	}

	agentSkills := skillusage.AgentCreatedNames()
	prompt := buildCuratorPrompt(cwd, projectDir, agentSkills)
	_, err = runAgent(skillusage.WithActor(ctx, skillusage.ActorCurator), prompt)
	return err
}

// archiveAgentSkill moves an agent-created skill that has been marked archived
// by ApplyTransitions from its live directory to ~/.conduit/skills/.archive/<name>/.
// Errors are logged but not returned — the LLM curator pass continues regardless.
func archiveAgentSkill(name string, now time.Time) {
	conduitSkillsDir := filepath.Join(settings.ConduitDir(), "skills")
	src := filepath.Join(conduitSkillsDir, name)
	archiveDir := filepath.Join(conduitSkillsDir, ".archive")

	fi, err := os.Lstat(src)
	if err != nil {
		return // already gone or never had a dir
	}
	if !fi.IsDir() {
		// A symlinked skill is the user's own checkout, not ours to move.
		log.Printf("curator: archive: %s is not a plain directory; leaving it in place", src)
		return
	}
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		log.Printf("curator: archive: mkdir %s: %v", archiveDir, err)
		return
	}
	// Never overwrite an earlier archive: a second archive of the same name
	// gets a timestamp suffix.
	dst := filepath.Join(archiveDir, name)
	if _, err := os.Lstat(dst); err == nil {
		dst = filepath.Join(archiveDir, name+"."+now.UTC().Format("20060102T150405"))
		for i := 2; ; i++ {
			if _, err := os.Lstat(dst); err != nil {
				break
			}
			dst = filepath.Join(archiveDir, fmt.Sprintf("%s.%s-%d", name, now.UTC().Format("20060102T150405"), i))
		}
	}
	if err := os.Rename(src, dst); err != nil {
		log.Printf("curator: archive: rename %s → %s: %v", src, dst, err)
		return
	}
	skillusage.RecordInfo(skillusage.ActorCurator, skillusage.OpArchive, name, dst, "moved from "+src)
}

// purgeArchivedSkill deletes the archived copies of name once its retention
// window has passed. It removes only directories the ledger recorded as
// archived by the curator, directly under ~/.conduit/skills/.archive/, so a
// directory that merely happens to carry the skill's name is never touched.
func purgeArchivedSkill(name string) {
	archiveDir := filepath.Join(settings.ConduitDir(), "skills", ".archive")
	for _, e := range skillusage.LedgerList(name, 0) {
		if e.Op != skillusage.OpArchive || e.Path == "" || filepath.Dir(filepath.Clean(e.Path)) != archiveDir {
			continue
		}
		fi, err := os.Lstat(e.Path)
		if err != nil || !fi.IsDir() {
			continue
		}
		if err := os.RemoveAll(e.Path); err != nil {
			log.Printf("curator: purge: remove %s: %v", e.Path, err)
			continue
		}
		skillusage.RecordInfo(skillusage.ActorCurator, skillusage.OpPurge, name, e.Path, "archive retention window elapsed")
	}
}

// buildCuratorPrompt returns the weekly maintenance prompt for the curator agent.
// agentSkills is the list of agent-created skill names to include in the prompt.
func buildCuratorPrompt(cwd, projectDir string, agentSkills []string) string {
	memDir := Path(cwd)
	memFile := filepath.Join(memDir, EntrypointName)

	var agentSkillsList string
	if len(agentSkills) == 0 {
		agentSkillsList = "none"
	} else {
		var sb strings.Builder
		for _, s := range agentSkills {
			sb.WriteString("- ")
			sb.WriteString(s)
			sb.WriteString("\n")
		}
		agentSkillsList = strings.TrimRight(sb.String(), "\n")
	}

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

You are performing a CONSOLIDATION pass on agent-created skills. You must NEVER edit, archive, or delete user-authored or pinned skills.

Agent-created skills available for review:
%s

Use SkillManage (actions: list, view, update) to review each of these skills:
- View each skill body to check accuracy and relevance
- Archive skills that are clearly obsolete by rewriting their body to start with "# ARCHIVED:" using action="update"
- Merge skill pairs that address the same workflow by combining content into one and archiving the other
- Promote any project-scoped skill that you judge to be general-purpose: use action="promote" to move it to global-conduit scope
- Keep skill bodies under 30 lines where possible

IMPORTANT: Only operate on the agent-created skills listed above. Do not modify any other skills.

Be conservative: when in doubt, keep rather than delete.

Project directory (session transcripts, for context): %s

Return a brief summary of what you consolidated, updated, or pruned. If nothing changed, say so.`,
		memFile, memDir, agentSkillsList, projectDir)
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
