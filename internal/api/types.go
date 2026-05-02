package api

// Wire types for /v1/messages.
//
// Field set is intentionally minimal for M1 — just enough to send a message
// and parse the assistant text. Tool use, streaming, thinking, and prompt
// caching land in M2.
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
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// SystemBlock is one block of the multi-block `system` field. Real Claude
// Code sends 4 blocks: a billing/identity marker, a short identity line,
// the main system prompt (cached 1h global), and the output guidance
// (cached 1h). Reference: /tmp/claude-go-capture/real_body.json.
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

// Message is one turn in the conversation.
type Message struct {
	Role    string         `json:"role"` // "user" | "assistant"
	Content []ContentBlock `json:"content"`
}

// ContentBlock is one of: text, tool_use, tool_result, image, document.
// In M1 we only emit text blocks; the type is open so M2 can add fields
// without breaking callers.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
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
	InputTokens             int `json:"input_tokens"`
	OutputTokens            int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens    int `json:"cache_read_input_tokens,omitempty"`
}

// APIErrorEnvelope is the shape Anthropic returns on 4xx/5xx.
type APIErrorEnvelope struct {
	Type  string `json:"type"`
	Error APIError `json:"error"`
}

// APIError is the inner error body. Reference: every Anthropic error response.
type APIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
