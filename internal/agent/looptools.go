package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/hooks"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/shellsafe"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/tools/skillmanagetool"
)

// toolTask holds the pre-checked state for one tool ready to execute.
type toolTask struct {
	block    api.ContentBlock
	rawInput json.RawMessage
	tool     tool.Tool // nil if tool not found or permission denied
	denied   bool
	denyMsg  string
	// note is appended to the tool result, e.g. the implement-guard fallback
	// notice.
	note string
}

// toolResult holds the outcome of one tool execution.
type toolResult struct {
	idx      int
	text     string
	isError  bool
	stopTurn bool
	// elapsed is the tool's Execute wall time; zero for denied or missing
	// tools, which never ran.
	elapsed time.Duration
}

// executeTools runs all tool_use blocks in the assistant message sequentially
// and returns the tool_result content blocks for the follow-up user message.
//
// For each tool:
//  1. Permission gate check (if configured).
//  2. PreToolUse hooks (if configured).
//  3. Tool execution.
//  4. PostToolUse hooks (if configured).
//
// invalid maps the ids of tool_use blocks whose arguments did not parse to the
// error text for their tool_result. Those calls are answered without running
// the tool, a permission prompt, or PreToolUse hooks (PostToolUse still fires
// on the error result), so the model can re-issue them.
func (l *Loop) executeTools(ctx context.Context, assistantBlocks []api.ContentBlock, invalid map[string]string, handler func(LoopEvent)) ([]api.ContentBlock, []api.ToolDuration, bool, error) { //nolint:unparam
	// Tools resolve relative paths and run shell commands under the loop's
	// working directory so a sub-agent in a worktree stays in its worktree.
	l.mu.RLock()
	if l.cfg.Cwd != "" {
		ctx = tool.WithCwd(ctx, l.cfg.Cwd)
	}
	loopCwd := l.cfg.Cwd
	// The live session id, so tools never rely on one captured at startup.
	ctx = tool.WithSessionID(ctx, l.cfg.SessionID)
	l.mu.RUnlock()
	// A sub-agent's tools keep per-agent state (the REPL's interpreter) apart
	// from the parent's.
	ctx = tool.WithScope(ctx, l.wireAgentID())
	redact := l.redactEnabled()
	// Phase 1: collect tool_use blocks and run interactive checks serially
	// (hooks + permission gate may prompt the user — must be sequential).
	// server_tool_use blocks are intentionally skipped — they are executed
	// server-side and their results are injected by the API automatically.
	var tasks []toolTask
	for _, block := range assistantBlocks {
		if block.Type != "tool_use" {
			continue
		}
		rawInput, _ := json.Marshal(block.Input)
		if rawInput == nil {
			rawInput = json.RawMessage("{}")
		}
		permInput := toolPermissionInput(block.Name, block.Input, loopCwd)

		task := toolTask{block: block, rawInput: rawInput}

		if msg, bad := invalid[block.ID]; bad {
			task.denied = true
			task.denyMsg = msg
			tasks = append(tasks, task)
			continue
		}

		// Resolve the tool early so we can check IsReadOnly before the permission gate.
		t, ok := l.reg.Lookup(block.Name)
		if !ok {
			task.denied = true
			task.denyMsg = fmt.Sprintf("Tool %q not found", block.Name)
			tasks = append(tasks, task)
			continue
		}
		task.tool = t

		// setInput swaps in a rewritten input: the model's arguments after
		// coercion, or a PreToolUse hook's updatedInput. The assistant message
		// in the history keeps what the model emitted.
		setInput := func(in map[string]any) {
			block.Input = in
			rawInput, _ = json.Marshal(in)
			permInput = toolPermissionInput(block.Name, in, loopCwd)
			task.block, task.rawInput = block, rawInput
		}
		// Repair stringly-typed arguments ("42", "true", JSON in a string)
		// against the tool's schema before anything inspects them.
		if fixed, changed := tool.CoerceInput(t.InputSchema(), block.Input); changed {
			setInput(fixed)
		}

		// --- PreToolUse hooks ---
		hookApproved, hookAsk := false, false
		if l.cfg.Hooks != nil && len(l.cfg.Hooks.PreToolUse) > 0 {
			inputMap := block.Input
			if inputMap == nil {
				inputMap = make(map[string]any)
				_ = json.Unmarshal(rawInput, &inputMap)
			}
			r := hooks.RunPreToolUse(ctx, l.cfg.Hooks.PreToolUse, l.cfg.SessionID, block.Name, inputMap)
			if r.Blocked {
				reason := r.Reason
				if reason == "" {
					reason = "blocked by PreToolUse hook"
				}
				task.denied = true
				task.denyMsg = "Tool blocked by hook: " + reason
				tasks = append(tasks, task)
				continue
			}
			if r.UpdatedInput != nil {
				fixed, _ := tool.CoerceInput(t.InputSchema(), r.UpdatedInput)
				if missing := missingRequired(t.InputSchema(), fixed); len(missing) > 0 {
					task.denied = true
					task.denyMsg = invalidUpdatedInputMsg(block.Name, missing)
					tasks = append(tasks, task)
					continue
				}
				setInput(fixed)
			}
			// An "ask" outranks an "allow" from another hook.
			hookAsk = r.Ask
			hookApproved = r.Approved && !r.Ask
		}

		if redact && carriesUnknownMask(block.Name, block.Input, l.cfg.Cwd) {
			task.denied = true
			task.denyMsg = maskedEditMsg
			tasks = append(tasks, task)
			continue
		}

		// --- Plan-mode file-dump block ---
		// In plan mode, bash commands that dump file contents (cat/head/tail/
		// less/more/bat) are blocked and steered toward the Read tool — even
		// when chained behind cd/&&/; so `cd dir && cat file` cannot bypass
		// the Read-tool-only policy the way a bare `cat file` would be caught.
		if block.Name == "Bash" && l.cfg.Gate != nil && l.cfg.Gate.Mode() == permissions.ModePlan {
			if shellsafe.ContainsFileDump(permInput) {
				task.denied = true
				task.denyMsg = "In plan mode, use the Read tool to read file contents instead of bash (cat/head/tail/less/more). Use Grep or Glob for searching."
				tasks = append(tasks, task)
				continue
			}
		}

		// --- Implement-role guard ---
		// When another provider owns code changes, the main model may not
		// write files the implementer has not touched. Checked ahead of the
		// gate so bypassPermissions cannot skip it.
		if msg := l.implGuardDenial(block.Name, block.Input, loopCwd); msg != "" {
			task.denied = true
			task.denyMsg = msg
			tasks = append(tasks, task)
			continue
		}
		task.note = l.implGuardFallbackNote(block.Name, block.Input, loopCwd)

		// --- Permission gate check ---
		// Read-only tools are auto-approved in default and plan modes — they cannot
		// modify state, so prompting for every FileRead/Glob/Grep call is noise.
		// A hook "ask" prompts even for a call the rules would let through.
		//
		// Plan and council modes never prompt: a hook "ask" there is denied
		// outright instead of upgrading the call to a prompt.
		planLike := l.cfg.Gate != nil && (l.cfg.Gate.Mode() == permissions.ModePlan || l.cfg.Gate.Mode() == permissions.ModeCouncil)
		if hookAsk && planLike {
			task.denied = true
			task.denyMsg = planModeDenyMsg(block.Name)
			tasks = append(tasks, task)
			continue
		}
		readOnly := t.IsReadOnly(rawInput) && !hookAsk
		if l.cfg.Gate != nil && !readOnly {
			decision := l.cfg.Gate.Check(block.Name, permInput)
			if hookAsk && decision == permissions.DecisionAllow {
				decision = permissions.DecisionAsk
			}
			switch decision {
			case permissions.DecisionDeny:
				task.denied = true
				task.denyMsg = "Tool denied by permission rules"
				if planLike {
					task.denyMsg = planModeDenyMsg(block.Name)
				}
				tasks = append(tasks, task)
				continue
			case permissions.DecisionAsk:
				l.mu.RLock()
				askPermission := l.cfg.AskPermission
				askSubAgent := l.cfg.AskSubAgentPermission
				l.mu.RUnlock()
				// A PreToolUse hook approval never skips the prompt for a
				// protected path (git config, shell rc, settings, ...).
				protected := l.cfg.Gate.IsProtectedWrite(block.Name, permInput)
				// ...nor does it skip an explicit ask rule.
				if !hookApproved || protected || l.cfg.Gate.MatchesAskRule(block.Name, permInput) {
					switch {
					case askPermission != nil:
						// This loop has its own direct interactive prompt (the
						// root loop, or anything else explicitly wired).
						allow, alwaysAllow := askPermission(ctx, block.Name, permInput)
						if !allow {
							task.denied = true
							task.denyMsg = fmt.Sprintf("%s denied by user", block.Name)
							tasks = append(tasks, task)
							continue
						}
						if alwaysAllow && !protected {
							rule := permissions.SuggestRule(block.Name, permInput)
							l.cfg.Gate.AllowForSession(rule)
							if l.cfg.Cwd != "" {
								_ = permissions.PersistAllow(rule, l.cfg.Cwd)
							}
						}
					case askSubAgent != nil:
						// No direct prompt of our own (we're a sub-agent), but an
						// ancestor has one — bubble the decision instead of
						// silently allowing it.
						switch askSubAgent(ctx, l.subAgentLabel, block.Name, permInput) {
						case SubAgentPermDeny:
							task.denied = true
							task.denyMsg = fmt.Sprintf("%s denied by user", block.Name)
							tasks = append(tasks, task)
							continue
						case SubAgentPermSwitchToAuto:
							// Clamped by the gate's own escalation ceiling — see
							// permissions.Gate.SetMode / Clone. Allows this call
							// and every subsequent one for the rest of this
							// sub-agent's run (Check short-circuits once in
							// bypassPermissions mode).
							l.cfg.Gate.SetMode(permissions.ModeBypassPermissions)
						case SubAgentPermAllowOnce:
							// Allow just this call.
						}
					default:
						// No interactive session anywhere (e.g. headless/print
						// mode, or a background reviewer). Allow writes without
						// prompting — unchanged pre-existing behavior.
					}
				}
			}
		} else if l.cfg.Gate != nil && readOnly {
			// Still check the deny list — a user can explicitly deny reads.
			if l.cfg.Gate.Check(block.Name, permInput) == permissions.DecisionDeny {
				task.denied = true
				task.denyMsg = "Tool denied by permission rules"
				tasks = append(tasks, task)
				continue
			}
		}
		tasks = append(tasks, task)
	}

	if len(tasks) == 0 {
		return nil, nil, false, nil
	}

	// Phase 2: execute tools. Run concurrency-safe tools in parallel (bounded
	// pool of maxConcurrentTools); non-safe or denied tools emit inline.
	taskResults := make([]toolResult, len(tasks))

	// Separate into parallel-eligible and must-be-serial.
	type workItem struct {
		idx  int
		task toolTask
	}
	var parallel, serial []workItem
	for i, task := range tasks {
		if task.denied || task.tool == nil {
			serial = append(serial, workItem{i, task})
			continue
		}
		if task.tool.IsConcurrencySafe(task.rawInput) {
			parallel = append(parallel, workItem{i, task})
		} else {
			serial = append(serial, workItem{i, task})
		}
	}

	// Run parallel tasks with a bounded worker pool.
	if len(parallel) > 0 {
		sem := make(chan struct{}, maxConcurrentTools)
		var wg sync.WaitGroup
		// Add the full count before the loop so wg.Add is never called while
		// other goroutines are decrementing — a stdlib requirement when the
		// counter can reach zero mid-loop.
		wg.Add(len(parallel))
		for _, wi := range parallel {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				// Fill the slot so Phase 3 sees a result for every task.
				taskResults[wi.idx] = toolResult{idx: wi.idx, text: "cancelled", isError: true}
				wg.Done()
				continue
			}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				start := time.Now()
				res, err := wi.task.tool.Execute(ctx, wi.task.rawInput)
				elapsed := time.Since(start)
				if err != nil {
					taskResults[wi.idx] = toolResult{idx: wi.idx, text: fmt.Sprintf("tool error: %v", err), isError: true, elapsed: elapsed}
					return
				}
				if !res.IsError {
					l.notifyFileAccess(wi.task.block.Name, wi.task.block.Input)
				}
				text := ""
				if len(res.Content) > 0 {
					text = res.Content[0].Text
				}
				taskResults[wi.idx] = toolResult{idx: wi.idx, text: text, isError: res.IsError, stopTurn: res.StopTurn, elapsed: elapsed}
			}()
		}
		wg.Wait()
	}

	// Run serial tasks (denied, not-found, or not concurrency-safe).
	for _, wi := range serial {
		if wi.task.denied || wi.task.tool == nil {
			taskResults[wi.idx] = toolResult{idx: wi.idx, text: wi.task.denyMsg, isError: true}
			continue
		}
		start := time.Now()
		res, err := wi.task.tool.Execute(ctx, wi.task.rawInput)
		elapsed := time.Since(start)
		if err != nil {
			taskResults[wi.idx] = toolResult{idx: wi.idx, text: fmt.Sprintf("tool error: %v", err), isError: true, elapsed: elapsed}
			continue
		}
		if !res.IsError {
			l.notifyFileAccess(wi.task.block.Name, wi.task.block.Input)
		}
		text := ""
		if len(res.Content) > 0 {
			text = res.Content[0].Text
		}
		taskResults[wi.idx] = toolResult{idx: wi.idx, text: text, isError: res.IsError, stopTurn: res.StopTurn, elapsed: elapsed}
	}
	durations := toolDurations(tasks, taskResults)

	// Phase 3: assemble results in original order + run PostToolUse hooks.
	var results []api.ContentBlock
	stopTurn := false
	for i, task := range tasks {
		tr := taskResults[i]
		if tr.stopTurn {
			stopTurn = true
		}
		// PostToolUse fires unconditionally — error results included — so hook
		// authors that log or route tool activity see every outcome. Matches
		// TS reference (runPostToolUseHooks fires without an isError guard).
		if l.cfg.Hooks != nil && len(l.cfg.Hooks.PostToolUse) > 0 {
			hooks.RunPostToolUse(ctx, l.cfg.Hooks.PostToolUse, l.cfg.SessionID, task.block.Name, tr.text)
		}
		// Hooks run locally and see the raw result; the model and the
		// transcript get it with secrets masked.
		if redact && !returnsFileBytes(task.block.Name, task.block.Input) {
			tr.text = redactResult(tr.text)
		}
		if task.note != "" {
			tr.text += "\n\n" + task.note
		}
		handler(LoopEvent{
			Type:       EventToolResult,
			ToolID:     task.block.ID,
			ToolName:   task.block.Name,
			ResultText: tr.text,
			IsError:    tr.isError,
		})
		results = append(results, api.ContentBlock{
			Type:          "tool_result",
			ToolUseID:     task.block.ID,
			IsError:       tr.isError,
			ResultContent: tr.text,
		})
	}
	return results, durations, stopTurn, nil
}

// planModeDenyMsg is the denial shown when plan or council mode refuses a call.
func planModeDenyMsg(name string) string {
	return name + " not run: plan mode is read-only. Use read-only tools or commands, or call ExitPlanMode."
}

// toolPermissionInput extracts the meaningful string to match against permission
// rules for a given tool. Rules like Bash(git log *) match the shell command,
// not the raw JSON input blob.
func toolPermissionInput(toolName string, input map[string]any, cwd string) string {
	switch toolName {
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			return cmd
		}
	case "Edit", "HashEdit":
		if p, ok := input["file_path"].(string); ok {
			return p
		}
	case "NotebookEdit":
		if p, ok := input["notebook_path"].(string); ok {
			return p
		}
	case "Write":
		if p, ok := input["file_path"].(string); ok {
			return p
		}
	case "Read":
		if p, ok := input["file_path"].(string); ok {
			return p
		}
	case "WebFetch":
		if u, ok := input["url"].(string); ok {
			return u
		}
	case "AstGrep":
		return astGrepWriteTarget(input, cwd)
	case "SkillManage":
		action, _ := input["action"].(string)
		name, _ := input["name"].(string)
		scope, _ := input["scope"].(string)
		return skillmanagetool.TargetPath(cwdOrProcess(cwd), action, name, scope)
	}
	return ""
}

// cwdOrProcess is the loop's working directory, or the process's when the loop
// has none configured.
func cwdOrProcess(cwd string) string {
	if cwd != "" {
		return cwd
	}
	cwd, _ = os.Getwd()
	return cwd
}

// astGrepWriteTarget returns the permission input for an AstGrep call: "" for
// a pure search, otherwise the path a rewrite touches. A rewrite may name
// several paths and one string stands for them all, so a protected path wins
// over the others; with none, the first path (or "." for the default).
func astGrepWriteTarget(input map[string]any, cwd string) string {
	if rw, _ := input["rewrite"].(string); rw == "" {
		return ""
	}
	if dry, ok := input["dry_run"].(bool); !ok || dry {
		return "" // dry_run defaults to true and previews without writing
	}
	var paths []string
	if raw, ok := input["paths"].([]any); ok {
		for _, p := range raw {
			if s, ok := p.(string); ok && s != "" {
				paths = append(paths, s)
			}
		}
	}
	if len(paths) == 0 {
		return "."
	}
	cwd = cwdOrProcess(cwd)
	for _, p := range paths {
		if permissions.IsProtectedPath(cwd, p) {
			return p
		}
	}
	return paths[0]
}

// notifyFileAccess fires cfg.OnFileAccess for file-mutating and file-reading tools.
func (l *Loop) notifyFileAccess(toolName string, input map[string]any) {
	if l.cfg.OnFileAccess == nil {
		return
	}
	switch toolName {
	case "Read":
		if p, ok := input["file_path"].(string); ok {
			l.cfg.OnFileAccess("read", p)
		}
	case "Edit", "Write":
		if p, ok := input["file_path"].(string); ok {
			l.cfg.OnFileAccess("write", p)
		}
	}
}

// deferredCountSetter is the subset of toolsearchtool.Tool that buildToolDefs
// needs to report the deferred count. Using a local interface avoids an import
// cycle between the agent and toolsearchtool packages.
type deferredCountSetter interface {
	SetDeferredCount(n int)
}

// apiDescriber is an optional tool extension that returns a description
// tailored for the API request schema. Always-on MCP tools use this to send
// a first-sentence truncation instead of the full multi-paragraph description,
// saving tokens on every turn while keeping the full text available via
// ToolSearch. Tools that do not implement this interface use Description().
type apiDescriber interface {
	APIDescription() string
}

// toolAPIDescription returns the description to use in the API schema. If the
// tool implements apiDescriber it uses the truncated form; otherwise it falls
// back to the standard Description().
func toolAPIDescription(t tool.Tool) string {
	if d, ok := t.(apiDescriber); ok {
		return d.APIDescription()
	}
	return t.Description()
}

// buildToolDefs converts the registry into the API tool definitions array.
// When ToolSearch is registered, tools that implement tool.DeferrableChecker
// and return true from Deferrable() are excluded from the returned slice —
// the model uses ToolSearch to discover them on demand. The excluded count is
// reported back to ToolSearch via SetDeferredCount so it can surface an
// accurate total_deferred_tools value.
func buildToolDefs(reg *tool.Registry) []api.ToolDef {
	return buildToolDefsFor(reg, "")
}

// weakEditModelRe matches model families that misread the anchored HashEdit
// grammar (Kimi, MiMo, MiniMax, DeepSeek, StepFun, codex-spark, GLM-5.3
// flash; the list follows oh-my-pi's edit-mode resolver). "step" is matched
// only as a model name ("step-3", "stepfun/..."), not inside other words.
var weakEditModelRe = regexp.MustCompile(`kimi|moonshot|mimo|minimax|deepseek|stepfun|(^|[/:])step[-_]\d|codex-spark|glm-5\.3.*flash`)

// modelPrefersPlainEdit reports whether model should be offered the plain
// string-replace Edit tool instead of HashEdit.
func modelPrefersPlainEdit(model string) bool {
	return weakEditModelRe.MatchString(strings.ToLower(model))
}

// buildToolDefsFor is buildToolDefs for a specific model: models that handle
// the HashEdit grammar poorly are not shown it, so they use Edit.
func buildToolDefsFor(reg *tool.Registry, model string) []api.ToolDef {
	all := reg.All()
	if modelPrefersPlainEdit(model) {
		all = slices.DeleteFunc(all, func(t tool.Tool) bool { return t.Name() == "HashEdit" })
	}
	if len(all) == 0 {
		return nil
	}

	// Check if ToolSearch is present. If so, deferrable tools are excluded.
	var countSetter deferredCountSetter
	if ts, ok := reg.Lookup("ToolSearch"); ok {
		if s, ok := ts.(deferredCountSetter); ok {
			countSetter = s
		}
	}

	// Sort alphabetically so the tools list is byte-identical across runs and
	// the cache_control breakpoint always lands on the same tool regardless of
	// registry iteration order.
	slices.SortFunc(all, func(a, b tool.Tool) int {
		return strings.Compare(a.Name(), b.Name())
	})

	defs := make([]api.ToolDef, 0, len(all))
	var deferred int
	for _, t := range all {
		if countSetter != nil && tool.IsDeferrable(t) {
			deferred++
			continue
		}
		var schema map[string]any
		_ = json.Unmarshal(t.InputSchema(), &schema)
		defs = append(defs, api.ToolDef{
			Name:        t.Name(),
			Description: toolAPIDescription(t),
			InputSchema: schema,
		})
	}

	if countSetter != nil {
		countSetter.SetDeferredCount(deferred)
	}

	// Mark the last tool definition as a prompt-cache breakpoint so the
	// system+tools prefix is cached as a stable unit. Only applied when the
	// list is non-empty and ToolSearch is not mid-turn (deferral count is
	// already accounted for above, so the slice is stable at this point).
	if len(defs) > 0 {
		defs[len(defs)-1].CacheControl = &api.CacheControl{Type: "ephemeral", TTL: "1h", Scope: "global"}
	}

	return defs
}

// readStateResetter is implemented by tools that hold what the model has read
// (the Read tool's tracker behind the read-before-write guard).
type readStateResetter interface{ ResetReadState() }

// resetReadState forgets the files the model has read. The model can no longer
// be assumed to remember them once the conversation is replaced (/clear,
// /rewind, a session switch); compaction deliberately keeps them, since the
// tracker's content hash already proves the file still matches. The tracker is
// one instance shared by the main loop, sub-agents and teammates, so a reset
// also makes live background agents re-Read before their next edit; the guard
// error tells them to, so it costs a Read but never a bad write.
func (l *Loop) resetReadState() {
	if l.reg == nil {
		return
	}
	for _, t := range l.reg.All() {
		if r, ok := t.(readStateResetter); ok {
			r.ResetReadState()
		}
	}
}
