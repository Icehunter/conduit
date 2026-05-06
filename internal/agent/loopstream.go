package agent

import (
	"context"
	"encoding/json"
	"errors"
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
// assistant content blocks, the stop reason, and the input token count from
// the message_start event (used by auto-compact to gauge context pressure).
func (l *Loop) drainStream(ctx context.Context, stream *api.Stream, handler func(LoopEvent)) ([]api.ContentBlock, string, int, error) {
	// blockTexts accumulates text/input_json across deltas per block index.
	blockTexts := map[int]*strings.Builder{}
	metas := map[int]blockMeta{}

	stopReason := "end_turn"
	inputTokens := 0

	for {
		if ctx.Err() != nil {
			return buildContentBlocks(metas, blockTexts), stopReason, inputTokens, ctx.Err()
		}

		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Conversation recovery: build whatever blocks we accumulated
			// before the error so the caller can persist them.
			return buildContentBlocks(metas, blockTexts), stopReason, inputTokens, err
		}

		switch ev.Type {
		case "message_start":
			// Extract input_tokens for auto-compact threshold checking.
			if ms, err := ev.AsMessageStart(); err == nil {
				inputTokens = ms.Message.Usage.InputTokens
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

	return buildContentBlocks(metas, blockTexts), stopReason, inputTokens, nil
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
