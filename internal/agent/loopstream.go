package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/icehunter/conduit/internal/api"
)

// blockMeta stores the block type and tool metadata per stream block index.
type blockMeta struct {
	blockType string
	toolID    string
	toolName  string
}

// drainStream reads all SSE events from the stream and returns the accumulated
// assistant content blocks, the stop reason, and the API-reported usage
// (input + output + cache fields) summed across all turns in the stream.
// Auto-compact uses Usage.InputTokens to gauge context pressure; the TUI
// uses the full Usage to surface real billable token counts to the user.
//
// EventUsage is emitted via handler on each message_stop so the TUI can
// update token counts incrementally when multiple turns arrive in one stream.
func (l *Loop) drainStream(ctx context.Context, stream *api.Stream, handler func(LoopEvent)) ([]api.ContentBlock, string, api.Usage, error) {
	// blockTexts accumulates text/input_json across deltas per block index.
	blockTexts := map[int]*strings.Builder{}
	metas := map[int]blockMeta{}

	stopReason := "end_turn"
	var totalUsage api.Usage // sum across all turns in this stream
	var turnUsage api.Usage  // usage for the current turn (reset at message_start)
	var gotMessageStart bool
	var gotMessageStop bool

	for {
		if ctx.Err() != nil {
			return buildContentBlocks(metas, blockTexts), stopReason, totalUsage, ctx.Err()
		}

		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Conversation recovery: build whatever blocks we accumulated
			// before the error so the caller can persist them.
			return buildContentBlocks(metas, blockTexts), stopReason, totalUsage, err
		}

		switch ev.Type {
		case "message_start":
			// message_start opens a new turn — reset per-turn accumulator.
			// input_tokens + cache fields live here; output_tokens is 0 here
			// and finalized in the subsequent message_delta.
			gotMessageStart = true
			gotMessageStop = false
			turnUsage = api.Usage{}
			if ms, err := ev.AsMessageStart(); err == nil {
				turnUsage.InputTokens = ms.Message.Usage.InputTokens
				turnUsage.CacheCreationInputTokens = ms.Message.Usage.CacheCreationInputTokens
				turnUsage.CacheReadInputTokens = ms.Message.Usage.CacheReadInputTokens
				if ms.Message.Usage.OutputTokens > 0 {
					turnUsage.OutputTokens = ms.Message.Usage.OutputTokens
				}
			}

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
				if raw.Type == "tool_use" {
					handler(LoopEvent{
						Type:     EventToolStart,
						ToolName: raw.Name,
						ToolID:   raw.ID,
					})
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
			// message_delta carries the finalized output_tokens for the turn.
			if md.Usage.OutputTokens > 0 {
				turnUsage.OutputTokens = md.Usage.OutputTokens
			}

		case "message_stop":
			// Emit per-turn usage and accumulate into the stream total.
			gotMessageStop = true
			if turnUsage.InputTokens > 0 || turnUsage.OutputTokens > 0 {
				handler(LoopEvent{Type: EventUsage, Usage: turnUsage})
				totalUsage.InputTokens += turnUsage.InputTokens
				totalUsage.OutputTokens += turnUsage.OutputTokens
				totalUsage.CacheCreationInputTokens += turnUsage.CacheCreationInputTokens
				totalUsage.CacheReadInputTokens += turnUsage.CacheReadInputTokens
			}

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

	// If the connection closed (io.EOF) after message_start but before
	// message_stop arrived, the server dropped the stream prematurely.
	// Surface an error so the agent loop doesn't silently treat a truncated
	// response as a completed turn.
	if gotMessageStart && !gotMessageStop {
		return buildContentBlocks(metas, blockTexts), stopReason, totalUsage,
			fmt.Errorf("sse: stream closed before message_stop")
	}

	return buildContentBlocks(metas, blockTexts), stopReason, totalUsage, nil
}

// buildContentBlocks materializes accumulated stream state into api.ContentBlocks.
// Used both for the success path and for partial-block recovery on stream error.
// metas is keyed by block index; blockTexts holds the accumulated text/json.
func buildContentBlocks(metas map[int]blockMeta, blockTexts map[int]*strings.Builder) []api.ContentBlock {
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
			// Skip empty text blocks — they'd be rejected by the API on resume.
			if text == "" {
				continue
			}
			blocks = append(blocks, api.ContentBlock{Type: "text", Text: text})
		case "tool_use":
			inputStr := "{}"
			if sb != nil && sb.Len() > 0 {
				inputStr = sb.String()
			}
			var inputMap map[string]any
			if err := json.Unmarshal([]byte(inputStr), &inputMap); err != nil {
				// Partial JSON — drop. A truncated tool_use can't be replayed
				// safely; conversation recovery on /resume would have to drop
				// it anyway via FilterUnresolvedToolUses.
				continue
			}
			blocks = append(blocks, api.ContentBlock{
				Type:  "tool_use",
				ID:    meta.toolID,
				Name:  meta.toolName,
				Input: inputMap,
			})
		}
	}
	return blocks
}
