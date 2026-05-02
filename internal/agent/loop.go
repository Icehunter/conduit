// Package agent implements the M2 query loop — the streaming agentic
// turn cycle that drives tool dispatch and multi-turn conversation.
//
// The loop mirrors src/query.ts's queryLoop() but with M5 additions:
// permission gate checks and PreToolUse/PostToolUse hook runners around
// each tool execution.
//
// Loop behaviour:
//  1. POST /v1/messages with current conversation history.
//  2. Stream SSE events; collect text deltas and tool_use blocks.
//  3. If the stop_reason is "tool_use":
//     a. Check permissions gate for each tool.
//     b. Run PreToolUse hooks.
//     c. Execute each tool in sequence (serial; concurrency in M4).
//     d. Run PostToolUse hooks.
//     e. Append assistant message + user tool_result message to history.
//     f. Go to 1 (unless MaxTurns exceeded).
//  4. If stop_reason is "end_turn": return.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/hooks"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
)

// maxConcurrentTools is the worker pool size for parallel tool execution.
// Mirrors the coordinator's concurrency limit in src/coordinator/coordinatorMode.ts.
const maxConcurrentTools = 4

// EventType identifies what kind of loop event the caller receives.
type EventType int

const (
	EventText       EventType = iota // a text delta streamed from the model
	EventToolUse                     // a tool_use block completed; tool is about to run
	EventToolResult                  // tool execution finished
)

// LoopEvent is emitted to the caller's handler on each significant event.
type LoopEvent struct {
	Type EventType

	// EventText
	Text string

	// EventToolUse
	ToolName  string
	ToolID    string
	ToolInput json.RawMessage

	// EventToolResult
	ResultText string
	IsError    bool
}

// LoopConfig controls the loop's behaviour.
type LoopConfig struct {
	Model     string
	MaxTokens int
	System    []api.SystemBlock
	Metadata  map[string]any
	// MaxTurns caps the number of API calls (tool-use follow-ups each count
	// as one turn). 0 means no limit (use carefully).
	MaxTurns int

	// Gate is the permission gate to consult before each tool call.
	// nil means no gate (all tools allowed).
	Gate *permissions.Gate

	// Hooks is the hooks configuration to run around tool calls.
	// nil means no hooks.
	Hooks *settings.HooksSettings

	// SessionID is used when invoking hooks (passed as session_id in hook input).
	SessionID string

	// AskPermission is called when a tool needs interactive approval.
	// It blocks until the user responds. Returns (allow, alwaysAllow).
	// nil means DecisionAsk → allow through silently.
	AskPermission func(ctx context.Context, toolName, toolInput string) (allow, alwaysAllow bool)
}

// Loop drives the agentic query cycle.
type Loop struct {
	client *api.Client
	reg    *tool.Registry
	cfg    LoopConfig
}

// NewLoop constructs a Loop.
func NewLoop(client *api.Client, reg *tool.Registry, cfg LoopConfig) *Loop {
	return &Loop{client: client, reg: reg, cfg: cfg}
}

// SetModel updates the model used for new requests (from /model slash command).
func (l *Loop) SetModel(name string) {
	l.cfg.Model = name
}

// SetSystem replaces the system blocks for subsequent requests.
func (l *Loop) SetSystem(blocks []api.SystemBlock) {
	l.cfg.System = blocks
}

// SetClient swaps the API client (e.g. after a fresh login reloads credentials).
func (l *Loop) SetClient(client *api.Client) {
	l.client = client
}

// SetAskPermission installs the interactive permission callback.
// Called from the TUI after the Bubble Tea program is created.
func (l *Loop) SetAskPermission(fn func(ctx context.Context, toolName, toolInput string) (allow, alwaysAllow bool)) {
	l.cfg.AskPermission = fn
}

// RunSubAgent runs a nested agent loop with the given prompt as the sole user
// message. Used by AgentTool and SkillTool to spawn forked sub-agents.
// The sub-agent inherits the same tools, model, and system prompt but starts
// with a fresh single-turn history. Returns the concatenated text from the
// final assistant message.
func (l *Loop) RunSubAgent(ctx context.Context, prompt string) (string, error) {
	msgs := []api.Message{
		{
			Role:    "user",
			Content: []api.ContentBlock{{Type: "text", Text: prompt}},
		},
	}
	history, err := l.Run(ctx, msgs, func(LoopEvent) {})
	if err != nil {
		return "", err
	}
	// Extract the last assistant text from history.
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" {
			var sb strings.Builder
			for _, block := range history[i].Content {
				if block.Type == "text" && block.Text != "" {
					if sb.Len() > 0 {
						sb.WriteByte('\n')
					}
					sb.WriteString(block.Text)
				}
			}
			return sb.String(), nil
		}
	}
	return "", nil
}

// Run executes the agentic loop starting with the given messages. handler is
// called synchronously for each event; it must not block.
//
// Returns the full accumulated message history (including all tool turns) and
// nil error on clean end_turn. On error, returns whatever history was built
// before the failure. Callers should replace their history slice with the
// returned messages to correctly track multi-turn tool use.
func (l *Loop) Run(ctx context.Context, messages []api.Message, handler func(LoopEvent)) ([]api.Message, error) {
	msgs := make([]api.Message, len(messages))
	copy(msgs, messages)

	// Fire SessionStart hooks once before the first turn.
	if l.cfg.Hooks != nil && len(l.cfg.Hooks.SessionStart) > 0 {
		hooks.RunSessionStart(ctx, l.cfg.Hooks.SessionStart, l.cfg.SessionID)
	}
	defer func() {
		if l.cfg.Hooks != nil && len(l.cfg.Hooks.Stop) > 0 {
			hooks.RunStop(context.Background(), l.cfg.Hooks.Stop, l.cfg.SessionID)
		}
	}()

	// Build tool definitions from registry.
	tools := buildToolDefs(l.reg)

	turn := 0
	for {
		if ctx.Err() != nil {
			return msgs, ctx.Err()
		}
		if l.cfg.MaxTurns > 0 && turn >= l.cfg.MaxTurns {
			return msgs, nil
		}
		turn++

		req := &api.MessageRequest{
			Model:     l.cfg.Model,
			MaxTokens: l.cfg.MaxTokens,
			System:    l.cfg.System,
			Messages:  msgs,
			Stream:    true,
			Tools:     tools,
			Metadata:  l.cfg.Metadata,
		}

		stream, err := l.client.StreamMessage(ctx, req)
		if err != nil {
			return msgs, fmt.Errorf("agent: stream: %w", err)
		}

		assistantBlocks, stopReason, err := l.drainStream(ctx, stream, handler)
		stream.Close()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return msgs, err
			}
			return msgs, fmt.Errorf("agent: drain: %w", err)
		}

		// Append the assistant message to history.
		msgs = append(msgs, api.Message{
			Role:    "assistant",
			Content: assistantBlocks,
		})

		if stopReason != "tool_use" {
			// end_turn or unknown — we're done.
			return msgs, nil
		}

		// Execute tools, build a user message with all tool_results.
		toolResults, err := l.executeTools(ctx, assistantBlocks, handler)
		if err != nil {
			return msgs, fmt.Errorf("agent: execute tools: %w", err)
		}
		msgs = append(msgs, api.Message{
			Role:    "user",
			Content: toolResults,
		})
	}
}

// drainStream reads all SSE events from the stream and returns the accumulated
// assistant content blocks plus the stop reason.
func (l *Loop) drainStream(ctx context.Context, stream *api.Stream, handler func(LoopEvent)) ([]api.ContentBlock, string, error) {
	// blockTexts accumulates text/input_json across deltas per block index.
	blockTexts := map[int]*strings.Builder{}
	// blockMeta stores the block type and tool metadata per index.
	type blockMeta struct {
		blockType string
		toolID    string
		toolName  string
	}
	metas := map[int]blockMeta{}

	stopReason := "end_turn"

	for {
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}

		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, "", err
		}

		switch ev.Type {
		case "content_block_start":
			cbs, err := ev.AsContentBlockStart()
			if err != nil {
				continue
			}
			// Parse the content block to learn its type.
			var raw struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if e := json.Unmarshal(cbs.ContentBlock, &raw); e == nil {
				blockTexts[cbs.Index] = &strings.Builder{}
				metas[cbs.Index] = blockMeta{
					blockType: raw.Type,
					toolID:    raw.ID,
					toolName:  raw.Name,
				}
			}

		case "content_block_delta":
			cbd, err := ev.AsContentBlockDelta()
			if err != nil {
				continue
			}
			sb, ok := blockTexts[cbd.Index]
			if !ok {
				continue
			}
			switch cbd.Delta.Type {
			case "text_delta":
				sb.WriteString(cbd.Delta.Text)
				handler(LoopEvent{Type: EventText, Text: cbd.Delta.Text})
			case "input_json_delta":
				sb.WriteString(cbd.Delta.PartialJSON)
			}

		case "message_delta":
			md, err := ev.AsMessageDelta()
			if err != nil {
				continue
			}
			stopReason = md.Delta.StopReason

		case "content_block_stop":
			// Block is complete — for tool_use emit the EventToolUse event.
			cbs, err := ev.AsContentBlockStop()
			if err != nil {
				continue
			}
			meta, ok := metas[cbs.Index]
			if !ok {
				continue
			}
			if meta.blockType == "tool_use" {
				rawInput := json.RawMessage("{}")
				if sb, ok := blockTexts[cbs.Index]; ok && sb.Len() > 0 {
					rawInput = json.RawMessage(sb.String())
				}
				handler(LoopEvent{
					Type:      EventToolUse,
					ToolName:  meta.toolName,
					ToolID:    meta.toolID,
					ToolInput: rawInput,
				})
			}
		}
	}

	// Build content blocks from accumulated state.
	blocks := make([]api.ContentBlock, 0, len(metas))
	for i := 0; i < len(metas); i++ {
		meta, ok := metas[i]
		if !ok {
			continue
		}
		sb := blockTexts[i]
		switch meta.blockType {
		case "text":
			text := ""
			if sb != nil {
				text = sb.String()
			}
			blocks = append(blocks, api.ContentBlock{
				Type: "text",
				Text: text,
			})
		case "tool_use":
			inputStr := "{}"
			if sb != nil && sb.Len() > 0 {
				inputStr = sb.String()
			}
			var inputMap map[string]any
			_ = json.Unmarshal([]byte(inputStr), &inputMap)
			blocks = append(blocks, api.ContentBlock{
				Type:  "tool_use",
				ID:    meta.toolID,
				Name:  meta.toolName,
				Input: inputMap,
			})
		}
	}

	return blocks, stopReason, nil
}

// executeTools runs all tool_use blocks in the assistant message sequentially
// and returns the tool_result content blocks for the follow-up user message.
//
// For each tool:
//  1. Permission gate check (if configured).
//  2. PreToolUse hooks (if configured).
//  3. Tool execution.
//  4. PostToolUse hooks (if configured).
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

func (l *Loop) executeTools(ctx context.Context, assistantBlocks []api.ContentBlock, handler func(LoopEvent)) ([]api.ContentBlock, error) {
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
		if l.cfg.Gate != nil {
			decision := l.cfg.Gate.Check(block.Name, permInput)
			switch decision {
			case permissions.DecisionDeny:
				task.denied = true
				task.denyMsg = "Tool denied by permission rules"
				tasks = append(tasks, task)
				continue
			case permissions.DecisionAsk:
				if !hookApproved && l.cfg.AskPermission != nil {
					allow, alwaysAllow := l.cfg.AskPermission(ctx, block.Name, permInput)
					if !allow {
						task.denied = true
						task.denyMsg = fmt.Sprintf("%s denied by user", block.Name)
						tasks = append(tasks, task)
						continue
					}
					if alwaysAllow {
						l.cfg.Gate.AllowForSession(permissions.SuggestRule(block.Name, permInput))
					}
				}
			}
		}

		// Resolve the tool for execution.
		t, ok := l.reg.Lookup(block.Name)
		if !ok {
			task.denied = true
			task.denyMsg = fmt.Sprintf("Tool %q not found", block.Name)
		} else {
			task.tool = t
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
	type workItem struct{ idx int; task toolTask }
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
		for _, wi := range parallel {
			wi := wi
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				res, err := wi.task.tool.Execute(ctx, wi.task.rawInput)
				if err != nil {
					taskResults[wi.idx] = toolResult{idx: wi.idx, text: fmt.Sprintf("tool error: %v", err), isError: true}
					return
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
		if l.cfg.Hooks != nil && len(l.cfg.Hooks.PostToolUse) > 0 && !tr.isError {
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
