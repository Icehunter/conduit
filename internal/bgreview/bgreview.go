// Package bgreview manages periodic background memory and skill review forks.
//
// After each end_turn the Reviewer tracks a turn counter and fires the
// appropriate background review when its nudge interval is reached. Reviews
// run asynchronously (fire-and-forget via goroutines); errors are logged but
// not returned to the caller.
//
// Two review types are supported:
//
//   - Memory review — fires every MemoryNudgeInterval end_turns. Runs a
//     restricted sub-agent that reads and updates the project memory files
//     (using Read/Write/Grep tools from the parent registry).
//
//   - Skill review — fires every SkillNudgeInterval end_turns. Runs a
//     restricted sub-agent that can list, view, create, and update skill files
//     via the SkillManage tool.
//
// The runAgent callback is provided by the caller (typically mainrepl.go) and
// should call lp.RunSubAgentTyped (or similar) with the appropriate tool
// restriction.
package bgreview

import (
	"context"
	"log"
	"sync"
)

const (
	defaultMemoryInterval = 5
	defaultSkillInterval  = 7
)

// Config holds nudge intervals for background reviews.
type Config struct {
	// MemoryNudgeInterval runs the memory review every N end_turn cycles.
	// Default (0) uses 5.
	MemoryNudgeInterval int
	// SkillNudgeInterval runs the skill review every N end_turn cycles.
	// Default (0) uses 7.
	SkillNudgeInterval int
}

// Reviewer tracks turn counts and fires background reviews on schedule.
//
// Safe for concurrent use — OnEndTurn may be called from the agent's
// event-dispatch goroutine.
type Reviewer struct {
	cfg Config
	mu  sync.Mutex
	// Counters are guarded by mu.
	turnsSinceMemory int
	turnsSinceSkill  int

	// memoryInflight and skillInflight are single-flight guards so a fast
	// chain of end_turns doesn't queue multiple concurrent reviews.
	memoryInflight bool
	skillInflight  bool

	// runMemory runs the memory review sub-agent with the given prompt.
	// The tools slice contains the allowed tool names.
	runMemory func(ctx context.Context, prompt string, tools []string) (string, error)
	// runSkill runs the skill review sub-agent.
	runSkill func(ctx context.Context, prompt string, tools []string) (string, error)

	cwd string
}

// New constructs a Reviewer.
//
// cfg controls nudge intervals; zero values fall back to defaults (5 and 7).
// cwd is the working directory (used to name memory paths in prompts).
// runAgent is a restricted background runner — it should call
// lp.RunSubAgentTyped (or equivalent) with the provided tool allowlist.
// Both runMemory and runSkill are set to runAgent; callers that want separate
// implementations can use NewSplit.
func New(cfg Config, cwd string, runAgent func(ctx context.Context, prompt string, tools []string) (string, error)) *Reviewer {
	return NewSplit(cfg, cwd, runAgent, runAgent)
}

// NewSplit constructs a Reviewer with separate callbacks for memory and skill
// reviews. This lets the caller wire different tool registries (e.g., one with
// Read/Write for memory and one with SkillManage for skills).
func NewSplit(
	cfg Config,
	cwd string,
	runMemory func(ctx context.Context, prompt string, tools []string) (string, error),
	runSkill func(ctx context.Context, prompt string, tools []string) (string, error),
) *Reviewer {
	if cfg.MemoryNudgeInterval <= 0 {
		cfg.MemoryNudgeInterval = defaultMemoryInterval
	}
	if cfg.SkillNudgeInterval <= 0 {
		cfg.SkillNudgeInterval = defaultSkillInterval
	}
	return &Reviewer{
		cfg:       cfg,
		cwd:       cwd,
		runMemory: runMemory,
		runSkill:  runSkill,
	}
}

// OnEndTurn should be called after each assistant end_turn. It increments the
// internal counters and fires the appropriate background review if the nudge
// interval has been reached. Reviews are fire-and-forget goroutines; errors
// are logged but not returned.
func (r *Reviewer) OnEndTurn(ctx context.Context) {
	r.mu.Lock()
	r.turnsSinceMemory++
	r.turnsSinceSkill++

	fireMemory := !r.memoryInflight && r.turnsSinceMemory >= r.cfg.MemoryNudgeInterval
	fireSkill := !r.skillInflight && r.turnsSinceSkill >= r.cfg.SkillNudgeInterval

	if fireMemory {
		r.memoryInflight = true
		r.turnsSinceMemory = 0
	}
	if fireSkill {
		r.skillInflight = true
		r.turnsSinceSkill = 0
	}
	r.mu.Unlock()

	if fireMemory {
		go func() {
			defer func() {
				r.mu.Lock()
				r.memoryInflight = false
				r.mu.Unlock()
			}()
			// Background context so the review isn't cancelled when the
			// parent turn context is done — the user's next turn may start
			// before the review goroutine runs.
			bgCtx := context.Background()
			prompt := memoryReviewPrompt(r.cwd)
			tools := []string{"Read", "Write", "Glob", "Grep"}
			if _, err := r.runMemory(bgCtx, prompt, tools); err != nil {
				log.Printf("bgreview: memory review failed (cwd=%s): %v", r.cwd, err)
			}
		}()
	}

	if fireSkill {
		go func() {
			defer func() {
				r.mu.Lock()
				r.skillInflight = false
				r.mu.Unlock()
			}()
			bgCtx := context.Background()
			prompt := skillReviewPrompt()
			tools := []string{"SkillManage"}
			if _, err := r.runSkill(bgCtx, prompt, tools); err != nil {
				log.Printf("bgreview: skill review failed (cwd=%s): %v", r.cwd, err)
			}
		}()
	}
}

// memoryReviewPrompt returns the memory review prompt for the background agent.
func memoryReviewPrompt(cwd string) string {
	_ = cwd // available for future path interpolation
	return `Review this session's conversation and update the project memory file if anything important should be saved.

You have access to the Read, Write, Glob, and Grep tools. Check what's currently in memory, then decide if anything from this session is worth saving — new facts about the project, user preferences, decisions made, or patterns discovered.

Be conservative: only save what's genuinely non-obvious and would help future sessions. Do not duplicate what's already there.`
}

// skillReviewPrompt returns the skill review prompt for the background agent.
func skillReviewPrompt() string {
	return `Review this session and consider whether any reusable workflow should be captured or improved as a skill.

## Step 1: Gap detection
First, list all available skills with SkillManage action="list". Did this session require a capability that NO existing skill covers? If yes, and a clear reusable pattern emerged, you should create a new skill.

## Step 2: Choose scope deliberately
For every create or promote operation, choose scope carefully:
- Use scope="project" ONLY when the skill is specific to THIS repo (its particular file layout, build commands, config files, or project conventions). Future-you in a different repo would NOT benefit from it.
- Use scope="global-conduit" for general skills — workflows, debugging patterns, tool usage patterns, communication patterns — that would help across ANY project. When unsure, prefer global-conduit.
- If you spot an existing PROJECT-scoped skill that is actually general, use action="promote" to move it to global-conduit.

## Step 3: Decision hierarchy (in order)
1. PATCH/UPDATE a skill that was used or observed this session if you noticed an improvement
2. UPDATE an existing umbrella skill if this session adds a useful case or refinement
3. CREATE a new skill only when nothing covers this class of work
4. PROMOTE a project-scoped skill to global-conduit if it turned out to be general

Be active: most sessions produce at least one useful skill update or creation. A pass that does nothing is a missed learning opportunity — but only when there is genuinely something to capture.

## What to capture
- Treat user corrections and expressions of frustration as FIRST-CLASS skill signals. If the user had to redirect you, encode the lesson as a guardrail or step in the relevant skill.
- Reusable approaches: multi-step workflows, debugging patterns, API usage patterns, tool sequences.
- Fixes and solutions: capture THE FIX, not the failure. "When X fails, do Y" is useful. "X is broken" is harmful.

## What NOT to capture — these HARDEN INTO REFUSALS the agent cites against itself
- Environment-dependent failures ("the build broke because of a missing tool") — those are transient
- Negative capability claims ("tool X doesn't support Y") unless you verified it authoritatively
- One-off task narratives ("I helped the user refactor function Foo") — not reusable
- Transient errors, network failures, flaky test results

## Skill structure
Skills should be class-level umbrella documents (rich SKILL.md + optional references/ files), not per-session notes. Each skill covers a TYPE of work, not a single instance.`
}
