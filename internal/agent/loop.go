// Package agent implements the M2 query loop — the streaming agentic
// turn cycle that drives tool dispatch and multi-turn conversation.
//
// The loop mirrors src/query.ts's queryLoop() but without the M5+
// features: no autocompact, microcompact, thinking, snip, hooks, or
// multi-agent coordinator. Those land in later milestones.
//
// Loop behaviour:
//  1. POST /v1/messages with current conversation history.
//  2. Stream SSE events; collect text deltas and tool_use blocks.
//  3. If the stop_reason is "tool_use":
//     a. Execute each tool in sequence (M2 is serial; concurrency in M4).
//     b. Append assistant message + user tool_result message to history.
//     c. Go to 1 (unless MaxTurns exceeded).
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

		var resultText string
		var isError bool

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
