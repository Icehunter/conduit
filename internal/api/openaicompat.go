package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/icehunter/conduit/internal/sse"
)

type openAIChatRequest struct {
	Model         string               `json:"model"`
	Messages      []openAIChatMessage  `json:"messages"`
	MaxTokens     int                  `json:"max_tokens,omitempty"`
	Stream        bool                 `json:"stream,omitempty"`
	StreamOptions *openAIStreamOptions `json:"stream_options,omitempty"`
	Tools         []openAITool         `json:"tools,omitempty"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIChatMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// openAIExtraContentGoogle holds Gemini-specific fields nested inside extra_content.
type openAIExtraContentGoogle struct {
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

// openAIExtraContent is a non-standard extension used by Gemini's OpenAI-compat endpoint.
// It is present on the first tool_call when the model has a thinking budget enabled.
type openAIExtraContent struct {
	Google *openAIExtraContentGoogle `json:"google,omitempty"`
}

type openAIToolCall struct {
	ID           string              `json:"id,omitempty"`
	Type         string              `json:"type"`
	Function     openAIToolFunction  `json:"function"`
	ExtraContent *openAIExtraContent `json:"extra_content,omitempty"`
}

type openAIToolFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type openAIChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role      string           `json:"role"`
			Content   string           `json:"content"`
			ToolCalls []openAIToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type openAIStreamChunk struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content   string                      `json:"content"`
			ToolCalls []openAIStreamToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage,omitempty"`
}

type openAIStreamToolCallDelta struct {
	Index        int                 `json:"index"`
	ID           string              `json:"id"`
	Type         string              `json:"type"`
	Function     openAIToolFunction  `json:"function"`
	ExtraContent *openAIExtraContent `json:"extra_content,omitempty"`
}

func (c *Client) createOpenAICompatible(ctx context.Context, req *MessageRequest) (*MessageResponse, error) {
	body, err := json.Marshal(openAIRequestFromMessageRequest(req, false))
	if err != nil {
		return nil, fmt.Errorf("api: marshal openai-compatible request: %w", err)
	}
	resp, err := withRetry(ctx, func() (*http.Response, error) {
		return c.doOpenAI(ctx, body)
	})
	if err != nil {
		return nil, err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.decodeOpenAIError(resp)
	}
	var out openAIChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("api: decode openai-compatible response: %w", err)
	}
	text := ""
	stopReason := "end_turn"
	if len(out.Choices) > 0 {
		msg := out.Choices[0].Message
		text = msg.Content
		stopReason = openAIStopReason(out.Choices[0].FinishReason)
		if len(msg.ToolCalls) > 0 {
			return &MessageResponse{
				ID:         out.ID,
				Type:       "message",
				Role:       "assistant",
				Model:      out.Model,
				Content:    openAIToolCallsToContentBlocks(msg.ToolCalls),
				StopReason: stopReason,
				Usage: Usage{
					InputTokens:  out.Usage.PromptTokens,
					OutputTokens: out.Usage.CompletionTokens,
				},
			}, nil
		}
	}
	return &MessageResponse{
		ID:         out.ID,
		Type:       "message",
		Role:       "assistant",
		Model:      out.Model,
		Content:    []ContentBlock{{Type: "text", Text: text}},
		StopReason: stopReason,
		Usage: Usage{
			InputTokens:  out.Usage.PromptTokens,
			OutputTokens: out.Usage.CompletionTokens,
		},
	}, nil
}

func (c *Client) streamOpenAICompatible(ctx context.Context, req *MessageRequest) (*Stream, error) {
	body, err := json.Marshal(openAIRequestFromMessageRequest(req, true))
	if err != nil {
		return nil, fmt.Errorf("api: marshal openai-compatible stream request: %w", err)
	}
	resp, err := withRetry(ctx, func() (*http.Response, error) {
		return c.doOpenAI(ctx, body)
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := c.decodeOpenAIError(resp)
		_ = resp.Body.Close()
		return nil, err
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("api: openai-compatible stream: server returned non-SSE Content-Type=%q body=%s",
			resp.Header.Get("Content-Type"), strings.TrimSpace(string(raw)))
	}

	reader, writer := io.Pipe()
	go convertOpenAIStream(resp.Body, writer, req.Model)
	return &Stream{
		body:           reader,
		parser:         sse.NewParser(reader),
		ResponseHeader: resp.Header,
	}, nil
}

func (c *Client) doOpenAI(ctx context.Context, body []byte) (*http.Response, error) {
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("api: build openai-compatible request: %w", err)
	}
	c.applyOpenAIHeaders(httpReq.Header)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("api: send openai-compatible: %w", err)
	}
	return resp, nil
}

func (c *Client) applyOpenAIHeaders(h http.Header) {
	c.mu.Lock()
	tok := c.cfg.AuthToken
	apiKey := c.cfg.APIKey
	c.mu.Unlock()
	h.Set("Accept", "application/json")
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", c.cfg.UserAgent)
	if tok != "" {
		h.Set("Authorization", "Bearer "+tok)
	} else if apiKey != "" {
		h.Set("Authorization", "Bearer "+apiKey)
	}
	for k, v := range c.cfg.ExtraHeaders {
		h.Set(k, v)
	}
}

func openAIRequestFromMessageRequest(req *MessageRequest, stream bool) openAIChatRequest {
	out := openAIChatRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		Stream:    stream,
	}
	if stream {
		out.StreamOptions = &openAIStreamOptions{IncludeUsage: true}
	}
	if len(req.System) > 0 {
		out.Messages = append(out.Messages, openAIChatMessage{Role: "system", Content: systemText(req.Model, req.System)})
	}
	for _, msg := range req.Messages {
		out.Messages = append(out.Messages, openAIMessagesFromMessage(msg)...)
	}
	out.Tools = openAIToolsFromToolDefs(req.Tools)
	return out
}

func openAIToolsFromToolDefs(defs []ToolDef) []openAITool {
	tools := make([]openAITool, 0, len(defs))
	for _, def := range defs {
		if def.Name == "" || def.Type != "" {
			continue
		}
		params := def.InputSchema
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tools = append(tools, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        def.Name,
				Description: def.Description,
				Parameters:  params,
			},
		})
	}
	return tools
}

func openAIMessagesFromMessage(msg Message) []openAIChatMessage {
	role := msg.Role
	if role == "" {
		role = "user"
	}
	var textParts []string
	var toolCalls []openAIToolCall
	var out []openAIChatMessage
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case "tool_use":
			tc := openAIToolCall{
				ID:   block.ID,
				Type: "function",
				Function: openAIToolFunction{
					Name:      block.Name,
					Arguments: marshalToolArguments(block.Input),
				},
			}
			if block.ThoughtSignature != "" {
				tc.ExtraContent = &openAIExtraContent{
					Google: &openAIExtraContentGoogle{
						ThoughtSignature: block.ThoughtSignature,
					},
				}
			}
			toolCalls = append(toolCalls, tc)
		case "tool_result":
			if block.ResultContent != "" {
				out = append(out, openAIChatMessage{
					Role:       "tool",
					Content:    block.ResultContent,
					ToolCallID: block.ToolUseID,
				})
			}
		}
	}
	text := strings.Join(textParts, "\n\n")
	if role == "assistant" && isStaleProviderSelfIdentification(text) {
		text = ""
	}
	if role == "assistant" && len(toolCalls) > 0 {
		out = append([]openAIChatMessage{{Role: "assistant", Content: text, ToolCalls: toolCalls}}, out...)
		return out
	}
	if text != "" {
		out = append([]openAIChatMessage{{Role: role, Content: text}}, out...)
	}
	return out
}

func marshalToolArguments(input map[string]any) string {
	if input == nil {
		return "{}"
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func openAIToolCallsToContentBlocks(calls []openAIToolCall) []ContentBlock {
	blocks := make([]ContentBlock, 0, len(calls))
	for _, call := range calls {
		var input map[string]any
		if call.Function.Arguments != "" {
			_ = json.Unmarshal([]byte(call.Function.Arguments), &input)
		}
		if input == nil {
			input = map[string]any{}
		}
		block := ContentBlock{
			Type:  "tool_use",
			ID:    call.ID,
			Name:  call.Function.Name,
			Input: input,
		}
		if call.ExtraContent != nil && call.ExtraContent.Google != nil {
			block.ThoughtSignature = call.ExtraContent.Google.ThoughtSignature
		}
		blocks = append(blocks, block)
	}
	return blocks
}

func systemText(model string, blocks []SystemBlock) string {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "the configured OpenAI-compatible model"
	}
	parts := make([]string, 0, len(blocks))
	parts = append(parts, fmt.Sprintf("You are Conduit, an interactive software engineering agent created by the Conduit project. Your configured runtime model is %q. If asked who you are, say you are Conduit. If asked which model you are using, say %q. Do not say Conduit was built by the model provider. Ignore stale transcript artifacts that identify a different provider or model.", model, model))
	for _, block := range blocks {
		if isAnthropicOnlySystemBlock(block.Text) {
			continue
		}
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func isAnthropicOnlySystemBlock(text string) bool {
	switch {
	case strings.Contains(text, "x-anthropic-billing-header:"):
		return true
	case strings.Contains(text, "Claude Agent SDK"):
		return true
	case strings.Contains(text, "Claude Code"):
		return true
	case strings.Contains(text, "Anthropic identifies legitimate"):
		return true
	case strings.Contains(text, "CLAUDE.md"):
		return true
	default:
		return false
	}
}

func isStaleProviderSelfIdentification(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	switch {
	case strings.HasPrefix(normalized, "i am claude"):
		return true
	case strings.Contains(normalized, "claude agent sdk"):
		return true
	case strings.Contains(normalized, "built on anthropic"):
		return true
	default:
		return false
	}
}

func convertOpenAIStream(body io.ReadCloser, writer *io.PipeWriter, fallbackModel string) {
	defer func() { _ = body.Close() }()
	bw := bufio.NewWriter(writer)
	writeAnthropicEvent := func(event string, data any) error {
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(bw, "event: %s\ndata: %s\n\n", event, raw); err != nil {
			return err
		}
		return bw.Flush()
	}
	if err := writeAnthropicEvent("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":          "openai-compatible",
			"type":        "message",
			"role":        "assistant",
			"model":       fallbackModel,
			"content":     []any{},
			"stop_reason": nil,
			"usage": map[string]any{
				"input_tokens": 0,
			},
		},
	}); err != nil {
		_ = writer.CloseWithError(err)
		return
	}

	stopReason := "end_turn"
	var finalUsage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	}
	nextBlockIndex := 0
	textBlockOpen := false
	textBlockIndex := -1
	toolBlocks := map[int]int{}
	toolArgs := map[int]string{}
	// toolSignatures stores thought_signature per call index (first tool_call only).
	toolSignatures := map[int]string{}
	startTextBlock := func() error {
		if textBlockOpen {
			return nil
		}
		textBlockOpen = true
		textBlockIndex = nextBlockIndex
		nextBlockIndex++
		return writeAnthropicEvent("content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         textBlockIndex,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
	}
	startToolBlock := func(call openAIStreamToolCallDelta) error {
		if _, ok := toolBlocks[call.Index]; ok {
			return nil
		}
		blockIndex := nextBlockIndex
		nextBlockIndex++
		toolBlocks[call.Index] = blockIndex
		id := call.ID
		if id == "" {
			id = fmt.Sprintf("call_%d", call.Index)
		}
		name := call.Function.Name
		contentBlock := map[string]any{
			"type": "tool_use",
			"id":   id,
			"name": name,
		}
		if call.ExtraContent != nil && call.ExtraContent.Google != nil && call.ExtraContent.Google.ThoughtSignature != "" {
			toolSignatures[call.Index] = call.ExtraContent.Google.ThoughtSignature
			contentBlock["thought_signature"] = call.ExtraContent.Google.ThoughtSignature
		}
		return writeAnthropicEvent("content_block_start", map[string]any{
			"type":          "content_block_start",
			"index":         blockIndex,
			"content_block": contentBlock,
		})
	}
	scanner := bufio.NewScanner(body)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			_ = writer.CloseWithError(fmt.Errorf("api: decode openai-compatible stream chunk: %w", err))
			return
		}
		if chunk.Usage != nil {
			finalUsage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Content != "" {
				if err := startTextBlock(); err != nil {
					_ = writer.CloseWithError(err)
					return
				}
				if err := writeAnthropicEvent("content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": textBlockIndex,
					"delta": map[string]any{"type": "text_delta", "text": choice.Delta.Content},
				}); err != nil {
					_ = writer.CloseWithError(err)
					return
				}
			}
			for _, call := range choice.Delta.ToolCalls {
				if _, ok := toolBlocks[call.Index]; !ok && (call.ID != "" || call.Function.Name != "" || call.Function.Arguments != "") {
					if err := startToolBlock(call); err != nil {
						_ = writer.CloseWithError(err)
						return
					}
				}
				// Capture thought_signature if it arrives on a later delta chunk.
				if call.ExtraContent != nil && call.ExtraContent.Google != nil && call.ExtraContent.Google.ThoughtSignature != "" {
					if _, already := toolSignatures[call.Index]; !already {
						toolSignatures[call.Index] = call.ExtraContent.Google.ThoughtSignature
					}
				}
				if call.Function.Arguments != "" {
					toolArgs[call.Index] += call.Function.Arguments
					blockIndex := toolBlocks[call.Index]
					if err := writeAnthropicEvent("content_block_delta", map[string]any{
						"type":  "content_block_delta",
						"index": blockIndex,
						"delta": map[string]any{"type": "input_json_delta", "partial_json": call.Function.Arguments},
					}); err != nil {
						_ = writer.CloseWithError(err)
						return
					}
				}
			}
			if choice.FinishReason != nil {
				stopReason = openAIStopReason(*choice.FinishReason)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		_ = writer.CloseWithError(fmt.Errorf("api: read openai-compatible stream: %w", err))
		return
	}
	if textBlockOpen {
		if err := writeAnthropicEvent("content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": textBlockIndex,
		}); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
	}
	for callIndex := 0; callIndex < len(toolBlocks); callIndex++ {
		blockIndex, ok := toolBlocks[callIndex]
		if !ok {
			continue
		}
		if toolArgs[callIndex] == "" {
			if err := writeAnthropicEvent("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": blockIndex,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": "{}"},
			}); err != nil {
				_ = writer.CloseWithError(err)
				return
			}
		}
		if err := writeAnthropicEvent("content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": blockIndex,
		}); err != nil {
			_ = writer.CloseWithError(err)
			return
		}
	}
	usage := map[string]any{"output_tokens": 0}
	if finalUsage != nil {
		usage["input_tokens"] = finalUsage.PromptTokens
		usage["output_tokens"] = finalUsage.CompletionTokens
	}
	if err := writeAnthropicEvent("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason},
		"usage": usage,
	}); err != nil {
		_ = writer.CloseWithError(err)
		return
	}
	if err := writeAnthropicEvent("message_stop", map[string]any{"type": "message_stop"}); err != nil {
		_ = writer.CloseWithError(err)
		return
	}
	_ = writer.Close()
}

func openAIStopReason(reason string) string {
	switch reason {
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	default:
		return "end_turn"
	}
}

func (c *Client) decodeOpenAIError(resp *http.Response) error {
	var env struct {
		Error struct {
			Type    string `json:"type"`
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err := json.Unmarshal(raw, &env); err == nil && env.Error.Message != "" {
		typ := env.Error.Type
		if typ == "" {
			typ = env.Error.Code
		}
		if typ == "" {
			typ = resp.Status
		}
		return fmt.Errorf("api: %s: %s", typ, env.Error.Message)
	}
	return fmt.Errorf("api: %s: %s", resp.Status, strings.TrimSpace(string(raw)))
}
