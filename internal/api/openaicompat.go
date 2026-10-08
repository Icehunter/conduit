package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/icehunter/conduit/internal/sse"
)

type openAIChatRequest struct {
	Model           string               `json:"model"`
	Messages        []openAIChatMessage  `json:"messages"`
	MaxTokens       int                  `json:"max_tokens,omitempty"`
	Stream          bool                 `json:"stream,omitempty"`
	StreamOptions   *openAIStreamOptions `json:"stream_options,omitempty"`
	Tools           []openAITool         `json:"tools,omitempty"`
	ReasoningEffort string               `json:"reasoning_effort,omitempty"`
	// ChatTemplateKwargs carries chat-template variables for llama.cpp, vLLM
	// and SGLang; conduit sets only preserve_thinking.
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
}

type openAIStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
	// Parts, when set, is sent as the content array in place of Content: a
	// user message with images or documents.
	Parts []openAIChatPart `json:"-"`
	// ReasoningContent and Reasoning replay an assistant turn's reasoning;
	// at most one is set (reasoningPolicy.field).
	ReasoningContent string           `json:"reasoning_content,omitempty"`
	Reasoning        string           `json:"reasoning,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
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

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type openAIChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role             string           `json:"role"`
			Content          string           `json:"content"`
			ReasoningContent string           `json:"reasoning_content"`
			Reasoning        json.RawMessage  `json:"reasoning"`
			ToolCalls        []openAIToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage openAIUsage      `json:"usage"`
	Error *openAIErrorBody `json:"error,omitempty"`
}

// openAIErrorBody is the error object OpenAI-dialect servers put in error
// responses, stream chunks and Responses events. code is a string on OpenAI
// and a number on OpenRouter and llama.cpp, so it stays raw; some servers send
// the whole error as a bare string, which becomes the message.
type openAIErrorBody struct {
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"`
	Message string          `json:"message"`
}

func (e *openAIErrorBody) UnmarshalJSON(data []byte) error {
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*e = openAIErrorBody{Message: text}
		return nil
	}
	type plain openAIErrorBody
	return json.Unmarshal(data, (*plain)(e))
}

// empty reports whether the object carries nothing, as in `"error": {}`,
// which some servers put on ordinary chunks.
func (e *openAIErrorBody) empty() bool {
	return e.Type == "" && e.Message == "" && e.code() == ""
}

// code returns the error code as text, whether it was sent as a string or a
// number; "" when absent or null.
func (e *openAIErrorBody) code() string {
	raw := bytes.TrimSpace(e.Code)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

// describe renders the error for an error message, keeping the provider's
// type and code: "invalid_request_error context_length_exceeded: …".
func (e *openAIErrorBody) describe() string {
	var label []string
	if e.Type != "" {
		label = append(label, e.Type)
	}
	if code := e.code(); code != "" && code != e.Type {
		label = append(label, code)
	}
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = "provider error"
	}
	if len(label) == 0 {
		return msg
	}
	return strings.Join(label, " ") + ": " + msg
}

// anthropicType maps the error onto the Anthropic error types Classify
// understands. A numeric code is read as an HTTP status, as OpenRouter and
// llama.cpp use it.
func (e *openAIErrorBody) anthropicType() string {
	code := e.code()
	status, _ := strconv.Atoi(code)
	s := strings.ToLower(e.Type + " " + code)
	switch {
	case status == http.StatusTooManyRequests ||
		containsAny(s, "rate_limit", "rate limit", "too_many_requests", "quota", "resource_exhausted"):
		return "rate_limit_error"
	case status == http.StatusUnauthorized || status == http.StatusForbidden ||
		containsAny(s, "authentication", "unauthorized", "invalid_api_key", "permission", "forbidden"):
		return "authentication_error"
	case status == http.StatusServiceUnavailable || status == 529 ||
		containsAny(s, "overloaded", "capacity", "unavailable"):
		return "overloaded_error"
	case status >= 400 && status < 500,
		containsAny(s, "invalid_request", "invalid_prompt", "bad_request", "not_found", "context_length", "unsupported"):
		return "invalid_request_error"
	default:
		return "api_error"
	}
}

func (openAIChatTransport) CreateMessage(ctx context.Context, c *Client, req *MessageRequest) (*MessageResponse, error) {
	extra := c.requestHeaders(req)
	// The body is rebuilt on every send so a retry after a 5xx uses what the
	// client has learned about the server since.
	resp, err := c.doWithRetryAndAuth(ctx, func() (*http.Response, error) {
		body, err := c.openAIChatBody(req, false)
		if err != nil {
			return nil, fmt.Errorf("api: marshal openai-compatible request: %w", err)
		}
		return c.doOpenAIChat(ctx, req, false, body, extra)
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
	if out.Error != nil && !out.Error.empty() && len(out.Choices) == 0 {
		return nil, fmt.Errorf("api: openai-compatible response: %w",
			&sse.Error{Type: out.Error.anthropicType(), Message: out.Error.describe()})
	}
	if req.RequireCompleted && (len(out.Choices) != 1 || out.Choices[0].FinishReason != "stop") {
		return nil, fmt.Errorf("api: bounded request requires explicit stop finish_reason")
	}
	var blocks []ContentBlock
	stopReason := "end_turn"
	if len(out.Choices) > 0 {
		choice := out.Choices[0]
		msg := choice.Message
		var failed bool
		stopReason, failed = openAIStopReason(choice.FinishReason)
		if failed {
			return nil, fmt.Errorf("api: openai-compatible response: %w", openAIFinishError(choice.FinishReason))
		}
		reasoning, field := reasoningDelta(msg.ReasoningContent, msg.Reasoning)
		if field != "" {
			c.noteReasoningField(field)
		}
		visible := msg.Content
		if c.splitsThinkTags() {
			var split thinkSplitter
			var thought []string
			if reasoning != "" {
				thought = append(thought, reasoning)
			}
			var text strings.Builder
			for _, seg := range append(split.feed(msg.Content), split.flush()...) {
				if seg.thinking {
					thought = append(thought, seg.text)
				} else {
					text.WriteString(seg.text)
				}
			}
			reasoning, visible = strings.Join(thought, "\n"), text.String()
		}
		if reasoning != "" {
			blocks = append(blocks, ContentBlock{Type: "thinking", Thinking: reasoning})
		}
		if c.toolSyntax(req.Model) != "" {
			blocks = append(blocks, textToolContentBlocks(visible)...)
			if slices.ContainsFunc(blocks, func(b ContentBlock) bool { return b.Type == "tool_use" }) && stopReason == "end_turn" {
				stopReason = "tool_use"
			}
		} else if visible != "" {
			blocks = append(blocks, ContentBlock{Type: "text", Text: visible})
		}
		if len(msg.ToolCalls) > 0 {
			blocks = append(blocks, openAIToolCallsToContentBlocks(msg.ToolCalls)...)
			if stopReason == "end_turn" {
				stopReason = "tool_use"
			}
		}
	}
	if len(blocks) == 0 {
		blocks = []ContentBlock{{Type: "text", Text: ""}}
	}
	return &MessageResponse{
		ID:         out.ID,
		Type:       "message",
		Role:       "assistant",
		Model:      out.Model,
		Content:    blocks,
		StopReason: stopReason,
		Usage: Usage{
			InputTokens:  out.Usage.PromptTokens,
			OutputTokens: out.Usage.CompletionTokens,
		},
	}, nil
}

func (openAIChatTransport) StreamMessage(ctx context.Context, c *Client, req *MessageRequest) (*Stream, error) {

	// See anthropicMessagesTransport.StreamMessage for why streamCtx (not ctx)
	// is what the request must be built with.
	streamCtx, cancel := context.WithCancel(ctx)

	extra := c.requestHeaders(req)
	resp, err := c.doWithRetryAndAuth(streamCtx, func() (*http.Response, error) {
		body, err := c.openAIChatBody(req, true)
		if err != nil {
			return nil, fmt.Errorf("api: marshal openai-compatible stream request: %w", err)
		}
		return c.doOpenAIChat(streamCtx, req, true, body, extra)
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
	if !strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("api: openai-compatible stream: server returned non-SSE Content-Type=%q body=%s",
			resp.Header.Get("Content-Type"), strings.TrimSpace(string(raw)))
	}

	idle := newIdleTimeoutReadCloser(resp.Body, streamIdleTimeout, cancel)
	reader, writer := io.Pipe()
	go convertOpenAIStream(idle, writer, openAIStreamConfig{
		model:          req.Model,
		splitThinkTags: c.splitsThinkTags(),
		implicitThink:  c.splitsThinkTags() && isLocalBaseURL(c.cfg.BaseURL) && reasoningEffort(req.Thinking) != "" && !extraBodyDisablesThinking(c.cfg.ExtraBody),
		onReasoning:    c.noteReasoningField,
		textTools:      c.toolSyntax(req.Model) != "",
	})
	return &Stream{
		body:           reader,
		parser:         sse.NewParser(reader),
		ResponseHeader: resp.Header,
		idle:           idle,
	}, nil
}

// openAIChatBody builds the /chat/completions body for req with the client's
// reasoning policy and ExtraBody applied.
func (c *Client) openAIChatBody(req *MessageRequest, stream bool) ([]byte, error) {
	cfg := c.cfg
	cfg.ToolCallFormat = c.toolSyntax(req.Model)
	out := openAIRequestFromMessageRequest(req, stream, c.reasoningPolicy(req.Model), cfg)
	if effort := reasoningEffort(req.Thinking); effort != "" && !extraBodyDisablesThinking(c.cfg.ExtraBody) &&
		sendsReasoningEffort(c.cfg.BaseURL, req.Model) && !c.rejectsEffort(req.Model) {
		out.ReasoningEffort = effort
	}
	return marshalOpenAIRequest(out, c.cfg.ExtraBody)
}

// toolSyntax is the in-text tool syntax to use for model, "" for native
// tools. Under auto it is hermes once the server has rejected tools for the
// model.
func (c *Client) toolSyntax(model string) string {
	format := normalizeToolCallFormat(c.cfg.ToolCallFormat)
	if format == ToolCallFormatAuto {
		c.mu.Lock()
		rejected := c.noTools[model]
		c.mu.Unlock()
		if !rejected {
			return ""
		}
		return ToolCallFormatHermes
	}
	return textToolSyntax(format)
}

func (c *Client) rejectsEffort(model string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.noEffort[model]
}

// doOpenAIChat sends body. If the server answers 400 and names the reasoning
// parameter, the model is remembered as not taking it; under tool format auto,
// a 400 or 422 saying tools are unsupported makes the model use in-text tools.
// The request is then rebuilt and sent again, at most once per cause. What to
// retry depends on what this body carried, not on what the client has learned
// since: a body built before a concurrent request got the same rejection is
// retried too.
func (c *Client) doOpenAIChat(ctx context.Context, req *MessageRequest, stream bool, body []byte, extra map[string]string) (*http.Response, error) {
	effortRetried, toolsRetried := false, false
	for {
		resp, err := c.doOpenAI(ctx, body, extra)
		if err != nil || (resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnprocessableEntity) {
			return resp, err
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		effort := !effortRetried && resp.StatusCode == http.StatusBadRequest &&
			bytes.Contains(body, []byte(`"reasoning_effort"`)) && mentionsReasoningEffort(raw)
		tools := !toolsRetried && bytes.Contains(body, []byte(`"tools"`)) &&
			normalizeToolCallFormat(c.cfg.ToolCallFormat) == ToolCallFormatAuto && mentionsToolsUnsupported(raw)
		if !effort && !tools {
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			return resp, nil
		}
		c.mu.Lock()
		if effort {
			effortRetried = true
			if c.noEffort == nil {
				c.noEffort = map[string]bool{}
			}
			c.noEffort[req.Model] = true
		}
		if tools {
			toolsRetried = true
			if c.noTools == nil {
				c.noTools = map[string]bool{}
			}
			c.noTools[req.Model] = true
		}
		c.mu.Unlock()
		if body, err = c.openAIChatBody(req, stream); err != nil {
			return nil, fmt.Errorf("api: marshal openai-compatible request: %w", err)
		}
	}
}

// marshalOpenAIRequest marshals req and applies extra on top as a JSON merge
// patch (RFC 7396). extra lets vendor/model-specific fields (e.g. llama.cpp/LM
// Studio's `chat_template_kwargs`) ride along without the core request struct
// having to model every backend's extensions: nested objects merge key by key,
// any other value from extra replaces the generated one, and a null in extra
// deletes the generated field.
func marshalOpenAIRequest(req openAIChatRequest, extra map[string]any) ([]byte, error) {
	if len(extra) == 0 {
		return json.Marshal(req)
	}
	base, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	var merged map[string]any
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	return json.Marshal(mergePatch(merged, extra))
}

// mergePatch applies patch to target as RFC 7396 describes and returns the
// result. patch is read, never modified.
func mergePatch(target, patch map[string]any) map[string]any {
	if target == nil {
		target = map[string]any{}
	}
	for k, v := range patch {
		switch pv := v.(type) {
		case nil:
			delete(target, k)
		case map[string]any:
			tv, _ := target[k].(map[string]any)
			target[k] = mergePatch(tv, pv)
		default:
			target[k] = v
		}
	}
	return target
}

func (c *Client) doOpenAI(ctx context.Context, body []byte, extra map[string]string) (*http.Response, error) {
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("api: build openai-compatible request: %w", err)
	}
	c.applyOpenAIHeaders(httpReq.Header, extra)
	resp, err := c.doWithHeaderTimeout(httpReq)
	if err != nil {
		return nil, fmt.Errorf("api: send openai-compatible: %w", err)
	}
	return resp, nil
}

// applyOpenAIHeaders sets the headers of an OpenAI-dialect request; extra, the
// call's requestHeaders result, goes last.
func (c *Client) applyOpenAIHeaders(h http.Header, extra map[string]string) {
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
	for k, v := range extra {
		h.Set(k, v)
	}
}

func openAIRequestFromMessageRequest(req *MessageRequest, stream bool, policy reasoningPolicy, cfg Config) openAIChatRequest {
	out := openAIChatRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		Stream:    stream,
	}
	if stream {
		out.StreamOptions = &openAIStreamOptions{IncludeUsage: true}
	}
	syntax := textToolSyntax(cfg.ToolCallFormat)
	system := ""
	if len(req.System) > 0 {
		system = systemText(req.Model, req.System)
	}
	if syntax != "" {
		if prompt := textToolsPrompt(syntax, req.Tools); prompt != "" {
			system = strings.TrimLeft(system+"\n\n"+prompt, "\n")
		}
	}
	if system != "" {
		out.Messages = append(out.Messages, openAIChatMessage{Role: "system", Content: system})
	}
	for _, msg := range req.Messages {
		if syntax != "" {
			msg = textToolMessage(msg)
		}
		out.Messages = append(out.Messages, openAIMessagesFromMessage(msg, policy, cfg, req.Model)...)
	}
	if syntax == "" {
		out.Tools = openAIToolsFromToolDefs(req.Tools)
	}
	if policy.preserveThinking {
		out.ChatTemplateKwargs = map[string]any{"preserve_thinking": true}
	}
	return out
}

func openAIToolsFromToolDefs(defs []ToolDef) []openAITool {
	tools := make([]openAITool, 0, len(defs))
	for _, def := range defs {
		if def.Name == "" || def.Type != "" {
			continue
		}
		params := def.InputSchema
		params = normalizeOpenAIToolSchema(params)
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

func normalizeOpenAIToolSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	out := make(map[string]any, len(schema)+1)
	for k, v := range schema {
		out[k] = normalizeOpenAIToolSchemaValue(v)
	}
	if out["type"] == "object" {
		if _, ok := out["properties"]; !ok {
			out["properties"] = map[string]any{}
		}
	}
	return out
}

func normalizeOpenAIToolSchemaValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		return normalizeOpenAIToolSchema(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = normalizeOpenAIToolSchemaValue(item)
		}
		return out
	default:
		return v
	}
}

// openAIMessagesFromMessage converts one conduit message to chat messages. A
// user message's tool results come first, as role:tool messages right after
// the assistant turn that called them, then its own text. An assistant turn
// carries its unsigned reasoning when policy replays it; signed thinking is
// Anthropic's and never leaves that wire.
func openAIMessagesFromMessage(msg Message, policy reasoningPolicy, cfg Config, model string) []openAIChatMessage {
	role := msg.Role
	if role == "" {
		role = "user"
	}
	var textParts, reasoningParts []string
	var toolCalls []openAIToolCall
	var out []openAIChatMessage
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				textParts = append(textParts, block.Text)
			}
		case "thinking":
			if block.Signature == "" && strings.TrimSpace(block.Thinking) != "" {
				reasoningParts = append(reasoningParts, block.Thinking)
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
			out = append(out, openAIChatMessage{
				Role:       "tool",
				Content:    toolResultText(block),
				ToolCallID: block.ToolUseID,
			})
		}
	}
	text := strings.Join(textParts, "\n\n")
	if role != "assistant" {
		parts, text := chatMediaParts(msg.Content, text, cfg, model)
		switch {
		case parts != nil:
			out = append(out, openAIChatMessage{Role: role, Parts: parts})
		case text != "":
			out = append(out, openAIChatMessage{Role: role, Content: text})
		}
		return out
	}
	if isStaleProviderSelfIdentification(text) {
		text = ""
	}
	// A turn with neither text nor tool calls is left out even when it has
	// reasoning: llama.cpp rejects an assistant message without content or
	// tool_calls.
	if text == "" && len(toolCalls) == 0 {
		return out
	}
	assistant := openAIChatMessage{Role: "assistant", Content: text, ToolCalls: toolCalls}
	if reasoning := strings.Join(reasoningParts, "\n"); reasoning != "" && policy.replays(len(toolCalls) > 0) {
		if policy.field == "reasoning" {
			assistant.Reasoning = reasoning
		} else {
			assistant.ReasoningContent = reasoning
		}
	}
	return append([]openAIChatMessage{assistant}, out...)
}

// toolResultText is a tool result's content for OpenAI-dialect wires: the
// string form, else the text of its blocks, else "(no output)". A call must
// always get a result message; servers reject a call left without one.
func toolResultText(block ContentBlock) string {
	if block.ResultContent != "" {
		return block.ResultContent
	}
	var parts []string
	for _, b := range block.ResultBlocks {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n\n")
	}
	return "(no output)"
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

// claudeIdentityPrompts are the Claude Code identity system blocks: conduit's
// own (agent.MinimalIdentitySystem) and the ones Claude Code itself sends.
// They describe the Anthropic product, so other providers' models never see
// them.
var claudeIdentityPrompts = []string{
	"You are a Claude agent, built on Anthropic's Claude Agent SDK.",
	"You are Claude Code, Anthropic's official CLI for Claude.",
	"You are Claude Code, Anthropic's official CLI for Claude, running within the Claude Agent SDK.",
}

// isAnthropicOnlySystemBlock reports whether a system block exists only for
// the Anthropic wire: the billing-header block or an exact Claude Code
// identity block. Everything else, including the agent prompt and CLAUDE.md
// instructions that mention Claude Code or CLAUDE.md, reaches every provider.
func isAnthropicOnlySystemBlock(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "x-anthropic-billing-header:") || slices.Contains(claudeIdentityPrompts, text)
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

// decodeOpenAIError turns a non-2xx OpenAI-dialect response into an
// *APIStatusError carrying the status and retry headers. Type is the
// provider's error type, or its code when that is the only label; the message
// keeps any other code so Classify sees context-overflow codes. A numeric code
// that repeats the HTTP status (OpenRouter, llama.cpp) is dropped.
func (c *Client) decodeOpenAIError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	rl := rateLimitSuffix(resp, nil)
	e := newStatusError(resp, raw, rl)
	var env struct {
		Error *openAIErrorBody `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err != nil || env.Error == nil || env.Error.Message == "" {
		return e
	}
	code := env.Error.code()
	if code == strconv.Itoa(resp.StatusCode) {
		code = ""
	}
	typ := env.Error.Type
	if typ == "" {
		typ = code
	}
	msg := env.Error.Message
	if code != "" && code != typ {
		msg = code + ": " + msg
	}
	if typ == "usage_not_included" || code == "usage_not_included" {
		msg = "To use Codex with your ChatGPT plan, upgrade to Plus: https://chatgpt.com/explore/plus"
	}
	e.Type, e.Message = typ, msg
	status := fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	if typ == "" {
		e.text = fmt.Sprintf("api: %s: %s%s", status, msg, rl)
	} else {
		e.text = fmt.Sprintf("api: %s: %s: %s%s", status, typ, msg, rl)
	}
	return e
}
