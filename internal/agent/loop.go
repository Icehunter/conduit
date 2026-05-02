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

	"github.com/icehunter/claude-go/internal/api"
	"github.com/icehunter/claude-go/internal/hooks"
	"github.com/icehunter/claude-go/internal/permissions"
	"github.com/icehunter/claude-go/internal/settings"
	"github.com/icehunter/claude-go/internal/tool"
)

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

// SetAskPermission installs the interactive permission callback.
// Called from the TUI after the Bubble Tea program is created.
func (l *Loop) SetAskPermission(fn func(ctx context.Context, toolName, toolInput string) (allow, alwaysAllow bool)) {
	l.cfg.AskPermission = fn
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
func (l *Loop) executeTools(ctx context.Context, assistantBlocks []api.ContentBlock, handler func(LoopEvent)) ([]api.ContentBlock, error) {
	var results []api.ContentBlock
	for _, block := range assistantBlocks {
		if block.Type != "tool_use" {
			continue
		}

		rawInput, _ := json.Marshal(block.Input)
		if rawInput == nil {
			rawInput = json.RawMessage("{}")
		}

		// Extract the meaningful input string for permission rule matching.
		// Rules like Bash(git log *) match against the command, not raw JSON.
		permInput := toolPermissionInput(block.Name, block.Input)

		var resultText string
		var isError bool

		// --- Permission gate check ---
		if l.cfg.Gate != nil {
			decision := l.cfg.Gate.Check(block.Name, permInput)
			switch decision {
			case permissions.DecisionDeny:
				resultText = "Tool denied by permission rules"
				isError = true
				handler(LoopEvent{
					Type:       EventToolResult,
					ToolID:     block.ID,
					ToolName:   block.Name,
					ResultText: resultText,
					IsError:    isError,
				})
				results = append(results, api.ContentBlock{
					Type:          "tool_result",
					ToolUseID:     block.ID,
					IsError:       isError,
					ResultContent: resultText,
				})
				continue
			case permissions.DecisionAsk:
				if l.cfg.AskPermission != nil {
					allow, alwaysAllow := l.cfg.AskPermission(ctx, block.Name, permInput)
					if !allow {
						resultText = fmt.Sprintf("%s denied by user", block.Name)
						isError = true
						handler(LoopEvent{Type: EventToolResult, ToolID: block.ID, ToolName: block.Name, ResultText: resultText, IsError: true})
						results = append(results, api.ContentBlock{Type: "tool_result", ToolUseID: block.ID, IsError: true, ResultContent: resultText})
						continue
					}
					if alwaysAllow {
						l.cfg.Gate.AllowForSession(permissions.SuggestRule(block.Name, permInput))
					}
				}
				// fall through to execution
			}
			// DecisionAllow: proceed normally.
		}

		// --- PreToolUse hooks ---
		if l.cfg.Hooks != nil && len(l.cfg.Hooks.PreToolUse) > 0 {
			// block.Input is already map[string]any; copy it for the hook.
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
				resultText = "Tool blocked by hook: " + reason
				isError = true
				handler(LoopEvent{
					Type:       EventToolResult,
					ToolID:     block.ID,
					ToolName:   block.Name,
					ResultText: resultText,
					IsError:    isError,
				})
				results = append(results, api.ContentBlock{
					Type:          "tool_result",
					ToolUseID:     block.ID,
					IsError:       isError,
					ResultContent: resultText,
				})
				continue
			}
		}

		// --- Tool execution ---
		t, ok := l.reg.Lookup(block.Name)
		if !ok {
			resultText = fmt.Sprintf("Tool %q not found", block.Name)
			isError = true
		} else {
			res, err := t.Execute(ctx, rawInput)
			if err != nil {
				resultText = fmt.Sprintf("tool error: %v", err)
				isError = true
			} else {
				if len(res.Content) > 0 {
					resultText = res.Content[0].Text
				}
				isError = res.IsError
			}
		}

		// --- PostToolUse hooks ---
		if l.cfg.Hooks != nil && len(l.cfg.Hooks.PostToolUse) > 0 && !isError {
			hooks.RunPostToolUse(ctx, l.cfg.Hooks.PostToolUse, l.cfg.SessionID, block.Name, resultText)
		}

		handler(LoopEvent{
			Type:       EventToolResult,
			ToolID:     block.ID,
			ToolName:   block.Name,
			ResultText: resultText,
			IsError:    isError,
		})

		results = append(results, api.ContentBlock{
			Type:          "tool_result",
			ToolUseID:     block.ID,
			IsError:       isError,
			ResultContent: resultText,
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
