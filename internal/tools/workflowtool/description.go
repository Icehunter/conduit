package workflowtool

import "strings"

// description is the tool prompt. It follows CC's Workflow tool text: the
// opt-in gate, the meta contract, and the script API reference the model
// writes against. ultracode flips the gate to "author one by default".
func description(ultracode bool) string {
	var sb strings.Builder
	sb.WriteString(`Execute a workflow script that orchestrates multiple subagents deterministically. Workflows run in the background — this tool returns immediately with a runId, and a <task-notification> arrives when the workflow completes. Use /workflows to watch live progress.

`)
	if ultracode {
		sb.WriteString(`ULTRACODE IS ON for this session. That opt-in is standing: author and run a workflow for every substantive task by default. Token cost is not a constraint; the goal is the most exhaustive, correct answer. For multi-phase work run several workflows in sequence (understand → design → implement → review) so you stay in the loop between them. Solo only on conversational turns or trivial mechanical edits.

`)
	} else {
		sb.WriteString(`ONLY call this tool when the user has explicitly opted into multi-agent orchestration. Workflows can spawn dozens of agents and consume a large amount of tokens; the user must request that scale, not have it inferred. Explicit opt-in means one of:
- The user included the keyword "ultracode" in their prompt.
- The user directly asked you to run a workflow or use multi-agent orchestration in their own words ("use a workflow", "fan out agents", "orchestrate this with subagents").
- The user invoked a skill or slash command whose instructions tell you to call Workflow.
- The user asked you to run a specific named or saved workflow.

For any other task — even one that would clearly benefit from parallelism — do NOT call this tool. Use the Task tool for individual subagents, or briefly describe what a workflow could do and roughly cost, and ask the user whether to run it.

`)
	}
	sb.WriteString(`Every script must begin with ` + "`export const meta = {...}`" + `: a PURE LITERAL (no variables, calls or interpolation) giving the workflow's ` + "`name`" + `, a one-line ` + "`description`" + `, optionally ` + "`whenToUse`" + ` and ` + "`phases`" + ` — one ` + "`{ title, detail? }`" + ` per phase() call, titles matched exactly. Pass the script inline via ` + "`script`" + `; every invocation persists it to a file and returns the path. To iterate, edit that file and relaunch with ` + "`{scriptPath, resumeFromRunId}`" + ` — unchanged agent() calls replay from the journal, edited/new ones run live.

Script body hooks:
- agent(prompt: string, opts?: {label?, phase?, schema?, model?, effort?, isolation?: 'worktree', agentType?}): Promise<any> — spawn a subagent. Without schema, returns its final text. With schema (a JSON Schema whose root is {type:'object', properties} and required ⊆ properties), returns the validated object. Resolves null when the subagent dies on a terminal error (filter with .filter(Boolean)). Throws synchronously on an invalid schema, the 1000-agent lifetime cap, or an exhausted budget. opts.phase assigns the call to a progress group (use it inside pipeline/parallel stages instead of relying on the global phase()). opts.isolation: 'worktree' runs the agent in its own git worktree — use ONLY when agents edit files in parallel; the worktree is auto-removed if unchanged, otherwise kept and its path logged. opts.agentType uses a named sub-agent from the registry (same names as Task's subagent_type).
- pipeline(items, stage1, stage2, ...): Promise<any[]> — run each item through all stages independently with NO barrier between stages. Each stage receives (prevResult, originalItem, index). A stage that throws or returns null drops that item to null and skips its remaining stages. This is the DEFAULT for multi-stage work.
- parallel(thunks: Array<() => Promise<any>>): Promise<any[]> — a BARRIER: awaits every thunk. A thunk that throws resolves to null; the call itself never rejects. Use only when the next step needs ALL results at once (dedup, early-exit on zero, cross-item comparison).
- phase(title): void — start a progress group for subsequent agent() calls.
- log(message): void — narrator line shown to the user. Log every cap or truncation you apply; silent truncation reads as full coverage.
- args: any — the tool's args input, verbatim (undefined if absent).
- budget: {total: number|null, spent(): number, remaining(): number} — output-token ceiling from the budgetTokens input. total is null with no budget and remaining() is then Infinity; guard loops with ` + "`while (budget.total && budget.remaining() > 50_000)`" + `. Once spent() reaches total, agent() throws.
- workflow(nameOrRef: string | {scriptPath}, args?): Promise<any> — run a saved workflow or script file inline as a child sharing this run's caps and budget. One level of nesting only.

Scripts are plain JavaScript (no TypeScript syntax, no import/require, no filesystem or Node APIs). The body runs in an async context — use await directly and return the result. Date.now(), Math.random() and argless new Date() throw (they would break resume); pass timestamps via args. Concurrent agents are capped at min(16, CPUs-2); one parallel()/pipeline() call accepts at most 4096 items.

Quality patterns: adversarial verify (N fresh-context skeptics per finding, majority vote, default to refuted); perspective-diverse verify (distinct lenses, not identical refuters); judge panel; loop-until-dry (stop after K consecutive empty rounds); multi-modal sweep; completeness critic. Always count what came back against what was sent and log dead nodes — a missing result among many otherwise reads as a complete report.`)
	return sb.String()
}
