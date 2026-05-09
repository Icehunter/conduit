package api

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// TestGeminiThoughtSignatureNonStreaming verifies that openAIToolCallsToContentBlocks
// propagates thought_signature from Gemini's extra_content field.
func TestGeminiThoughtSignatureNonStreaming(t *testing.T) {
	calls := []openAIToolCall{
		{
			ID:   "call_abc",
			Type: "function",
			Function: openAIToolFunction{
				Name:      "Glob",
				Arguments: `{"pattern":"**/*.go"}`,
			},
			ExtraContent: &openAIExtraContent{
				Google: &openAIExtraContentGoogle{
					ThoughtSignature: "sig_xyz123",
				},
			},
		},
		{
			ID:   "call_def",
			Type: "function",
			Function: openAIToolFunction{
				Name:      "Read",
				Arguments: `{"file_path":"/tmp/foo.go"}`,
			},
		},
	}

	blocks := openAIToolCallsToContentBlocks(calls)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	if blocks[0].ThoughtSignature != "sig_xyz123" {
		t.Errorf("first block ThoughtSignature = %q; want %q", blocks[0].ThoughtSignature, "sig_xyz123")
	}
	if blocks[1].ThoughtSignature != "" {
		t.Errorf("second block ThoughtSignature = %q; want empty", blocks[1].ThoughtSignature)
	}
}

// TestGeminiThoughtSignatureRoundTrip verifies that a ContentBlock with a
// ThoughtSignature is re-serialized into extra_content.google.thought_signature
// when converting back to OpenAI messages.
func TestGeminiThoughtSignatureRoundTrip(t *testing.T) {
	msg := Message{
		Role: "assistant",
		Content: []ContentBlock{
			{
				Type:             "tool_use",
				ID:               "call_abc",
				Name:             "Glob",
				Input:            map[string]any{"pattern": "**/*.go"},
				ThoughtSignature: "sig_roundtrip",
			},
		},
	}

	msgs := openAIMessagesFromMessage(msg)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(msgs[0].ToolCalls))
	}
	tc := msgs[0].ToolCalls[0]
	if tc.ExtraContent == nil {
		t.Fatal("ExtraContent is nil; want non-nil")
	}
	if tc.ExtraContent.Google == nil {
		t.Fatal("ExtraContent.Google is nil; want non-nil")
	}
	if tc.ExtraContent.Google.ThoughtSignature != "sig_roundtrip" {
		t.Errorf("ThoughtSignature = %q; want %q", tc.ExtraContent.Google.ThoughtSignature, "sig_roundtrip")
	}

	// Verify JSON marshalling includes extra_content.
	raw, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("marshal tool call: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["extra_content"]; !ok {
		t.Errorf("extra_content missing from JSON: %s", raw)
	}
}

// TestGeminiThoughtSignatureNoExtraContent verifies that tool calls without a
// signature are serialized without extra_content (no spurious null field).
func TestGeminiThoughtSignatureNoExtraContent(t *testing.T) {
	msg := Message{
		Role: "assistant",
		Content: []ContentBlock{
			{
				Type:  "tool_use",
				ID:    "call_abc",
				Name:  "Glob",
				Input: map[string]any{"pattern": "**/*.go"},
			},
		},
	}

	msgs := openAIMessagesFromMessage(msg)
	if len(msgs[0].ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call")
	}
	if msgs[0].ToolCalls[0].ExtraContent != nil {
		t.Errorf("ExtraContent should be nil when no signature present")
	}

	raw, err := json.Marshal(msgs[0].ToolCalls[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "extra_content") {
		t.Errorf("extra_content should not appear in JSON when absent: %s", raw)
	}
}

// TestGeminiThoughtSignatureStreamingCapture verifies that convertOpenAIStream
// captures thought_signature from a streaming tool_call delta and embeds it in
// the synthetic content_block_start event so loopstream can pick it up.
func TestGeminiThoughtSignatureStreamingCapture(t *testing.T) {
	// Gemini streaming SSE: one tool call with thought_signature on the first delta.
	sseInput := strings.Join([]string{
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Glob","arguments":""},"extra_content":{"google":{"thought_signature":"sig_stream"}}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"pattern\":\"**/*.go\"}"}}]}}]}`,
		`data: {"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")

	body := io.NopCloser(strings.NewReader(sseInput))
	reader, writer := io.Pipe()

	go convertOpenAIStream(body, writer, "gemini-2.5-flash")

	// Collect all synthetic Anthropic SSE events.
	events := collectSSEEvents(t, reader)

	// Find the content_block_start for the tool_use block.
	var toolBlockStart map[string]json.RawMessage
	for _, ev := range events {
		if ev["type"] != nil {
			var typ string
			_ = json.Unmarshal(ev["type"], &typ)
			if typ == "content_block_start" {
				toolBlockStart = ev
				break
			}
		}
	}
	if toolBlockStart == nil {
		t.Fatal("no content_block_start event found")
	}

	var cb map[string]json.RawMessage
	if err := json.Unmarshal(toolBlockStart["content_block"], &cb); err != nil {
		t.Fatalf("unmarshal content_block: %v", err)
	}
	var sig string
	if err := json.Unmarshal(cb["thought_signature"], &sig); err != nil {
		t.Fatalf("thought_signature missing or not a string: %v — raw content_block: %s", err, toolBlockStart["content_block"])
	}
	if sig != "sig_stream" {
		t.Errorf("thought_signature = %q; want %q", sig, "sig_stream")
	}
}

// collectSSEEvents drains an Anthropic SSE event stream and returns each parsed data payload.
func collectSSEEvents(t *testing.T, r io.Reader) []map[string]json.RawMessage {
	t.Helper()
	var events []map[string]json.RawMessage
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &m); err != nil {
			continue
		}
		events = append(events, m)
	}
	return events
}
