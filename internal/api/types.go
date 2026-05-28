package api

import "encoding/json"

// Wire types for /v1/messages.
//
// Reference: https://docs.anthropic.com/en/api/messages, decoded chunks
// 0158.js (creator), 0166.js (alt path).

// MessageRequest is the body of POST /v1/messages.
type MessageRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	System    []SystemBlock  `json:"system,omitempty"`
	Messages  []Message      `json:"messages"`
	Stream    bool           `json:"stream,omitempty"`
	Tools     []ToolDef      `json:"tools,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	// Thinking enables extended thinking (interleaved-thinking-2025-05-14 beta).
	// When non-nil, sent as {"type":"enabled","budget_tokens":N}.
	Thinking *ThinkingConfig `json:"thinking,omitempty"`
}

// ThinkingConfig enables the extended thinking feature.
// Reference: interleaved-thinking-2025-05-14 beta, effort-2025-11-24 beta.
type ThinkingConfig struct {
	Type         string `json:"type"`          // always "enabled"
	BudgetTokens int    `json:"budget_tokens"` // token budget for thinking
}

// SystemBlock is one block of the multi-block `system` field.
type SystemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// CacheControl mirrors the prompt-caching directive on system / message blocks.
type CacheControl struct {
	Type  string `json:"type"`            // "ephemeral"
	TTL   string `json:"ttl,omitempty"`   // "5m" | "1h"
	Scope string `json:"scope,omitempty"` // "global" (optional)
}

// ToolDef is a tool declaration in the request `tools` array.
// For standard tools: Name, Description, InputSchema are set; Type is empty (defaults to "custom").
// For native server tools (e.g. web_search_20250305): Type is set; other fields may be empty.
type ToolDef struct {
	// Type is the tool type. Empty means standard custom tool. Examples of
	// native types: "web_search_20250305".
	Type        string         `json:"type,omitempty"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`

	// CacheControl marks the end of a cacheable tool-list prefix when non-nil.
	// Set on the last ToolDef so the system+tools block is cached as a unit.
	CacheControl *CacheControl `json:"cache_control,omitempty"`

	// Extra holds additional native-tool fields (e.g. max_uses, allowed_domains).
	// Keys from Extra are merged into the JSON output via custom MarshalJSON.
	Extra map[string]any `json:"-"`
}

// MarshalJSON serialises ToolDef, merging Extra fields into the top-level object.
func (td ToolDef) MarshalJSON() ([]byte, error) {
	// Build a map with all non-empty fields.
	m := make(map[string]any, 7+len(td.Extra))
	if td.Type != "" {
		m["type"] = td.Type
	}
	if td.Name != "" {
		m["name"] = td.Name
	}
	if td.Description != "" {
		m["description"] = td.Description
	}
	if td.InputSchema != nil {
		m["input_schema"] = td.InputSchema
	}
	if td.CacheControl != nil {
		m["cache_control"] = td.CacheControl
	}
	for k, v := range td.Extra {
		m[k] = v
	}

	type plain map[string]any
	return json.Marshal(plain(m))
}

// Message is one turn in the conversation.
type Message struct {
	Role    string         `json:"role"` // "user" | "assistant"
	Content []ContentBlock `json:"content"`

	// ProviderKind/Provider are conduit-only transcript metadata. They are
	// deliberately excluded from API JSON so resumed local/provider turns stay
	// renderable without sending private routing metadata upstream.
	ProviderKind string `json:"-"`
	Provider     string `json:"-"`
}

// ContentBlock is one block of content in a message.
// Union of text | image | document | tool_use | tool_result | thinking |
// server_tool_use | web_search_tool_result | code_execution_tool_result |
// web_fetch_tool_result. Fields are set according to Type.
type ContentBlock struct {
	// Common
	Type string `json:"type"` // "text" | "image" | "document" | "tool_use" | "tool_result" | "thinking" | "server_tool_use" | "web_search_tool_result" | "code_execution_tool_result" | "web_fetch_tool_result"

	// CacheControl marks this block as a prompt-cache breakpoint when non-nil.
	// Anthropic allows up to 4 breakpoints per request across system + messages.
	CacheControl *CacheControl `json:"cache_control,omitempty"`

	// type=text
	Text string `json:"text,omitempty"`

	// type=thinking (extended thinking block — must be round-tripped to the API verbatim)
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"` // opaque Anthropic signature on the thinking block

	// type=image or type=document (user-sent content — clipboard paste, file attach)
	// For documents: source.media_type = "application/pdf"
	Source *ImageSource `json:"source,omitempty"`

	// type=tool_use (assistant → us)
	ID    string         `json:"id,omitempty"`    // tool use ID, e.g. "toolu_01..."
	Name  string         `json:"name,omitempty"`  // tool name
	Input map[string]any `json:"input,omitempty"` // parsed tool input

	// ThoughtSignature carries the Gemini thought_signature for tool_use blocks.
	// It is NOT sent to the Anthropic API (json:"-") but is round-tripped to
	// Gemini's OpenAI-compatible endpoint on the first tool_call in each turn.
	ThoughtSignature string `json:"-"`

	// type=tool_result (us → assistant, in a user-role message)
	ToolUseID string `json:"tool_use_id,omitempty"` // matches ID from tool_use block
	IsError   bool   `json:"is_error,omitempty"`
	// ResultContent is the string form of tool_result content. Serialized as
	// {"content": "..."} for backward compatibility with simple text results.
	ResultContent string `json:"content,omitempty"`
	// ResultBlocks is the array form of tool_result content, used when the result
	// contains non-text blocks (e.g. images). When set, serialized as
	// {"content": [{...}, ...]} instead of the string form. Takes precedence
	// over ResultContent if both are set.
	ResultBlocks []ContentBlock `json:"-"`

	// type=server_tool_use (assistant messages — server-side tools like web_search).
	// These are NOT dispatched to local tool executors; the server resolves them.
	// Name (above) is reused for the server tool name — both share the "name" JSON key.
	// ServerToolInput holds the raw JSON input; it is serialized via MarshalJSON
	// as "input" when Type == "server_tool_use" (taking precedence over Input).
	ServerToolInput json.RawMessage `json:"-"`

	// type=web_search_tool_result | code_execution_tool_result | web_fetch_tool_result
	// (user-role messages — the API sends these back automatically).
	// ServerContent holds the nested content blocks returned by the server tool.
	// Serialized as "content" by MarshalJSON when IsServerToolResult() is true.
	ServerContent []ContentBlock `json:"-"`
}

// IsServerToolResult reports whether the block is one of the API-managed
// server tool result types: web_search_tool_result, code_execution_tool_result,
// or web_fetch_tool_result. These blocks are sent back by the API automatically
// and must be round-tripped verbatim — conduit does not execute them locally.
func (cb ContentBlock) IsServerToolResult() bool {
	switch cb.Type {
	case "web_search_tool_result", "code_execution_tool_result", "web_fetch_tool_result":
		return true
	}
	return false
}

// MarshalJSON serialises ContentBlock, ensuring the `input` field is always
// present for tool_use blocks even when the map is nil or empty.
// encoding/json's omitempty treats nil and empty maps identically (both
// omitted), but the Anthropic API requires `input` on every tool_use block.
//
// server_tool_use blocks emit "name" and "input" (raw JSON).
// *_tool_result blocks (web_search_tool_result, code_execution_tool_result,
// web_fetch_tool_result) emit "tool_use_id" and "content" (nested blocks).
func (cb ContentBlock) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, 10)
	m["type"] = cb.Type
	if cb.CacheControl != nil {
		m["cache_control"] = cb.CacheControl
	}
	if cb.Text != "" && cb.Type != "tool_result" {
		m["text"] = cb.Text
	}
	if cb.Thinking != "" {
		m["thinking"] = cb.Thinking
	}
	if cb.Signature != "" {
		m["signature"] = cb.Signature
	}
	if cb.Source != nil {
		m["source"] = cb.Source
	}
	if cb.ID != "" {
		m["id"] = cb.ID
	}
	if cb.Name != "" {
		m["name"] = cb.Name
	}
	switch cb.Type {
	case "tool_use":
		if cb.Input != nil {
			m["input"] = cb.Input
		} else {
			m["input"] = map[string]any{}
		}
	case "server_tool_use":
		// Emit raw input JSON; fall back to empty object so the field is always present.
		if len(cb.ServerToolInput) > 0 {
			m["input"] = cb.ServerToolInput
		} else {
			m["input"] = json.RawMessage("{}")
		}
	}
	if cb.ToolUseID != "" {
		m["tool_use_id"] = cb.ToolUseID
	}
	if cb.IsError {
		m["is_error"] = cb.IsError
	}
	switch {
	case cb.IsServerToolResult() && len(cb.ServerContent) > 0:
		// Server tool results carry a nested content array.
		m["content"] = cb.ServerContent
	case len(cb.ResultBlocks) > 0:
		// ResultBlocks (array form) takes precedence over ResultContent (string form)
		// when present, to support tool results that include images or other media.
		m["content"] = cb.ResultBlocks
	case cb.ResultContent != "":
		m["content"] = cb.ResultContent
	}
	return json.Marshal(m)
}

// UnmarshalJSON deserialises ContentBlock. For most types the default struct
// unmarshalling suffices, but server_tool_use needs ServerToolInput captured as
// raw JSON (the "input" field), and *_tool_result types need ServerContent
// decoded as a nested []ContentBlock array.
func (cb *ContentBlock) UnmarshalJSON(data []byte) error {
	// Use a type alias to avoid infinite recursion.
	type plain ContentBlock
	if err := json.Unmarshal(data, (*plain)(cb)); err != nil {
		return err
	}
	switch cb.Type {
	case "server_tool_use":
		// Capture the raw "input" field as ServerToolInput.
		var raw struct {
			Input json.RawMessage `json:"input"`
		}
		if err := json.Unmarshal(data, &raw); err == nil && len(raw.Input) > 0 {
			cb.ServerToolInput = raw.Input
		}
	case "web_search_tool_result", "code_execution_tool_result", "web_fetch_tool_result":
		// Decode the nested "content" field as a []ContentBlock slice.
		var raw struct {
			Content []ContentBlock `json:"content"`
		}
		if err := json.Unmarshal(data, &raw); err == nil {
			cb.ServerContent = raw.Content
		}
	}
	return nil
}

// ImageSource is the source payload for a type=image content block.
// Mirrors the Anthropic API image source shape.
type ImageSource struct {
	Type      string `json:"type"`       // "base64"
	MediaType string `json:"media_type"` // "image/png" | "image/jpeg" | "image/gif" | "image/webp"
	Data      string `json:"data"`       // base64-encoded bytes
}

// MessageResponse is the JSON response shape from a non-streaming Messages call.
type MessageResponse struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Role       string         `json:"role"`
	Model      string         `json:"model"`
	Content    []ContentBlock `json:"content"`
	StopReason string         `json:"stop_reason"`
	Usage      Usage          `json:"usage"`
}

// Usage is the token counter block returned with every response.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// PromptInputTokens returns the total prompt/context size represented by a
// usage block. Anthropic reports cached prompt tokens separately from
// input_tokens, but they still occupy the model context window.
func (u Usage) PromptInputTokens() int {
	return u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// APIErrorEnvelope is the shape Anthropic returns on 4xx/5xx.
type APIErrorEnvelope struct {
	Type  string   `json:"type"`
	Error APIError `json:"error"`
}

// APIError is the inner error body.
type APIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
