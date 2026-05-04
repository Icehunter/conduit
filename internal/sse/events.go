package sse

import (
	"encoding/json"
	"fmt"
)

// Typed event-data structures for the small set of Anthropic event types
// we care about in M2. Adding more is mechanical — declare a struct, add
// an As… helper. Reference: real_sse.txt + Anthropic Messages API docs.

// MessageStartEvent is the first event in every stream.
type MessageStartEvent struct {
	Type    string      `json:"type"`
	Message MessageMeta `json:"message"`
}

// MessageMeta is the message envelope inside message_start.
type MessageMeta struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Role       string            `json:"role"`
	Model      string            `json:"model"`
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      Usage             `json:"usage"`
}

// Usage tracks input/output tokens. cache_* fields are present when prompt
// caching is in play.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// ContentBlockStartEvent — the start of a new content block.
type ContentBlockStartEvent struct {
	Type         string          `json:"type"`
	Index        int             `json:"index"`
	ContentBlock json.RawMessage `json:"content_block"`
}

// ContentBlockDeltaEvent — one delta to an in-progress content block.
type ContentBlockDeltaEvent struct {
	Type  string       `json:"type"`
	Index int          `json:"index"`
	Delta ContentDelta `json:"delta"`
}

// ContentDelta is one of: text_delta (most common), input_json_delta
// (tool inputs streaming in), thinking_delta (extended thinking).
type ContentDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`         // text_delta
	PartialJSON string `json:"partial_json,omitempty"` // input_json_delta
	Thinking    string `json:"thinking,omitempty"`     // thinking_delta
}

// ContentBlockStopEvent — content block closed.
type ContentBlockStopEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

// MessageDeltaEvent — final usage / stop info.
type MessageDeltaEvent struct {
	Type  string       `json:"type"`
	Delta MessageDelta `json:"delta"`
	Usage Usage        `json:"usage"`
}

// MessageDelta carries the stop reason at message close.
type MessageDelta struct {
	StopReason   string `json:"stop_reason"`
	StopSequence string `json:"stop_sequence,omitempty"`
}

// AsContentBlockDelta decodes ev into a ContentBlockDeltaEvent.
// Returns an error when the event type doesn't match.
func (ev Event) AsContentBlockDelta() (*ContentBlockDeltaEvent, error) {
	if ev.Type != "content_block_delta" {
		return nil, fmt.Errorf("sse: AsContentBlockDelta on event type %q", ev.Type)
	}
	var out ContentBlockDeltaEvent
	if err := json.Unmarshal(ev.RawData, &out); err != nil {
		return nil, fmt.Errorf("sse: decode content_block_delta: %w", err)
	}
	return &out, nil
}

// AsMessageStart decodes ev into a MessageStartEvent.
func (ev Event) AsMessageStart() (*MessageStartEvent, error) {
	if ev.Type != "message_start" {
		return nil, fmt.Errorf("sse: AsMessageStart on event type %q", ev.Type)
	}
	var out MessageStartEvent
	if err := json.Unmarshal(ev.RawData, &out); err != nil {
		return nil, fmt.Errorf("sse: decode message_start: %w", err)
	}
	return &out, nil
}

// AsContentBlockStart decodes ev into a ContentBlockStartEvent.
func (ev Event) AsContentBlockStart() (*ContentBlockStartEvent, error) {
	if ev.Type != "content_block_start" {
		return nil, fmt.Errorf("sse: AsContentBlockStart on event type %q", ev.Type)
	}
	var out ContentBlockStartEvent
	if err := json.Unmarshal(ev.RawData, &out); err != nil {
		return nil, fmt.Errorf("sse: decode content_block_start: %w", err)
	}
	return &out, nil
}

// AsContentBlockStop decodes ev into a ContentBlockStopEvent.
func (ev Event) AsContentBlockStop() (*ContentBlockStopEvent, error) {
	if ev.Type != "content_block_stop" {
		return nil, fmt.Errorf("sse: AsContentBlockStop on event type %q", ev.Type)
	}
	var out ContentBlockStopEvent
	if err := json.Unmarshal(ev.RawData, &out); err != nil {
		return nil, fmt.Errorf("sse: decode content_block_stop: %w", err)
	}
	return &out, nil
}

// AsMessageDelta decodes ev into a MessageDeltaEvent.
func (ev Event) AsMessageDelta() (*MessageDeltaEvent, error) {
	if ev.Type != "message_delta" {
		return nil, fmt.Errorf("sse: AsMessageDelta on event type %q", ev.Type)
	}
	var out MessageDeltaEvent
	if err := json.Unmarshal(ev.RawData, &out); err != nil {
		return nil, fmt.Errorf("sse: decode message_delta: %w", err)
	}
	return &out, nil
}
