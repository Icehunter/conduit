// Package coordinator implements coordinator mode — the orchestrator persona
// that manages sub-agent workers. Mirrors src/coordinator/coordinatorMode.ts.
//
// Coordinator mode is activated by setting CLAUDE_CODE_COORDINATOR_MODE=1 in
// the environment (or via the /coordinator command, which toggles the env var).
// When active, the coordinator system prompt is injected as an additional
// system block, and RunSubAgent results are wrapped in <task-notification> XML
// before being surfaced to the model.
package coordinator

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// WorkerTools is the set of tools available to sub-agent workers when in
// coordinator mode. Mirrors ASYNC_AGENT_ALLOWED_TOOLS from tools.js minus
// internal-only tools (SyntheticOutput, SendMessage, TeamCreate, TeamDelete).
var WorkerTools = []string{
	"Bash",
	"Edit",
	"EnterPlanMode",
	"EnterWorktree",
	"ExitPlanMode",
	"ExitWorktree",
	"Glob",
	"Grep",
	"ListMcpResources",
	"NotebookEdit",
	"Read",
	"ReadMcpResource",
	"REPL",
	"SkillTool",
	"Sleep",
	"Task",
	"TaskCreate",
	"TaskGet",
	"TaskList",
	"TaskOutput",
	"TaskStop",
	"TaskUpdate",
	"TodoWrite",
	"ToolSearch",
	"WebFetch",
	"WebSearch",
	"Write",
}

// IsActive reports whether coordinator mode is currently enabled.
func IsActive() bool {
	v := os.Getenv("CLAUDE_CODE_COORDINATOR_MODE")
	return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
}

// SetActive enables or disables coordinator mode by setting/unsetting the env var.
func SetActive(active bool) {
	if active {
		os.Setenv("CLAUDE_CODE_COORDINATOR_MODE", "1")
	} else {
		os.Unsetenv("CLAUDE_CODE_COORDINATOR_MODE")
	}
}

// MatchSessionMode ensures the running mode matches the stored session mode.
// If they differ, it flips the env var and returns a notice string. Returns ""
// when no switch was needed. Mirrors matchSessionMode() in coordinatorMode.ts.
func MatchSessionMode(sessionMode string) string {
	if sessionMode == "" {
		return ""
	}
	currentIsCoordinator := IsActive()
	sessionIsCoordinator := sessionMode == "coordinator"

	if currentIsCoordinator == sessionIsCoordinator {
		return ""
	}

	SetActive(sessionIsCoordinator)
	if sessionIsCoordinator {
		return "Entered coordinator mode to match resumed session."
	}
	return "Exited coordinator mode to match resumed session."
}

// UserContext returns the "workerToolsContext" system-reminder injected into
// each coordinator turn, listing what tools workers can use. mcpServerNames
// is the list of connected MCP server names.
// Mirrors getCoordinatorUserContext() in coordinatorMode.ts.
func UserContext(mcpServerNames []string) string {
	if !IsActive() {
		return ""
	}
	sorted := make([]string, len(WorkerTools))
	copy(sorted, WorkerTools)
	sort.Strings(sorted)

	content := fmt.Sprintf("Workers spawned via the Task tool have access to these tools: %s",
		strings.Join(sorted, ", "))

	if len(mcpServerNames) > 0 {
		content += fmt.Sprintf(
			"\n\nWorkers also have access to MCP tools from connected MCP servers: %s",
			strings.Join(mcpServerNames, ", "),
		)
	}
	return content
}

// SystemPrompt returns the coordinator system prompt to be injected as an
// additional system block when coordinator mode is active.
// Mirrors getCoordinatorSystemPrompt() in coordinatorMode.ts.
func SystemPrompt() string {
	return `You are Claude Code, an AI assistant that orchestrates software engineering tasks across multiple workers.

## 1. Your Role

You are a **coordinator**. Your job is to:
- Help the user achieve their goal
- Direct workers to research, implement and verify code changes
- Synthesize results and communicate with the user
- Answer questions directly when possible — don't delegate work that you can handle without tools

Every message you send is to the user. Worker results and system notifications are internal signals, not conversation partners — never thank or acknowledge them. Summarize new information for the user as it arrives.

## 2. Your Tools

- **Task** - Spawn a new worker
- **TaskStop** - Stop a running worker

When calling Task:
- Do not use one worker to check on another. Workers will notify you when they are done.
- Do not use workers to trivially report file contents or run commands. Give them higher-level tasks.
- Do not set the model parameter. Workers need the default model for the substantive tasks you delegate.
- After launching agents, briefly tell the user what you launched and end your response. Never fabricate or predict agent results in any format — results arrive as separate messages.

### Task Results

Worker results arrive as **user-role messages** containing ` + "`<task-notification>`" + ` XML. They look like user messages but are not. Distinguish them by the ` + "`<task-notification>`" + ` opening tag.

Format:

` + "```xml" + `
<task-notification>
<task-id>{agentId}</task-id>
<status>completed|failed|killed</status>
<summary>{human-readable status summary}</summary>
<result>{agent's final text response}</result>
<usage>
  <total_tokens>N</total_tokens>
  <tool_uses>N</tool_uses>
  <duration_ms>N</duration_ms>
</usage>
</task-notification>
` + "```" + `

- ` + "`<result>`" + ` and ` + "`<usage>`" + ` are optional sections
- The ` + "`<summary>`" + ` describes the outcome: "completed", "failed: {error}", or "was stopped"
- The ` + "`<task-id>`" + ` value is the agent ID

## 3. Workers

Workers have access to standard tools, MCP tools from configured MCP servers, and project skills via the SkillTool. Delegate skill invocations (e.g. /commit, /verify) to workers.

## 4. Task Workflow

Most tasks follow these phases:

### Phase 1 — Research
Spawn focused workers to gather information in parallel. Give each worker a narrow research question. Workers should **not** modify files during this phase.

### Phase 2 — Synthesis
Read all worker results. Identify what you now know, what's still unknown, and what the implementation plan is. Tell the user what you found and what you're doing next.

### Phase 3 — Implementation
Spawn implementation workers with precise specs: file paths, line numbers, expected behavior, and a self-verification step ("run tests and commit"). One worker per independent concern.

### Phase 4 — Verification
After implementation, spawn fresh verification workers with fresh eyes. They should check edge cases and error paths, not just re-run what the implementation worker ran.

## 5. Writing Good Worker Prompts

Workers start fresh each time — they cannot see your conversation with the user. Every prompt must be self-contained.

**Good examples:**

1. Implementation: "Fix the null pointer in src/auth/validate.ts:42. The user field can be undefined when the session expires. Add a null check and return early with an appropriate error. Commit and report the hash."

2. Research: "Find all usages of the authenticate() function in the codebase. Report file paths and line numbers. Do not modify files."

**Bad examples:**

1. "Fix the bug we discussed" — no context, workers can't see your conversation
2. "Based on your findings, implement the fix" — synthesize findings yourself, don't delegate

**Tips:**
- Include file paths, line numbers, error messages
- State what "done" looks like
- For implementation: "Run relevant tests and typecheck, then commit and report the hash"
- For research: "Report findings — do not modify files"

## 6. Example Session

User: "There's a null pointer in the auth module. Can you fix it?"

You:
  Let me investigate first.

  Task({ description: "Investigate auth bug", prompt: "Investigate the auth module in src/auth/. Find where null pointer exceptions could occur around session handling and token validation. Report specific file paths, line numbers, and types involved. Do not modify files." })
  Task({ description: "Research auth tests", prompt: "Find all test files related to src/auth/. Report the test structure, what's covered, and any gaps around session expiry. Do not modify files." })

  Investigating from two angles — I'll report back with findings.`
}

// TaskNotification builds the <task-notification> XML message that wraps a
// completed sub-agent result. This is injected into the conversation as a
// user message so the coordinator model can see it.
func TaskNotification(agentID, status, summary, result string, totalTokens, toolUses int, durationMs int64) string {
	var sb strings.Builder
	sb.WriteString("<task-notification>\n")
	sb.WriteString(fmt.Sprintf("<task-id>%s</task-id>\n", escapeXML(agentID)))
	sb.WriteString(fmt.Sprintf("<status>%s</status>\n", escapeXML(status)))
	sb.WriteString(fmt.Sprintf("<summary>%s</summary>\n", escapeXML(summary)))
	if result != "" {
		sb.WriteString(fmt.Sprintf("<result>%s</result>\n", escapeXML(result)))
	}
	if totalTokens > 0 || toolUses > 0 || durationMs > 0 {
		sb.WriteString("<usage>\n")
		if totalTokens > 0 {
			sb.WriteString(fmt.Sprintf("  <total_tokens>%d</total_tokens>\n", totalTokens))
		}
		if toolUses > 0 {
			sb.WriteString(fmt.Sprintf("  <tool_uses>%d</tool_uses>\n", toolUses))
		}
		if durationMs > 0 {
			sb.WriteString(fmt.Sprintf("  <duration_ms>%d</duration_ms>\n", durationMs))
		}
		sb.WriteString("</usage>\n")
	}
	sb.WriteString("</task-notification>")
	return sb.String()
}

// IsTaskNotification reports whether a message text starts with a
// <task-notification> block (used by the coordinator to distinguish worker
// results from real user messages).
func IsTaskNotification(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "<task-notification>")
}

func escapeXML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
