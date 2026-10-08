package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/sse"
)

type openAIStreamChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// ReasoningContent is DeepSeek's field, also used by llama.cpp,
			// vLLM, Qwen and Kimi; Reasoning is OpenRouter's, Ollama's and
			// Groq's. Reasoning stays raw because some servers send an object.
			ReasoningContent string                      `json:"reasoning_content"`
			Reasoning        json.RawMessage             `json:"reasoning"`
			ToolCalls        []openAIStreamToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage,omitempty"`
	// Error is a mid-stream failure (OpenRouter, xAI, llama.cpp).
	Error *openAIErrorBody `json:"error,omitempty"`
}

type openAIStreamToolCallDelta struct {
	Index        int                 `json:"index"`
	ID           string              `json:"id"`
	Type         string              `json:"type"`
	Function     openAIToolFunction  `json:"function"`
	ExtraContent *openAIExtraContent `json:"extra_content,omitempty"`
}

// reasoningDelta returns reasoning text and the field that held it:
// reasoning_content, or reasoning when only that one is a non-empty string.
func reasoningDelta(content string, raw json.RawMessage) (text, field string) {
	if content != "" {
		return content, "reasoning_content"
	}
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil && s != "" {
			return s, "reasoning"
		}
	}
	return "", ""
}

// anthropicEventWriter writes Anthropic Messages SSE events into a
// converter's pipe.
type anthropicEventWriter struct {
	bw *bufio.Writer
}

func (w anthropicEventWriter) write(event string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("api: encode %s event: %w", event, err)
	}
	if _, err := fmt.Fprintf(w.bw, "event: %s\ndata: %s\n\n", event, raw); err != nil {
		return err
	}
	return w.bw.Flush()
}

// messageStart opens the converted message.
func (w anthropicEventWriter) messageStart(id, model string) error {
	return w.write("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":          id,
			"type":        "message",
			"role":        "assistant",
			"model":       model,
			"content":     []any{},
			"stop_reason": nil,
			"usage":       map[string]any{"input_tokens": 0},
		},
	})
}

// streamError ends the converted stream with an Anthropic error event, which
// Stream.Next returns as an *sse.Error.
func (w anthropicEventWriter) streamError(typ, message string) error {
	return w.write("error", map[string]any{
		"type":  "error",
		"error": map[string]any{"type": typ, "message": message},
	})
}

// anthropicBlocks tracks the content blocks a converter opens. Text and
// thinking blocks close as soon as another block starts; tool_use blocks stay
// open until the end, because their argument deltas can arrive interleaved.
type anthropicBlocks struct {
	w     anthropicEventWriter
	next  int    // index of the next block
	open  int    // index of the open text or thinking block, -1 when none
	kind  string // "text" or "thinking"
	tools []*anthropicToolBlock
}

type anthropicToolBlock struct {
	index      int
	hasArgs    bool
	providerID string // the id the server gave the call; "" if it gave none
}

func newAnthropicBlocks(w anthropicEventWriter) *anthropicBlocks {
	return &anthropicBlocks{w: w, open: -1}
}

// text appends to the open block of kind ("text" or "thinking"), opening one
// first if needed.
func (b *anthropicBlocks) text(kind, s string) error {
	if s == "" {
		return nil
	}
	if err := b.ensure(kind); err != nil {
		return err
	}
	delta := map[string]any{"type": "text_delta", "text": s}
	if kind == "thinking" {
		delta = map[string]any{"type": "thinking_delta", "thinking": s}
	}
	return b.w.write("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": b.open,
		"delta": delta,
	})
}

// ensure leaves a block of kind open, closing a different one and starting a
// new one as needed.
func (b *anthropicBlocks) ensure(kind string) error {
	if b.kind == kind {
		return nil
	}
	if err := b.closeOpen(); err != nil {
		return err
	}
	b.open, b.kind = b.next, kind
	b.next++
	block := map[string]any{"type": "text", "text": ""}
	if kind == "thinking" {
		block = map[string]any{"type": "thinking", "thinking": ""}
	}
	return b.w.write("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         b.open,
		"content_block": block,
	})
}

// thinkingSignature attaches sig to the open thinking block, starting an empty
// one when the reasoning had no visible text, and closes it: a signature ends
// its block.
func (b *anthropicBlocks) thinkingSignature(sig string) error {
	if err := b.ensure("thinking"); err != nil {
		return err
	}
	if err := b.w.write("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": b.open,
		"delta": map[string]any{"type": "signature_delta", "signature": sig},
	}); err != nil {
		return err
	}
	return b.closeOpen()
}

func (b *anthropicBlocks) closeOpen() error {
	if b.open < 0 {
		return nil
	}
	index := b.open
	b.open, b.kind = -1, ""
	return b.w.write("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
}

// startTool opens a tool_use block described by contentBlock.
func (b *anthropicBlocks) startTool(contentBlock map[string]any) (*anthropicToolBlock, error) {
	if err := b.closeOpen(); err != nil {
		return nil, err
	}
	tb := &anthropicToolBlock{index: b.next}
	b.next++
	b.tools = append(b.tools, tb)
	err := b.w.write("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         tb.index,
		"content_block": contentBlock,
	})
	return tb, err
}

func (b *anthropicBlocks) toolArgs(tb *anthropicToolBlock, partial string) error {
	if partial == "" {
		return nil
	}
	tb.hasArgs = true
	return b.w.write("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": tb.index,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": partial},
	})
}

// finish closes every open block, tools in the order they started, and ends
// the message. A tool block that got no arguments gets "{}". A natural end
// with tool calls becomes tool_use: some servers report "stop" for it.
func (b *anthropicBlocks) finish(stopReason string, usage map[string]any) error {
	if err := b.closeOpen(); err != nil {
		return err
	}
	for _, tb := range b.tools {
		if !tb.hasArgs {
			if err := b.toolArgs(tb, "{}"); err != nil {
				return err
			}
		}
		if err := b.w.write("content_block_stop", map[string]any{"type": "content_block_stop", "index": tb.index}); err != nil {
			return err
		}
	}
	if stopReason == "end_turn" && len(b.tools) > 0 {
		stopReason = "tool_use"
	}
	if err := b.w.write("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason},
		"usage": usage,
	}); err != nil {
		return err
	}
	return b.w.write("message_stop", map[string]any{"type": "message_stop"})
}

// postFinishWait is how long a stream that has sent its finish_reason, but
// neither usage nor [DONE], is waited on before it ends as is.
const postFinishWait = 2 * time.Second

// openAIStreamConfig configures convertOpenAIStream.
type openAIStreamConfig struct {
	model string
	// splitThinkTags turns <think>…</think> in delta.content into thinking.
	splitThinkTags bool
	// implicitThink expects output that may start inside reasoning, with the
	// <think> already in the prompt: text before a bare </think> is thinking.
	implicitThink bool
	// postFinishWait overrides postFinishWait, for tests.
	postFinishWait time.Duration
	// onReasoning, when set, learns the delta field reasoning arrived in.
	onReasoning func(field string)
	// textTools turns <tool_call> blocks in the visible content into tool_use
	// blocks (in-text tool calling).
	textTools bool
}

// convertOpenAIStream converts a /chat/completions SSE body into Anthropic
// Messages SSE events on writer. The message ends only after a finish_reason
// or [DONE]; a body that stops before either fails with ErrStreamTruncated.
// A provider error chunk, or a finish_reason that reports one, becomes an
// Anthropic error event.
func convertOpenAIStream(body io.ReadCloser, writer *io.PipeWriter, cfg openAIStreamConfig) {
	defer func() { _ = body.Close() }()
	if err := runOpenAIStream(body, anthropicEventWriter{bw: bufio.NewWriter(writer)}, cfg); err != nil {
		_ = writer.CloseWithError(err)
		return
	}
	_ = writer.Close()
}

func runOpenAIStream(body io.Reader, w anthropicEventWriter, cfg openAIStreamConfig) error {
	if err := w.messageStart("openai-compatible", cfg.model); err != nil {
		return err
	}
	blocks := newAnthropicBlocks(w)
	var split *thinkSplitter
	if cfg.splitThinkTags {
		split = &thinkSplitter{implicit: cfg.implicitThink}
	}
	var toolParser *textToolParser
	var nextToolID func() string
	if cfg.textTools {
		toolParser = &textToolParser{}
		nextToolID = newTextToolIDs()
	}
	emitToolEvents := func(events []textToolEvent) error {
		for _, ev := range events {
			if ev.call == nil {
				if err := blocks.text("text", ev.text); err != nil {
					return err
				}
				continue
			}
			tb, err := blocks.startTool(map[string]any{"type": "tool_use", "id": nextToolID(), "name": ev.call.name})
			if err != nil {
				return err
			}
			if err := blocks.toolArgs(tb, ev.call.args); err != nil {
				return err
			}
		}
		return nil
	}
	emitContent := func(segments []thinkSegment) error {
		for _, seg := range segments {
			switch {
			case seg.thinking:
				if err := blocks.text("thinking", seg.text); err != nil {
					return err
				}
			case toolParser != nil:
				if err := emitToolEvents(toolParser.feed(seg.text)); err != nil {
					return err
				}
			default:
				if err := blocks.text("text", seg.text); err != nil {
					return err
				}
			}
		}
		return nil
	}
	// endImplicit stops waiting for a bare </think>, emitting what was held as
	// visible text: the server has a real reasoning channel or moved on to a
	// tool call, so the text is the answer.
	endImplicit := func() error {
		if split == nil {
			return nil
		}
		return emitContent(split.endLeading())
	}
	tools := map[int]*anthropicToolBlock{}
	reasoningField := ""
	stopReason := ""
	terminal := false
	var usage *openAIUsage

	// A goroutine reads lines so the loop can give up on a host that keeps the
	// connection open after the finish_reason; closing body (the caller's
	// defer) unblocks it.
	lines := make(chan string)
	stopReader := make(chan struct{})
	defer close(stopReader)
	var scanErr error
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 64*1024), 8<<20)
		for scanner.Scan() {
			select {
			case lines <- scanner.Text():
			case <-stopReader:
				return
			}
		}
		scanErr = scanner.Err()
	}()
	wait := cfg.postFinishWait
	if wait <= 0 {
		wait = postFinishWait
	}
	var deadline <-chan time.Time
	readerDone := false // lines was closed, so scanErr is settled
	// Once a finish_reason and the usage are in, nothing else matters: stop
	// rather than wait on a host that keeps the connection open. Without a
	// usage chunk or [DONE], the read ends wait after the finish_reason.
read:
	for !(terminal && usage != nil) {
		var text string
		select {
		case l, ok := <-lines:
			if !ok {
				readerDone = true
				break read
			}
			text = l
		case <-deadline:
			break read
		}
		line := strings.TrimSpace(text)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			terminal = true
			break
		}
		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			if terminal {
				break
			}
			return fmt.Errorf("api: decode openai-compatible stream chunk: %w", err)
		}
		if chunk.Error != nil && !chunk.Error.empty() {
			return w.streamError(chunk.Error.anthropicType(), chunk.Error.describe())
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if text, field := reasoningDelta(choice.Delta.ReasoningContent, choice.Delta.Reasoning); text != "" {
				if field != reasoningField && cfg.onReasoning != nil {
					cfg.onReasoning(field)
				}
				reasoningField = field
				if err := endImplicit(); err != nil {
					return err
				}
				if err := blocks.text("thinking", text); err != nil {
					return err
				}
			}
			if content := choice.Delta.Content; content != "" {
				segments := []thinkSegment{{text: content}}
				if split != nil {
					segments = split.feed(content)
				}
				if err := emitContent(segments); err != nil {
					return err
				}
			}
			for _, call := range choice.Delta.ToolCalls {
				if err := endImplicit(); err != nil {
					return err
				}
				tb, ok := tools[call.Index]
				// A new id at an index in use is a new call: some servers
				// number parallel calls all 0. Argument-only deltas follow
				// the latest block at the index.
				if !ok || call.ID != "" && tb.providerID != "" && call.ID != tb.providerID {
					if call.ID == "" && call.Function.Name == "" && call.Function.Arguments == "" {
						continue
					}
					var err error
					if tb, err = blocks.startTool(openAIToolUseBlock(call)); err != nil {
						return err
					}
					tb.providerID = call.ID
					tools[call.Index] = tb
				}
				if err := blocks.toolArgs(tb, call.Function.Arguments); err != nil {
					return err
				}
			}
			if choice.FinishReason != nil && *choice.FinishReason != "" {
				reason, failed := openAIStopReason(*choice.FinishReason)
				if failed {
					e := openAIFinishError(*choice.FinishReason)
					return w.streamError(e.Type, e.Message)
				}
				stopReason = reason
				terminal = true
			}
		}
		if terminal && usage == nil && deadline == nil {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			deadline = timer.C
		}
	}
	// A read error after the finish_reason costs at most the usage chunk.
	if readerDone && scanErr != nil && !terminal {
		return fmt.Errorf("api: read openai-compatible stream: %w", scanErr)
	}
	if !terminal {
		return fmt.Errorf("api: openai-compatible stream: %w", ErrStreamTruncated)
	}
	if split != nil {
		if err := emitContent(split.flush()); err != nil {
			return err
		}
	}
	if toolParser != nil {
		if err := emitToolEvents(toolParser.flush()); err != nil {
			return err
		}
	}
	if stopReason == "" {
		stopReason = "end_turn"
	}
	out := map[string]any{"output_tokens": 0}
	if usage != nil {
		out["input_tokens"] = usage.PromptTokens
		out["output_tokens"] = usage.CompletionTokens
	} else if os.Getenv("CLAUDE_GO_DEBUG_HTTP") == "1" {
		// Token counts for this turn will be zero, so compaction thresholds
		// and cost tracking undercount. Not printed otherwise: stderr
		// output corrupts the TUI.
		fmt.Fprintf(os.Stderr, "conduit: openaicompat: stream ended without usage data; token counts will be zero for this turn\n")
	}
	return blocks.finish(stopReason, out)
}

// openAIToolUseBlock is the content_block_start payload for a streamed tool
// call, keeping Gemini's thought_signature when the first delta carries it.
func openAIToolUseBlock(call openAIStreamToolCallDelta) map[string]any {
	id := call.ID
	if id == "" {
		id = fmt.Sprintf("call_%d", call.Index)
	}
	block := map[string]any{
		"type": "tool_use",
		"id":   id,
		"name": call.Function.Name,
	}
	if call.ExtraContent != nil && call.ExtraContent.Google != nil && call.ExtraContent.Google.ThoughtSignature != "" {
		block["thought_signature"] = call.ExtraContent.Google.ThoughtSignature
	}
	return block
}

// openAIStopReason maps a finish_reason onto an Anthropic stop_reason. failed
// is set for the reasons that report a provider failure rather than an end of
// output (openAIFinishError describes those). Unknown reasons end the turn.
func openAIStopReason(reason string) (stopReason string, failed bool) {
	switch strings.ToLower(reason) {
	case "tool_calls", "function_call":
		return "tool_use", false
	case "length", "max_tokens":
		return "max_tokens", false
	case "content_filter":
		return "refusal", false
	case "network_error", "error", "insufficient_system_resource":
		return "", true
	default:
		return "end_turn", false
	}
}

// openAIFinishError is the stream error for a failure finish_reason. The
// reasons are transient (gateways report upstream failures this way, DeepSeek
// its capacity), so it is an api_error, which Classify retries.
func openAIFinishError(reason string) *sse.Error {
	return &sse.Error{Type: "api_error", Message: fmt.Sprintf("provider returned finish_reason %q", reason)}
}
