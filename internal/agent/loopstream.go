package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/ttsr"
)

// blockMeta stores the block type and tool metadata per stream block index.
type blockMeta struct {
	blockType        string
	toolID           string
	toolName         string
	thoughtSignature string // Gemini thought_signature from content_block_start (OpenAI-compat path)
}

// startedTool is a tool_use block whose row the UI opened (EventToolStart).
type startedTool struct {
	ID, Name string
}

// streamResult is what drainStream collected from one response, also when it
// returns an error (the partial state, for recovery and cleanup).
type streamResult struct {
	// Blocks are the assistant content blocks. An invalid or truncated
	// tool_use is kept with an empty input and listed in InvalidTools.
	Blocks     []api.ContentBlock
	StopReason string
	// Usage is the API-reported usage (input + output + cache fields) summed
	// across all turns in the stream. Auto-compact uses Usage.PromptInputTokens
	// to gauge context pressure; the TUI uses the full Usage for real token
	// counts.
	Usage api.Usage
	// InvalidTools maps the id of each tool_use whose arguments were not valid
	// JSON to the error text its tool_result carries.
	InvalidTools map[string]string
	// StartedTools lists every tool_use row opened during the stream, in order.
	StartedTools []startedTool
}

// Error texts for tool calls whose arguments did not parse. The model reads
// them as the tool_result, so they say what to do next.
const (
	toolArgsTruncatedMsg = "The tool call was cut off because the response hit the output token limit, so its arguments were incomplete and the tool did not run. Re-issue the call with smaller arguments, for example by splitting a large write into several smaller steps."
	toolArgsInvalidMsg   = "The tool call arguments were not valid JSON, so the tool did not run. Re-issue the call with valid arguments."
)

// drainStream reads all SSE events from the stream and returns what it
// collected as a streamResult.
//
// EventUsage is emitted via handler on each message_stop so the TUI can
// update token counts incrementally when multiple turns arrive in one stream.
//
// watcher is an optional TTSR watcher. When non-nil and a rule fires,
// drainStream returns early with a *ttsr.ErrMatch error so the caller can
// inject the correction and restart the turn. Pass nil to disable TTSR.
func (l *Loop) drainStream(ctx context.Context, stream *api.Stream, handler func(LoopEvent), watcher *ttsr.Watcher) (streamResult, error) {
	// blockTexts accumulates text/input_json across deltas per block index.
	blockTexts := map[int]*strings.Builder{}
	metas := map[int]blockMeta{}
	// blockSignatures stores the signature_delta value for thinking blocks.
	// Keyed by block index; only set when the block is a thinking block.
	blockSignatures := map[int]string{}

	stopReason := "end_turn"
	var totalUsage api.Usage // sum across all turns in this stream
	var turnUsage api.Usage  // usage for the current turn (reset at message_start)
	var gotMessageStop bool
	var started []startedTool

	// result snapshots the state so far; error paths return it as the partial.
	result := func() streamResult {
		blocks, invalid := buildBlocks(metas, blockTexts, blockSignatures)
		r := streamResult{Blocks: blocks, StopReason: stopReason, Usage: totalUsage, StartedTools: started}
		if len(invalid) > 0 {
			msg := toolArgsInvalidMsg
			if stopReason == "max_tokens" {
				msg = toolArgsTruncatedMsg
			}
			r.InvalidTools = make(map[string]string, len(invalid))
			for _, id := range invalid {
				r.InvalidTools[id] = msg
			}
		}
		return r
	}

	for {
		if ctx.Err() != nil {
			return result(), ctx.Err()
		}

		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Conversation recovery: hand back whatever blocks we accumulated
			// before the error so the caller can persist them.
			return result(), err
		}

		switch ev.Type {
		case "message_start":
			// message_start opens a new turn — reset per-turn accumulator.
			// input_tokens + cache fields live here; output_tokens is 0 here
			// and finalized in the subsequent message_delta.
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
				Type             string `json:"type"`
				ID               string `json:"id"`
				Name             string `json:"name"`
				ThoughtSignature string `json:"thought_signature"`
			}
			if e := json.Unmarshal(cbs.ContentBlock, &raw); e == nil {
				blockTexts[cbs.Index] = &strings.Builder{}
				metas[cbs.Index] = blockMeta{
					blockType:        raw.Type,
					toolID:           raw.ID,
					toolName:         raw.Name,
					thoughtSignature: raw.ThoughtSignature,
				}
				if raw.Type == "tool_use" {
					started = append(started, startedTool{ID: raw.ID, Name: raw.Name})
					handler(LoopEvent{
						Type:     EventToolStart,
						ToolName: raw.Name,
						ToolID:   raw.ID,
					})
				}
				// server_tool_use blocks are preserved in history for API
				// round-tripping but are not dispatched locally. No event is
				// emitted — the server manages execution and result injection.
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
				if watcher != nil {
					if rule, fired := watcher.Feed(cbd.Delta.Text); fired {
						return result(), &ttsr.ErrMatch{Rule: rule}
					}
				}
			case "thinking_delta":
				// Accumulate thinking text into the block builder (preserved for
				// the API round-trip) and surface it as EventThinking so the
				// TUI shows it apart from the answer text.
				sb.WriteString(cbd.Delta.Thinking)
				handler(LoopEvent{Type: EventThinking, Text: cbd.Delta.Thinking})
				if watcher != nil {
					if rule, fired := watcher.FeedThinking(cbd.Delta.Thinking); fired {
						return result(), &ttsr.ErrMatch{Rule: rule}
					}
				}
			case "signature_delta":
				// The thinking block signature must be round-tripped to the API
				// verbatim. Store it keyed by block index.
				blockSignatures[cbd.Index] = cbd.Delta.Signature
			case "input_json_delta":
				sb.WriteString(cbd.Delta.PartialJSON)
			}

		case "message_delta":
			md, err := ev.AsMessageDelta()
			if err != nil {
				// A malformed message_delta loses output_tokens and stop_reason
				// for this turn. Log so token undercounts are not silent.
				fmt.Fprintf(os.Stderr, "conduit: drainStream: message_delta decode error (usage/stop_reason lost): %v\n", err)
				continue
			}
			stopReason = md.Delta.StopReason
			// Anthropic message_delta carries finalized output_tokens. Some
			// OpenAI-compatible streams only provide prompt usage at the end;
			// the adapter maps that onto this same usage block.
			if md.Usage.InputTokens > 0 {
				turnUsage.InputTokens = md.Usage.InputTokens
			}
			if md.Usage.CacheCreationInputTokens > 0 {
				turnUsage.CacheCreationInputTokens = md.Usage.CacheCreationInputTokens
			}
			if md.Usage.CacheReadInputTokens > 0 {
				turnUsage.CacheReadInputTokens = md.Usage.CacheReadInputTokens
			}
			if md.Usage.OutputTokens > 0 {
				turnUsage.OutputTokens = md.Usage.OutputTokens
			}
			// Emit running cost estimate so the TUI can display it during streaming.
			if turnUsage.InputTokens > 0 || turnUsage.OutputTokens > 0 {
				cost := api.CostUSDForModel(l.cfg.Model, turnUsage)
				handler(LoopEvent{Type: EventCost, CostUSD: cost, Usage: turnUsage})
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
				if sb, ok := blockTexts[cbs.Index]; ok && sb.Len() > 0 && json.Valid([]byte(sb.String())) {
					rawInput = json.RawMessage(sb.String())
				}
				handler(LoopEvent{
					Type:      EventToolUse,
					ToolName:  meta.toolName,
					ToolID:    meta.toolID,
					ToolInput: rawInput,
				})
				if watcher != nil {
					if rule, fired := watcher.CheckToolCall(meta.toolName, string(rawInput)); fired {
						return result(), &ttsr.ErrMatch{Rule: rule}
					}
				}
			}
		}
	}

	// The connection closed without a message_stop: the server dropped the
	// stream (or never started one). Surface it so the loop does not treat a
	// truncated response as a completed turn.
	if !gotMessageStop {
		return result(), fmt.Errorf("agent: stream closed before message_stop: %w", api.ErrStreamTruncated)
	}

	return result(), nil
}

// buildBlocks materializes accumulated stream state into api.ContentBlocks.
// Used both for the success path and for partial-block recovery on stream error.
// metas is keyed by block index; blockTexts holds the accumulated text/json;
// blockSignatures holds the signature for thinking blocks. A tool_use whose
// arguments are not a JSON object is kept with an empty input and its id is
// returned in invalid, so the loop can answer it with an error tool_result
// rather than losing the call.
func buildBlocks(metas map[int]blockMeta, blockTexts map[int]*strings.Builder, blockSignatures map[int]string) (blocks []api.ContentBlock, invalid []string) {
	blocks = make([]api.ContentBlock, 0, len(metas))
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
		case "thinking":
			// Thinking blocks must be preserved verbatim and round-tripped to
			// the API in subsequent turns — the model uses them for context.
			// An empty thinking block is valid (budget used but no output).
			thinkingText := ""
			if sb != nil {
				thinkingText = sb.String()
			}
			// A block with neither text nor signature carries nothing the API
			// can replay — keeping it only guarantees a 400 on the next turn.
			if thinkingText == "" && blockSignatures[i] == "" {
				continue
			}
			blocks = append(blocks, api.ContentBlock{
				Type:      "thinking",
				Thinking:  thinkingText,
				Signature: blockSignatures[i],
			})
		case "tool_use":
			inputStr := "{}"
			if sb != nil && sb.Len() > 0 {
				inputStr = sb.String()
			}
			var inputMap map[string]any
			if err := json.Unmarshal([]byte(inputStr), &inputMap); err != nil {
				// Truncated or malformed arguments: keep the call with an
				// empty input so it can be answered with an error result.
				inputMap = nil
				invalid = append(invalid, meta.toolID)
			}
			if inputMap == nil {
				inputMap = map[string]any{}
			}
			block := api.ContentBlock{
				Type:  "tool_use",
				ID:    meta.toolID,
				Name:  meta.toolName,
				Input: inputMap,
			}
			if meta.thoughtSignature != "" {
				block.ThoughtSignature = meta.thoughtSignature
			}
			blocks = append(blocks, block)
		case "server_tool_use":
			// server_tool_use blocks are emitted by the API for built-in server-side
			// tools (e.g. web_search). Conduit does not execute them locally; they are
			// preserved verbatim in the assistant message for API round-tripping.
			// The input accumulates as JSON in the string builder.
			inputRaw := json.RawMessage("{}")
			if sb != nil && sb.Len() > 0 {
				candidate := json.RawMessage(sb.String())
				// Only keep it if it parses as valid JSON — drop truncated blocks.
				var probe any
				if err := json.Unmarshal(candidate, &probe); err != nil {
					continue
				}
				inputRaw = candidate
			}
			blocks = append(blocks, api.ContentBlock{
				Type:            "server_tool_use",
				ID:              meta.toolID,
				Name:            meta.toolName,
				ServerToolInput: inputRaw,
			})
		}
	}
	return blocks, invalid
}
