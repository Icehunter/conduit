package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/hooks"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/tool"
)

// toolTask holds the pre-checked state for one tool ready to execute.
type toolTask struct {
	block    api.ContentBlock
	rawInput json.RawMessage
	tool     tool.Tool // nil if tool not found or permission denied
	denied   bool
	denyMsg  string
}

// toolResult holds the outcome of one tool execution.
type toolResult struct {
	idx     int
	text    string
	isError bool
}

// executeTools runs all tool_use blocks in the assistant message sequentially
// and returns the tool_result content blocks for the follow-up user message.
//
// For each tool:
//  1. Permission gate check (if configured).
//  2. PreToolUse hooks (if configured).
//  3. Tool execution.
//  4. PostToolUse hooks (if configured).
func (l *Loop) executeTools(ctx context.Context, assistantBlocks []api.ContentBlock, handler func(LoopEvent)) ([]api.ContentBlock, error) { //nolint:unparam
	// Phase 1: collect tool_use blocks and run interactive checks serially
	// (hooks + permission gate may prompt the user — must be sequential).
	var tasks []toolTask
	for _, block := range assistantBlocks {
		if block.Type != "tool_use" {
			continue
		}
		rawInput, _ := json.Marshal(block.Input)
		if rawInput == nil {
			rawInput = json.RawMessage("{}")
		}
		permInput := toolPermissionInput(block.Name, block.Input)

		task := toolTask{block: block, rawInput: rawInput}

		// Resolve the tool early so we can check IsReadOnly before the permission gate.
		t, ok := l.reg.Lookup(block.Name)
		if !ok {
			task.denied = true
			task.denyMsg = fmt.Sprintf("Tool %q not found", block.Name)
			tasks = append(tasks, task)
			continue
		}
		task.tool = t

		// --- PreToolUse hooks ---
		hookApproved := false
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
			hookApproved = r.Approved
		}

		// --- Permission gate check ---
		// Read-only tools are auto-approved in default and plan modes — they cannot
		// modify state, so prompting for every FileRead/Glob/Grep call is noise.
		readOnly := t.IsReadOnly(rawInput)
		if l.cfg.Gate != nil && !readOnly {
			decision := l.cfg.Gate.Check(block.Name, permInput)
			switch decision {
			case permissions.DecisionDeny:
				task.denied = true
				task.denyMsg = "Tool denied by permission rules"
				tasks = append(tasks, task)
				continue
			case permissions.DecisionAsk:
				l.mu.RLock()
				askPermission := l.cfg.AskPermission
				l.mu.RUnlock()
				if !hookApproved && askPermission != nil {
					allow, alwaysAllow := askPermission(ctx, block.Name, permInput)
					if !allow {
						task.denied = true
						task.denyMsg = fmt.Sprintf("%s denied by user", block.Name)
						tasks = append(tasks, task)
						continue
					}
					if alwaysAllow {
						rule := permissions.SuggestRule(block.Name, permInput)
						l.cfg.Gate.AllowForSession(rule)
						if l.cfg.Cwd != "" {
							_ = permissions.PersistAllow(rule, l.cfg.Cwd)
						}
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
		return nil, nil
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
				res, err := wi.task.tool.Execute(ctx, wi.task.rawInput)
				if err != nil {
					taskResults[wi.idx] = toolResult{idx: wi.idx, text: fmt.Sprintf("tool error: %v", err), isError: true}
					return
				}
				if !res.IsError {
					l.notifyFileAccess(wi.task.block.Name, wi.task.block.Input)
				}
				text := ""
				if len(res.Content) > 0 {
					text = res.Content[0].Text
				}
				taskResults[wi.idx] = toolResult{idx: wi.idx, text: text, isError: res.IsError}
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
		res, err := wi.task.tool.Execute(ctx, wi.task.rawInput)
		if err != nil {
			taskResults[wi.idx] = toolResult{idx: wi.idx, text: fmt.Sprintf("tool error: %v", err), isError: true}
			continue
		}
		if !res.IsError {
			l.notifyFileAccess(wi.task.block.Name, wi.task.block.Input)
		}
		text := ""
		if len(res.Content) > 0 {
			text = res.Content[0].Text
		}
		taskResults[wi.idx] = toolResult{idx: wi.idx, text: text, isError: res.IsError}
	}

	// Phase 3: assemble results in original order + run PostToolUse hooks.
	var results []api.ContentBlock
	for i, task := range tasks {
		tr := taskResults[i]
		// PostToolUse fires unconditionally — error results included — so hook
		// authors that log or route tool activity see every outcome. Matches
		// TS reference (runPostToolUseHooks fires without an isError guard).
		if l.cfg.Hooks != nil && len(l.cfg.Hooks.PostToolUse) > 0 {
			hooks.RunPostToolUse(ctx, l.cfg.Hooks.PostToolUse, l.cfg.SessionID, task.block.Name, tr.text)
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
	return results, nil
}

// toolPermissionInput extracts the meaningful string to match against permission
// rules for a given tool. Rules like Bash(git log *) match the shell command,
// not the raw JSON input blob.
func toolPermissionInput(toolName string, input map[string]any) string {
	switch toolName {
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			return cmd
		}
	case "Edit":
		if p, ok := input["file_path"].(string); ok {
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
	}
	return ""
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

// buildToolDefs converts the registry into the API tool definitions array.
func buildToolDefs(reg *tool.Registry) []api.ToolDef {
	all := reg.All()
	if len(all) == 0 {
		return nil
	}
	defs := make([]api.ToolDef, 0, len(all))
	for _, t := range all {
		var schema map[string]any
		_ = json.Unmarshal(t.InputSchema(), &schema)
		defs = append(defs, api.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: schema,
		})
	}
	return defs
}
