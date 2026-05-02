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
}

// ContentBlock is one block of content in a message.
// Union of text | tool_use | tool_result. Fields are set according to Type.
type ContentBlock struct {
	// Common
	Type string `json:"type"` // "text" | "tool_use" | "tool_result"

	// type=text
	Text string `json:"text,omitempty"`

	// type=tool_use (assistant → us)
	ID    string         `json:"id,omitempty"`    // tool use ID, e.g. "toolu_01..."
	Name  string         `json:"name,omitempty"`  // tool name
	Input map[string]any `json:"input,omitempty"` // parsed tool input

	// type=tool_result (us → assistant, in a user-role message)
	ToolUseID string `json:"tool_use_id,omitempty"` // matches ID from tool_use block
	IsError   bool   `json:"is_error,omitempty"`
	// Content for tool_result: string or []ContentBlock. We send string for simplicity.
	ResultContent string `json:"content,omitempty"`
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
