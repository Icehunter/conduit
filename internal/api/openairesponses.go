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
	Include         []string                  `json:"include,omitempty"`
	PromptCacheKey  string                    `json:"prompt_cache_key,omitempty"`
}

type openAIResponsesReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// reasoningSignaturePrefix marks a thinking block's Signature as an OpenAI
// Responses reasoning item: prefix, item id, ":", encrypted_content. Other
// providers' signatures never carry it, so replay can tell them apart.
const reasoningSignaturePrefix = "oai-resp:"

func encodeReasoningSignature(id, encrypted string) string {
	return reasoningSignaturePrefix + id + ":" + encrypted
}

func decodeReasoningSignature(sig string) (id, encrypted string, ok bool) {
	rest, found := strings.CutPrefix(sig, reasoningSignaturePrefix)
	if !found {
		return "", "", false
	}
	id, encrypted, ok = strings.Cut(rest, ":")
	return id, encrypted, ok && id != "" && encrypted != ""
}

type openAIResponsesInput struct {
	Type    string                   `json:"type,omitempty"`
	Role    string                   `json:"role,omitempty"`
	Content []openAIResponsesContent `json:"content,omitempty"`
	CallID  string                   `json:"call_id,omitempty"`
	Name    string                   `json:"name,omitempty"`
	Args    string                   `json:"arguments,omitempty"`
	Output  string                   `json:"output,omitempty"`
	// ID, Summary and EncryptedContent make a replayed reasoning item.
	ID               string          `json:"id,omitempty"`
	Summary          json.RawMessage `json:"summary,omitempty"`
	EncryptedContent string          `json:"encrypted_content,omitempty"`
}

type openAIResponsesContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Filename string `json:"filename,omitempty"`
	FileData string `json:"file_data,omitempty"`
}

type openAIResponsesTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type openAIInputTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

type openAIResponsesChunk struct {
	Type        string `json:"type"`
	ItemID      string `json:"item_id"`
	OutputIndex int    `json:"output_index"`
	Delta       string `json:"delta"`
	// Code and Message are the top-level fields of an `error` event; Error is
	// the nested form some servers send instead.
	Code    json.RawMessage  `json:"code"`
	Message string           `json:"message"`
	Error   *openAIErrorBody `json:"error"`
	Item    struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		// EncryptedContent is a reasoning item's opaque replay payload.
		EncryptedContent string `json:"encrypted_content"`
	} `json:"item"`
	Response struct {
		ID                string `json:"id"`
		Model             string `json:"model"`
		Status            string `json:"status"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		Error *openAIErrorBody `json:"error"`
		Usage *struct {
			InputTokens        int                       `json:"input_tokens"`
			OutputTokens       int                       `json:"output_tokens"`
			InputTokensDetails *openAIInputTokensDetails `json:"input_tokens_details"`
		} `json:"usage"`
	} `json:"response"`
}

type openAIResponsesResponse struct {
	ID                string `json:"id"`
	Model             string `json:"model"`
	Status            string `json:"status"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Error  *openAIErrorBody `json:"error"`
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
		Summary []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"summary"`
		EncryptedContent string `json:"encrypted_content"`
	} `json:"output"`
	Usage struct {
		InputTokens        int                       `json:"input_tokens"`
		OutputTokens       int                       `json:"output_tokens"`
		InputTokensDetails *openAIInputTokensDetails `json:"input_tokens_details"`
	} `json:"usage"`
}

func (openAIResponsesTransport) CreateMessage(ctx context.Context, c *Client, req *MessageRequest) (*MessageResponse, error) {
	body, err := json.Marshal(openAIResponsesRequestFromMessageRequest(req, false, c.cfg))
	if err != nil {
		return nil, fmt.Errorf("api: marshal openai-responses request: %w", err)
	}
	extra := c.requestHeaders(req)
	resp, err := c.doWithRetryAndAuth(ctx, func() (*http.Response, error) {
		return c.doOpenAIResponses(ctx, body, extra)
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
	if out.Status == "failed" || (out.Error != nil && len(out.Output) == 0) {
		e := out.Error
		if e == nil {
			e = &openAIErrorBody{Message: "response failed"}
		}
		return nil, fmt.Errorf("api: openai-responses response: %w", &sse.Error{Type: e.anthropicType(), Message: e.describe()})
	}
	blocks := make([]ContentBlock, 0, len(out.Output))
	hasToolUse := false
	for _, item := range out.Output {
		switch item.Type {
		case "reasoning":
			var summary []string
			for _, part := range item.Summary {
				if part.Text != "" {
					summary = append(summary, part.Text)
				}
			}
			block := ContentBlock{Type: "thinking", Thinking: strings.Join(summary, "\n\n")}
			if item.EncryptedContent != "" {
				block.Signature = encodeReasoningSignature(item.ID, item.EncryptedContent)
			}
			if block.Thinking != "" || block.Signature != "" {
				blocks = append(blocks, block)
			}
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
			hasToolUse = true
		}
	}
	if len(blocks) == 0 {
		blocks = append(blocks, ContentBlock{Type: "text"})
	}
	reason := ""
	if out.IncompleteDetails != nil {
		reason = out.IncompleteDetails.Reason
	}
	stopReason := openAIResponsesStopReason(out.Status, reason)
	if stopReason == "end_turn" && hasToolUse {
		stopReason = "tool_use"
	}
	return &MessageResponse{
		ID:         out.ID,
		Type:       "message",
		Role:       "assistant",
		Model:      out.Model,
		Content:    blocks,
		StopReason: stopReason,
		Usage:      responsesUsage(out.Usage.InputTokens, out.Usage.OutputTokens, out.Usage.InputTokensDetails),
	}, nil
}

// responsesUsage maps Responses usage onto Anthropic semantics: input_tokens
// counts only uncached prompt tokens and the cached share is
// cache_read_input_tokens, so PromptInputTokens still sums to the full prompt.
func responsesUsage(input, output int, details *openAIInputTokensDetails) Usage {
	cached := 0
	if details != nil {
		cached = min(max(details.CachedTokens, 0), input)
	}
	return Usage{InputTokens: input - cached, OutputTokens: output, CacheReadInputTokens: cached}
}

func (openAIResponsesTransport) StreamMessage(ctx context.Context, c *Client, req *MessageRequest) (*Stream, error) {
	body, err := json.Marshal(openAIResponsesRequestFromMessageRequest(req, true, c.cfg))
	if err != nil {
		return nil, fmt.Errorf("api: marshal openai-responses stream request: %w", err)
	}

	// See anthropicMessagesTransport.StreamMessage for why streamCtx (not ctx)
	// is what the request must be built with.
	streamCtx, cancel := context.WithCancel(ctx)

	extra := c.requestHeaders(req)
	resp, err := c.doWithRetryAndAuth(streamCtx, func() (*http.Response, error) {
		return c.doOpenAIResponses(streamCtx, body, extra)
	})
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := c.decodeOpenAIError(resp)
		_ = resp.Body.Close()
		cancel()
		return nil, err
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType != "" && !strings.Contains(contentType, "event-stream") {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("api: openai-responses stream: server returned non-SSE Content-Type=%q body=%s",
			contentType, strings.TrimSpace(string(raw)))
	}

	idle := newIdleTimeoutReadCloser(resp.Body, streamIdleTimeout, cancel)
	reader, writer := io.Pipe()
	go convertOpenAIResponsesStream(idle, writer, req.Model)
	return &Stream{
		body:           reader,
		parser:         sse.NewParser(reader),
		ResponseHeader: resp.Header,
		idle:           idle,
	}, nil
}

func (c *Client) doOpenAIResponses(ctx context.Context, body []byte, extra map[string]string) (*http.Response, error) {
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/responses"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("api: build openai-responses request: %w", err)
	}
	c.applyOpenAIHeaders(httpReq.Header, extra)
	resp, err := c.doWithHeaderTimeout(httpReq)
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
	if effort := reasoningEffort(req.Thinking); effort != "" && sendsReasoningEffort(cfg.BaseURL, req.Model) {
		// summary=auto asks for the reasoning summary text that the stream
		// converter shows as thinking.
		out.Reasoning = &openAIResponsesReasoning{Effort: effort, Summary: "auto"}
	}
	if cfg.OpenAIResponsesReplayReasoning && cfg.OpenAIResponsesStore != nil && !*cfg.OpenAIResponsesStore {
		out.Include = []string{"reasoning.encrypted_content"}
	}
	if cfg.OpenAIResponsesPromptCacheKey {
		out.PromptCacheKey = responsesCacheKey(req, cfg)
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
		out.Input = append(out.Input, openAIResponsesInputsFromMessage(msg, cfg, req.Model)...)
	}
	return out
}

// openAIResponsesInputsFromMessage converts one conduit message to Responses
// input items: an assistant turn's text before its function calls, a user
// turn's function_call_output items before its own text.
func openAIResponsesInputsFromMessage(msg Message, cfg Config, model string) []openAIResponsesInput {
	role := msg.Role
	if role == "" {
		role = "user"
	}
	var textParts []string
	var out, reasoning []openAIResponsesInput
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case "thinking":
			if _, enc, ok := decodeReasoningSignature(block.Signature); ok && role == "assistant" && cfg.OpenAIResponsesReplayReasoning {
				reasoning = append(reasoning, openAIResponsesInput{
					// No id: with store=false the item is not stored server-side, and
					// opencode and codex-rs send only the encrypted payload.
					Type: "reasoning", Summary: json.RawMessage("[]"), EncryptedContent: enc,
				})
			}
		case "tool_use":
			out = append(out, openAIResponsesInput{
				Type:   "function_call",
				CallID: block.ID,
				Name:   block.Name,
				Args:   marshalToolArguments(block.Input),
			})
		case "tool_result":
			out = append(out, openAIResponsesInput{
				Type:   "function_call_output",
				CallID: block.ToolUseID,
				Output: toolResultText(block),
			})
		}
	}
	text := strings.Join(textParts, "\n\n")
	if role == "assistant" && isStaleProviderSelfIdentification(text) {
		text = ""
	}
	if role == "assistant" {
		// Reasoning items come first, then the text, then the calls they led to.
		items := reasoning
		if text != "" {
			items = append(items, openAIResponsesInput{
				Role:    role,
				Content: []openAIResponsesContent{{Type: "output_text", Text: text}},
			})
		}
		return append(items, out...)
	}
	media, text := responsesMediaContent(msg.Content, text, cfg, model)
	var content []openAIResponsesContent
	if text != "" {
		content = append(content, openAIResponsesContent{Type: "input_text", Text: text})
	}
	content = append(content, media...)
	if len(content) == 0 {
		return out
	}
	return append(out, openAIResponsesInput{Role: role, Content: content})
}

// responsesCacheKey is the prompt_cache_key: the conduit session of the
// request, else the client's own session id.
func responsesCacheKey(req *MessageRequest, cfg Config) string {
	if req.Hints != nil && req.Hints.SessionID != "" {
		return req.Hints.SessionID
	}
	return cfg.SessionID
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

// convertOpenAIResponsesStream converts a /responses SSE body into Anthropic
// Messages SSE events on writer. The message ends only after
// response.completed, response.incomplete or response.done; a body that stops
// before one fails with ErrStreamTruncated. response.failed and error events
// become an Anthropic error event.
func convertOpenAIResponsesStream(body io.ReadCloser, writer *io.PipeWriter, fallbackModel string) {
	defer func() { _ = body.Close() }()
	if err := runOpenAIResponsesStream(body, anthropicEventWriter{bw: bufio.NewWriter(writer)}, fallbackModel); err != nil {
		_ = writer.CloseWithError(err)
		return
	}
	_ = writer.Close()
}

func runOpenAIResponsesStream(body io.Reader, w anthropicEventWriter, fallbackModel string) error {
	if err := w.messageStart("openai-responses", fallbackModel); err != nil {
		return err
	}
	blocks := newAnthropicBlocks(w)
	tools := map[int]*anthropicToolBlock{} // by output_index
	startTool := func(outputIndex int, id, name string) (*anthropicToolBlock, error) {
		if tb, ok := tools[outputIndex]; ok {
			return tb, nil
		}
		if id == "" {
			id = fmt.Sprintf("call_%d", outputIndex)
		}
		tb, err := blocks.startTool(map[string]any{"type": "tool_use", "id": id, "name": name})
		if err != nil {
			return nil, err
		}
		tools[outputIndex] = tb
		return tb, nil
	}
	stopReason := "end_turn"
	inputTokens, outputTokens := 0, 0
	var cachedTokens int
	terminal := false
	reasoningHasText := false // the open reasoning item has shown summary text

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 8<<20)
	for !terminal && scanner.Scan() {
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
			return fmt.Errorf("api: decode openai-responses stream chunk: %w", err)
		}
		switch chunk.Type {
		case "error":
			e := openAIErrorBody{Code: chunk.Code, Message: chunk.Message}
			if chunk.Error != nil {
				e.Type = chunk.Error.Type
				if len(chunk.Error.Code) > 0 {
					e.Code = chunk.Error.Code
				}
				if chunk.Error.Message != "" {
					e.Message = chunk.Error.Message
				}
			}
			return w.streamError(e.anthropicType(), e.describe())
		case "response.failed":
			return w.streamError(responseFailure(chunk.Response.Error, chunk.Response.Status))
		case "response.output_item.added":
			if chunk.Item.Type == "function_call" {
				id := chunk.Item.CallID
				if id == "" {
					id = chunk.Item.ID
				}
				if _, err := startTool(chunk.OutputIndex, id, chunk.Item.Name); err != nil {
					return err
				}
			}
		case "response.reasoning_summary_part.added":
			// A new summary part continues the same thinking block.
			if reasoningHasText {
				if err := blocks.text("thinking", "\n\n"); err != nil {
					return err
				}
				reasoningHasText = false
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if chunk.Delta != "" {
				reasoningHasText = true
			}
			if err := blocks.text("thinking", chunk.Delta); err != nil {
				return err
			}
		case "response.output_text.delta":
			if err := blocks.text("text", chunk.Delta); err != nil {
				return err
			}
		case "response.function_call_arguments.delta":
			tb, err := startTool(chunk.OutputIndex, chunk.ItemID, "")
			if err != nil {
				return err
			}
			if err := blocks.toolArgs(tb, chunk.Delta); err != nil {
				return err
			}
		case "response.output_item.done":
			if chunk.Item.Type == "reasoning" {
				reasoningHasText = false
				if chunk.Item.EncryptedContent != "" {
					sig := encodeReasoningSignature(chunk.Item.ID, chunk.Item.EncryptedContent)
					if err := blocks.thinkingSignature(sig); err != nil {
						return err
					}
				} else if err := blocks.closeOpen(); err != nil {
					return err
				}
			}
			if chunk.Item.Type == "function_call" {
				id := chunk.Item.CallID
				if id == "" {
					id = chunk.Item.ID
				}
				tb, err := startTool(chunk.OutputIndex, id, chunk.Item.Name)
				if err != nil {
					return err
				}
				if !tb.hasArgs {
					if err := blocks.toolArgs(tb, chunk.Item.Arguments); err != nil {
						return err
					}
				}
			}
		case "response.completed", "response.incomplete", "response.done":
			status := chunk.Response.Status
			if status == "failed" || status == "cancelled" {
				return w.streamError(responseFailure(chunk.Response.Error, status))
			}
			if chunk.Response.Usage != nil {
				u := responsesUsage(chunk.Response.Usage.InputTokens, chunk.Response.Usage.OutputTokens, chunk.Response.Usage.InputTokensDetails)
				inputTokens, outputTokens, cachedTokens = u.InputTokens, u.OutputTokens, u.CacheReadInputTokens
			}
			reason := ""
			if chunk.Response.IncompleteDetails != nil {
				reason = chunk.Response.IncompleteDetails.Reason
			}
			stopReason = openAIResponsesStopReason(status, reason)
			terminal = true
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("api: read openai-responses stream: %w", err)
	}
	if !terminal {
		return fmt.Errorf("api: openai-responses stream: %w", ErrStreamTruncated)
	}
	return blocks.finish(stopReason, map[string]any{
		"input_tokens":            inputTokens,
		"output_tokens":           outputTokens,
		"cache_read_input_tokens": cachedTokens,
	})
}

// responseFailure is the Anthropic error type and message for a Responses
// response that ended as failed or cancelled.
func responseFailure(e *openAIErrorBody, status string) (typ, message string) {
	if e == nil || e.Message == "" {
		e = &openAIErrorBody{Message: "response " + status}
	}
	return e.anthropicType(), e.describe()
}

func openAIResponsesStopReason(status, reason string) string {
	if status == "incomplete" {
		switch reason {
		case "max_output_tokens", "max_tokens":
			return "max_tokens"
		case "content_filter":
			return "refusal"
		}
	}
	return "end_turn"
}
