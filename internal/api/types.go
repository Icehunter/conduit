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

	// Extra holds additional native-tool fields (e.g. max_uses, allowed_domains).
	// Keys from Extra are merged into the JSON output via custom MarshalJSON.
	Extra map[string]any `json:"-"`
}

// MarshalJSON serialises ToolDef, merging Extra fields into the top-level object.
func (td ToolDef) MarshalJSON() ([]byte, error) {
	// Build a map with all non-empty fields.
	m := make(map[string]any, 6+len(td.Extra))
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
// Union of text | image | document | tool_use | tool_result | thinking. Fields are set according to Type.
type ContentBlock struct {
	// Common
	Type string `json:"type"` // "text" | "image" | "document" | "tool_use" | "tool_result" | "thinking"

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
	// Content for tool_result: string or []ContentBlock. We send string for simplicity.
	ResultContent string `json:"content,omitempty"`
}

// MarshalJSON serialises ContentBlock, ensuring the `input` field is always
// present for tool_use blocks even when the map is nil or empty.
// encoding/json's omitempty treats nil and empty maps identically (both
// omitted), but the Anthropic API requires `input` on every tool_use block.
func (b ContentBlock) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, 8)
	m["type"] = b.Type
	if b.Text != "" {
		m["text"] = b.Text
	}
	if b.Thinking != "" {
		m["thinking"] = b.Thinking
	}
	if b.Signature != "" {
		m["signature"] = b.Signature
	}
	if b.Source != nil {
		m["source"] = b.Source
	}
	if b.ID != "" {
		m["id"] = b.ID
	}
	if b.Name != "" {
		m["name"] = b.Name
	}
	if b.Type == "tool_use" {
		if b.Input != nil {
			m["input"] = b.Input
		} else {
			m["input"] = map[string]any{}
		}
	}
	if b.ToolUseID != "" {
		m["tool_use_id"] = b.ToolUseID
	}
	if b.IsError {
		m["is_error"] = b.IsError
	}
	if b.ResultContent != "" {
		m["content"] = b.ResultContent
	}
	return json.Marshal(m)
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
