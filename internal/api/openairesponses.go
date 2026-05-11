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

type openAIResponsesRequest struct {
	Model           string                    `json:"model"`
	Instructions    string                    `json:"instructions,omitempty"`
	Input           []openAIResponsesInput    `json:"input"`
	MaxOutputTokens int                       `json:"max_output_tokens,omitempty"`
	Stream          bool                      `json:"stream,omitempty"`
	Store           *bool                     `json:"store,omitempty"`
	Tools           []openAIResponsesTool     `json:"tools,omitempty"`
	Reasoning       *openAIResponsesReasoning `json:"reasoning,omitempty"`
}

type openAIResponsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type openAIResponsesInput struct {
	Type    string                   `json:"type,omitempty"`
	Role    string                   `json:"role,omitempty"`
	Content []openAIResponsesContent `json:"content,omitempty"`
	CallID  string                   `json:"call_id,omitempty"`
	Name    string                   `json:"name,omitempty"`
	Args    string                   `json:"arguments,omitempty"`
	Output  string                   `json:"output,omitempty"`
}

type openAIResponsesContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAIResponsesTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type openAIResponsesChunk struct {
	Type        string `json:"type"`
	ItemID      string `json:"item_id"`
	OutputIndex int    `json:"output_index"`
	Delta       string `json:"delta"`
	Error       struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	Item struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"item"`
	Response struct {
		ID                string `json:"id"`
		Model             string `json:"model"`
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Usage *struct {
			InputTokens        int `json:"input_tokens"`
			OutputTokens       int `json:"output_tokens"`
			InputTokensDetails *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"input_tokens_details"`
		} `json:"usage"`
	} `json:"response"`
}

type openAIResponsesResponse struct {
	ID     string `json:"id"`
	Model  string `json:"model"`
	Status string `json:"status"`
	Output []struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"output"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (openAIResponsesTransport) CreateMessage(ctx context.Context, c *Client, req *MessageRequest) (*MessageResponse, error) {
	body, err := json.Marshal(openAIResponsesRequestFromMessageRequest(req, false, c.cfg))
	if err != nil {
		return nil, fmt.Errorf("api: marshal openai-responses request: %w", err)
	}
	resp, err := c.doWithRetryAndAuth(ctx, func() (*http.Response, error) {
		return c.doOpenAIResponses(ctx, body)
	})
	if err != nil {
		return nil, err
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, c.decodeOpenAIError(resp)
	}
	var out openAIResponsesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("api: decode openai-responses response: %w", err)
	}
	blocks := make([]ContentBlock, 0, len(out.Output))
	for _, item := range out.Output {
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Type == "output_text" && content.Text != "" {
					blocks = append(blocks, ContentBlock{Type: "text", Text: content.Text})
				}
			}
		case "function_call":
			var input map[string]any
			if item.Arguments != "" {
				_ = json.Unmarshal([]byte(item.Arguments), &input)
			}
			if input == nil {
				input = map[string]any{}
			}
			id := item.CallID
			if id == "" {
				id = item.ID
			}
			blocks = append(blocks, ContentBlock{Type: "tool_use", ID: id, Name: item.Name, Input: input})
		}
	}
	if len(blocks) == 0 {
		blocks = append(blocks, ContentBlock{Type: "text"})
	}
	return &MessageResponse{
		ID:         out.ID,
		Type:       "message",
		Role:       "assistant",
		Model:      out.Model,
		Content:    blocks,
		StopReason: openAIResponsesStopReason(out.Status, ""),
		Usage: Usage{
			InputTokens:  out.Usage.InputTokens,
			OutputTokens: out.Usage.OutputTokens,
		},
	}, nil
}

func (openAIResponsesTransport) StreamMessage(ctx context.Context, c *Client, req *MessageRequest) (*Stream, error) {
	body, err := json.Marshal(openAIResponsesRequestFromMessageRequest(req, true, c.cfg))
	if err != nil {
		return nil, fmt.Errorf("api: marshal openai-responses stream request: %w", err)
	}
	resp, err := c.doWithRetryAndAuth(ctx, func() (*http.Response, error) {
		return c.doOpenAIResponses(ctx, body)
	})
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := c.decodeOpenAIError(resp)
		_ = resp.Body.Close()
		return nil, err
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType != "" && !strings.Contains(contentType, "event-stream") {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("api: openai-responses stream: server returned non-SSE Content-Type=%q body=%s",
			contentType, strings.TrimSpace(string(raw)))
	}

	reader, writer := io.Pipe()
	go convertOpenAIResponsesStream(resp.Body, writer, req.Model)
	return &Stream{
		body:           reader,
		parser:         sse.NewParser(reader),
		ResponseHeader: resp.Header,
	}, nil
}

func (c *Client) doOpenAIResponses(ctx context.Context, body []byte) (*http.Response, error) {
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/responses"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("api: build openai-responses request: %w", err)
	}
	c.applyOpenAIHeaders(httpReq.Header)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("api: send openai-responses: %w", err)
	}
	return resp, nil
}

func openAIResponsesRequestFromMessageRequest(req *MessageRequest, stream bool, cfg Config) openAIResponsesRequest {
	out := openAIResponsesRequest{
		Model:  req.Model,
		Stream: stream,
		Tools:  openAIResponsesToolsFromToolDefs(req.Tools),
	}
	if !cfg.OpenAIResponsesOmitMaxOutputTokens {
		out.MaxOutputTokens = req.MaxTokens
	}
	if cfg.OpenAIResponsesStore != nil {
		out.Store = cfg.OpenAIResponsesStore
	}
	if req.Thinking != nil {
		out.Reasoning = &openAIResponsesReasoning{Effort: "medium"}
	}
	if len(req.System) > 0 {
		text := systemText(req.Model, req.System)
		if cfg.OpenAIResponsesSystemAsInstructions {
			out.Instructions = text
		} else {
			out.Input = append(out.Input, openAIResponsesInput{
				Role:    "developer",
				Content: []openAIResponsesContent{{Type: "input_text", Text: text}},
			})
		}
	}
	for _, msg := range req.Messages {
		out.Input = append(out.Input, openAIResponsesInputsFromMessage(msg)...)
	}
	return out
}

func openAIResponsesInputsFromMessage(msg Message) []openAIResponsesInput {
	role := msg.Role
	if role == "" {
		role = "user"
	}
	var textParts []string
	var out []openAIResponsesInput
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case "tool_use":
			out = append(out, openAIResponsesInput{
				Type:   "function_call",
				CallID: block.ID,
				Name:   block.Name,
				Args:   marshalToolArguments(block.Input),
			})
		case "tool_result":
			if block.ResultContent != "" {
				out = append(out, openAIResponsesInput{
					Type:   "function_call_output",
					CallID: block.ToolUseID,
					Output: block.ResultContent,
				})
			}
		}
	}
	text := strings.Join(textParts, "\n\n")
	if role == "assistant" && isStaleProviderSelfIdentification(text) {
		text = ""
	}
	if text != "" {
		contentType := "input_text"
		if role == "assistant" {
			contentType = "output_text"
		}
		out = append([]openAIResponsesInput{{
			Role:    role,
			Content: []openAIResponsesContent{{Type: contentType, Text: text}},
		}}, out...)
	}
	return out
}

func openAIResponsesToolsFromToolDefs(defs []ToolDef) []openAIResponsesTool {
	tools := make([]openAIResponsesTool, 0, len(defs))
	for _, def := range defs {
		if def.Name == "" || def.Type != "" {
			continue
		}
		params := def.InputSchema
		params = normalizeOpenAIToolSchema(params)
		tools = append(tools, openAIResponsesTool{
			Type:        "function",
			Name:        def.Name,
			Description: def.Description,
			Parameters:  params,
		})
	}
	return tools
}

func convertOpenAIResponsesStream(body io.ReadCloser, writer *io.PipeWriter, fallbackModel string) {
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
			"id":          "openai-responses",
			"type":        "message",
			"role":        "assistant",
			"model":       fallbackModel,
			"content":     []any{},
			"stop_reason": nil,
			"usage":       map[string]any{"input_tokens": 0},
		},
	}); err != nil {
		_ = writer.CloseWithError(err)
		return
	}

	stopReason := "end_turn"
	inputTokens := 0
	outputTokens := 0
	nextBlockIndex := 0
	textBlockOpen := false
	textBlockIndex := -1
	toolBlocks := map[int]int{}
	toolIDs := map[int]string{}
	toolArgs := map[int]string{}
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
	startToolBlock := func(outputIndex int, id, name string) error {
		if _, ok := toolBlocks[outputIndex]; ok {
			return nil
		}
		blockIndex := nextBlockIndex
		nextBlockIndex++
		toolBlocks[outputIndex] = blockIndex
		if id == "" {
			id = fmt.Sprintf("call_%d", outputIndex)
		}
		toolIDs[outputIndex] = id
		return writeAnthropicEvent("content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": blockIndex,
			"content_block": map[string]any{
				"type": "tool_use",
				"id":   id,
				"name": name,
			},
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
		var chunk openAIResponsesChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			_ = writer.CloseWithError(fmt.Errorf("api: decode openai-responses stream chunk: %w", err))
			return
		}
		switch chunk.Type {
		case "error":
			msg := chunk.Error.Message
			if msg == "" {
				msg = "openai responses stream error"
			}
			_ = writer.CloseWithError(fmt.Errorf("api: openai-responses stream: %s", msg))
			return
		case "response.output_item.added":
			if chunk.Item.Type == "function_call" {
				id := chunk.Item.CallID
				if id == "" {
					id = chunk.Item.ID
				}
				if err := startToolBlock(chunk.OutputIndex, id, chunk.Item.Name); err != nil {
					_ = writer.CloseWithError(err)
					return
				}
			}
		case "response.output_text.delta":
			if err := startTextBlock(); err != nil {
				_ = writer.CloseWithError(err)
				return
			}
			if err := writeAnthropicEvent("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": textBlockIndex,
				"delta": map[string]any{"type": "text_delta", "text": chunk.Delta},
			}); err != nil {
				_ = writer.CloseWithError(err)
				return
			}
		case "response.function_call_arguments.delta":
			if _, ok := toolBlocks[chunk.OutputIndex]; !ok {
				if err := startToolBlock(chunk.OutputIndex, chunk.ItemID, ""); err != nil {
					_ = writer.CloseWithError(err)
					return
				}
			}
			toolArgs[chunk.OutputIndex] += chunk.Delta
			blockIndex := toolBlocks[chunk.OutputIndex]
			if err := writeAnthropicEvent("content_block_delta", map[string]any{
				"type":  "content_block_delta",
				"index": blockIndex,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": chunk.Delta},
			}); err != nil {
				_ = writer.CloseWithError(err)
				return
			}
		case "response.output_item.done":
			if chunk.Item.Type == "function_call" {
				if _, ok := toolBlocks[chunk.OutputIndex]; !ok {
					id := chunk.Item.CallID
					if id == "" {
						id = chunk.Item.ID
					}
					if err := startToolBlock(chunk.OutputIndex, id, chunk.Item.Name); err != nil {
						_ = writer.CloseWithError(err)
						return
					}
				}
				if toolArgs[chunk.OutputIndex] == "" && chunk.Item.Arguments != "" {
					blockIndex := toolBlocks[chunk.OutputIndex]
					toolArgs[chunk.OutputIndex] = chunk.Item.Arguments
					if err := writeAnthropicEvent("content_block_delta", map[string]any{
						"type":  "content_block_delta",
						"index": blockIndex,
						"delta": map[string]any{"type": "input_json_delta", "partial_json": chunk.Item.Arguments},
					}); err != nil {
						_ = writer.CloseWithError(err)
						return
					}
				}
			}
		case "response.completed", "response.incomplete":
			if chunk.Response.Usage != nil {
				inputTokens = chunk.Response.Usage.InputTokens
				outputTokens = chunk.Response.Usage.OutputTokens
			}
			reason := ""
			if chunk.Response.IncompleteDetails != nil {
				reason = chunk.Response.IncompleteDetails.Reason
			}
			stopReason = openAIResponsesStopReason(chunk.Response.Status, reason)
		}
	}
	if err := scanner.Err(); err != nil {
		_ = writer.CloseWithError(fmt.Errorf("api: read openai-responses stream: %w", err))
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
	if err := writeAnthropicEvent("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": stopReason},
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
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

func openAIResponsesStopReason(status, reason string) string {
	if status == "incomplete" {
		switch reason {
		case "max_output_tokens", "max_tokens":
			return "max_tokens"
		default:
			return "end_turn"
		}
	}
	return "end_turn"
}
