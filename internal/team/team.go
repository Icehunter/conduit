// Package team manages agent team state for conduit's in-process agent teams
// feature. Unlike upstream Claude Code (which uses separate OS processes and
// tmux), conduit teammates are goroutine-level nested Loops sharing the same
// process.
//
// This file holds the activation flag and session-naming helpers. The runtime
// (member registry, mailbox, spawn) will be added in the full implementation.
package team

import (
	"strings"
	"sync/atomic"
)

var active atomic.Bool

// IsActive reports whether agent teams are enabled for this session.
// Toggled at startup from conduit.json "agentTeams" and live via SetActive.
func IsActive() bool {
	return active.Load()
}

// SetActive enables or disables agent teams. Called at startup from the loaded
// ConduitConfig and immediately when the /config panel toggles the setting.
func SetActive(on bool) {
	active.Store(on)
}

// TeammateTools is the allowed-tool list for teammate loops. It mirrors
// coordinator.WorkerTools and adds SendMessage (always injected via ExtraTools
// in SpawnTeammate so the sender identity is baked correctly).
var TeammateTools = []string{
	"Bash", "Edit", "EnterPlanMode", "EnterWorktree",
	"ExitPlanMode", "ExitWorktree", "Glob", "Grep",
	"ListMcpResources", "NotebookEdit", "Read", "ReadMcpResource",
	"REPL", "SendMessage", "SkillTool", "Sleep",
	"TaskCreate", "TaskGet", "TaskList", "TaskOutput", "TaskStop", "TaskUpdate",
	"TodoWrite", "ToolSearch", "WebFetch", "WebSearch", "Write",
}

// LeadSystemPrompt returns the system-prompt block injected when the lead agent
// is running in team mode. Describes how to orchestrate teammates via SendMessage
// and the plan-approval / shutdown protocols.
func LeadSystemPrompt() string {
	return `You are the lead agent in an agent team. Your role is to:
- Assign work to teammates by spawning them via the Task tool
- Communicate with running teammates via SendMessage
- Approve or reject teammate plans (kind: plan-approve / plan-reject)
- Request a graceful shutdown of a teammate when their work is done (kind: shutdown-request)
- Synthesize results and report progress to the user

Teammates report back to you as <team-message>, <team-plan>, <team-idle>, <team-completion>, <team-shutdown-approve>, and <team-shutdown-reject> tags injected into your context. Treat these as internal signals — do not echo or acknowledge them verbatim to the user.

When a teammate sends a plan for approval, evaluate it against the task goals and respond with SendMessage (kind: plan-approve or plan-reject with feedback). Never fabricate teammate results.`
}

// UserContext returns a brief per-turn reminder of active teammates, injected
// as a volatile system block so the lead always sees the current roster.
// Returns "" when no teammates are registered.
func UserContext() string {
	if !IsActive() {
		return ""
	}
	names := Default.Names()
	var teammates []string
	for _, n := range names {
		if n != ReservedLeadName {
			teammates = append(teammates, n)
		}
	}
	if len(teammates) == 0 {
		return ""
	}
	return "Active teammates: " + strings.Join(teammates, ", ") +
		". Use SendMessage to communicate with them."
}

// SessionName returns the deterministic team name derived from a session ID:
// "team-" + the first 8 characters of the session ID. Replaces the removed
// TeamCreate/TeamDelete tools (CC divergence: session-scoped, no create/delete).
func SessionName(sessionID string) string {
	if len(sessionID) > 8 {
		return "team-" + sessionID[:8]
	}
	return "team-" + sessionID
}
