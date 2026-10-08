package api

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/sse"
)

// convertResponses runs SSE data lines through convertOpenAIResponsesStream.
func convertResponses(data ...string) ([]sse.Event, error) {
	var b strings.Builder
	for _, d := range data {
		fmt.Fprintf(&b, "data: %s\n\n", d)
	}
	reader, writer := io.Pipe()
	go convertOpenAIResponsesStream(io.NopCloser(strings.NewReader(b.String())), writer, "gpt-5")
	return drainConverted(reader)
}

const responsesCompleted = `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":5,"output_tokens":3}}}`

// TestConvertOpenAIResponsesStream_ToolAfterReasoning: a function_call at
// output_index 1, after a reasoning item at 0, must still get its
// content_block_stop. The loop keyed by contiguous call indices never visited
// index 1, so the tool_use block was left open and its EventToolUse never
// fired.
func TestConvertOpenAIResponsesStream_ToolAfterReasoning(t *testing.T) {
	evs, err := convertResponses(
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Bash"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"fc_1","delta":"{\"command\":\"ls\"}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"Bash","arguments":"{\"command\":\"ls\"}"}}`,
		responsesCompleted,
	)
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	for _, ev := range evs {
		if ev.Type != "content_block_stop" {
			continue
		}
		s, err := ev.AsContentBlockStop()
		if err != nil {
			t.Fatal(err)
		}
		stopped = stopped || s.Index == 0
	}
	if !stopped {
		t.Fatalf("tool_use block 0 never got content_block_stop: %q", trace(t, evs))
	}
	want := []string{"start 0 tool_use call_1 Bash", `json 0 {"command":"ls"}`, "stop 0", "end tool_use"}
	if got := trace(t, evs); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("trace = %q\nwant    %q", got, want)
	}
}

func TestConvertOpenAIResponsesStream_Endings(t *testing.T) {
	text := `{"type":"response.output_text.delta","output_index":0,"delta":"hi"}`
	tests := []struct {
		name      string
		data      []string
		want      []string
		truncated bool
		errType   string
		errMsg    string
	}{
		{"completed", []string{text, responsesCompleted},
			[]string{"start 0 text", "text 0 hi", "stop 0", "end end_turn"}, false, "", ""},
		{"done event", []string{text, `{"type":"response.done","response":{"status":"completed"}}`},
			[]string{"start 0 text", "text 0 hi", "stop 0", "end end_turn"}, false, "", ""},
		{"incomplete max tokens", []string{text, `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}}}`},
			[]string{"start 0 text", "text 0 hi", "stop 0", "end max_tokens"}, false, "", ""},
		{"incomplete content filter", []string{text, `{"type":"response.incomplete","response":{"status":"incomplete","incomplete_details":{"reason":"content_filter"}}}`},
			[]string{"start 0 text", "text 0 hi", "stop 0", "end refusal"}, false, "", ""},
		{"EOF before completion", []string{text}, nil, true, "", ""},
		{"DONE before completion", []string{text, "[DONE]"}, nil, true, "", ""},
		{"failed", []string{text, `{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"boom"}}}`},
			nil, false, "api_error", "server_error: boom"},
		{"failed without error object", []string{`{"type":"response.failed","response":{"status":"failed"}}`},
			nil, false, "api_error", "response failed"},
		{"done but failed", []string{`{"type":"response.done","response":{"status":"failed","error":{"code":"rate_limit_exceeded","message":"slow down"}}}`},
			nil, false, "rate_limit_error", "rate_limit_exceeded: slow down"},
		{"top-level error event", []string{text, `{"type":"error","code":"rate_limit_exceeded","message":"Rate limit reached","param":null}`},
			nil, false, "rate_limit_error", "rate_limit_exceeded: Rate limit reached"},
		{"nested error event", []string{`{"type":"error","error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"too long"}}`},
			nil, false, "invalid_request_error", "invalid_request_error context_length_exceeded: too long"},
		{"numeric error code", []string{`{"type":"error","code":503,"message":"busy"}`},
			nil, false, "overloaded_error", "503: busy"},
		{"nested type with top-level message", []string{`{"type":"error","message":"try later","error":{"type":"server_error"}}`},
			nil, false, "api_error", "server_error: try later"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs, err := convertResponses(tt.data...)
			switch {
			case tt.truncated:
				if !errors.Is(err, ErrStreamTruncated) {
					t.Fatalf("err = %v, want ErrStreamTruncated", err)
				}
				for _, ev := range evs {
					if ev.Type == "message_stop" {
						t.Fatal("truncated stream emitted message_stop")
					}
				}
			case tt.errType != "":
				var se *sse.Error
				if !errors.As(err, &se) || se.Type != tt.errType || se.Message != tt.errMsg {
					t.Fatalf("err = %#v, want *sse.Error{%s %q}", err, tt.errType, tt.errMsg)
				}
			default:
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				if got := trace(t, evs); strings.Join(got, "|") != strings.Join(tt.want, "|") {
					t.Errorf("trace = %q\nwant    %q", got, tt.want)
				}
			}
		})
	}
}

func TestOpenAIResponsesRequest_ReasoningEffort(t *testing.T) {
	tests := []struct {
		name     string
		thinking *ThinkingConfig
		want     string // "" means no reasoning object
	}{
		{"off", nil, ""},
		{"low", &ThinkingConfig{Type: "enabled", BudgetTokens: 1000}, "low"},
		{"medium", &ThinkingConfig{Type: "enabled", BudgetTokens: 4000}, "medium"},
		{"high", &ThinkingConfig{Type: "enabled", BudgetTokens: 8000}, "high"},
		{"max", &ThinkingConfig{Type: "enabled", BudgetTokens: 16000}, "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := openAIResponsesRequestFromMessageRequest(&MessageRequest{Model: "gpt-5", Thinking: tt.thinking}, true, Config{})
			got := ""
			if out.Reasoning != nil {
				got = out.Reasoning.Effort
			}
			if got != tt.want {
				t.Errorf("reasoning effort = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOpenAIResponsesInputs_ToolResults(t *testing.T) {
	msg := Message{Role: "user", Content: []ContentBlock{
		{Type: "text", Text: "also check this"},
		{Type: "tool_result", ToolUseID: "call_1", ResultContent: ""},
		{Type: "tool_result", ToolUseID: "call_2", ResultBlocks: []ContentBlock{{Type: "text", Text: "from blocks"}}},
	}}
	got := openAIResponsesInputsFromMessage(msg, Config{}, "m")
	if len(got) != 3 {
		t.Fatalf("items = %+v, want 2 outputs then the text", got)
	}
	if got[0].Type != "function_call_output" || got[0].CallID != "call_1" || got[0].Output != "(no output)" {
		t.Errorf("item 0 = %+v, want call_1 output (no output)", got[0])
	}
	if got[1].Type != "function_call_output" || got[1].Output != "from blocks" {
		t.Errorf("item 1 = %+v, want call_2 output from blocks", got[1])
	}
	if got[2].Role != "user" || got[2].Content[0].Text != "also check this" {
		t.Errorf("item 2 = %+v, want the user text last", got[2])
	}

	assistant := openAIResponsesInputsFromMessage(Message{Role: "assistant", Content: []ContentBlock{
		{Type: "text", Text: "running"},
		{Type: "tool_use", ID: "call_3", Name: "Bash", Input: map[string]any{}},
	}}, Config{}, "m")
	if len(assistant) != 2 || assistant[0].Role != "assistant" || assistant[1].Type != "function_call" {
		t.Errorf("assistant items = %+v, want text then function_call", assistant)
	}
}
