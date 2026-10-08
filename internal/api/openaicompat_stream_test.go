package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/sse"
)

// drainConverted reads converted Anthropic events with the real SSE parser
// and returns them with the error that ended the stream (nil on clean EOF).
func drainConverted(r io.Reader) ([]sse.Event, error) {
	p := sse.NewParser(r)
	var evs []sse.Event
	for {
		ev, err := p.Next()
		if errors.Is(err, io.EOF) {
			return evs, nil
		}
		if err != nil {
			return evs, err
		}
		evs = append(evs, ev)
	}
}

// convertChat runs SSE data lines through convertOpenAIStream.
func convertChat(cfg openAIStreamConfig, data ...string) ([]sse.Event, error) {
	var b strings.Builder
	for _, d := range data {
		fmt.Fprintf(&b, "data: %s\n\n", d)
	}
	reader, writer := io.Pipe()
	go convertOpenAIStream(io.NopCloser(strings.NewReader(b.String())), writer, cfg)
	return drainConverted(reader)
}

// trace renders converted events compactly:
// "start 0 text", "text 0 hi", "think 1 r", "json 2 {}", "stop 0", "end tool_use".
func trace(t *testing.T, evs []sse.Event) []string {
	t.Helper()
	var out []string
	for _, ev := range evs {
		switch ev.Type {
		case "content_block_start":
			cbs, err := ev.AsContentBlockStart()
			if err != nil {
				t.Fatal(err)
			}
			var block struct {
				Type, ID, Name string
				Signature      *string `json:"signature"`
			}
			if err := json.Unmarshal(cbs.ContentBlock, &block); err != nil {
				t.Fatal(err)
			}
			if block.Signature != nil {
				t.Errorf("block %d carries a signature field: %s", cbs.Index, cbs.ContentBlock)
			}
			s := fmt.Sprintf("start %d %s", cbs.Index, block.Type)
			if block.Type == "tool_use" {
				s += " " + block.ID + " " + block.Name
			}
			out = append(out, s)
		case "content_block_delta":
			d, err := ev.AsContentBlockDelta()
			if err != nil {
				t.Fatal(err)
			}
			switch d.Delta.Type {
			case "text_delta":
				out = append(out, fmt.Sprintf("text %d %s", d.Index, d.Delta.Text))
			case "thinking_delta":
				out = append(out, fmt.Sprintf("think %d %s", d.Index, d.Delta.Thinking))
			case "input_json_delta":
				out = append(out, fmt.Sprintf("json %d %s", d.Index, d.Delta.PartialJSON))
			default:
				out = append(out, "delta "+d.Delta.Type)
			}
		case "content_block_stop":
			s, err := ev.AsContentBlockStop()
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, fmt.Sprintf("stop %d", s.Index))
		case "message_delta":
			md, err := ev.AsMessageDelta()
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, "end "+md.Delta.StopReason)
		}
	}
	return out
}

func TestConvertOpenAIStream_Endings(t *testing.T) {
	text := `{"choices":[{"delta":{"content":"hi"},"finish_reason":null}]}`
	finish := func(reason string) string {
		return fmt.Sprintf(`{"choices":[{"delta":{},"finish_reason":%q}]}`, reason)
	}
	usage := `{"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2}}`
	call := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Bash","arguments":"{}"}}]}}]}`
	tests := []struct {
		name      string
		data      []string
		want      []string // trace; nil when the stream must fail
		truncated bool
		errType   string // *sse.Error type
	}{
		{"finish then DONE", []string{text, finish("stop"), usage, "[DONE]"},
			[]string{"start 0 text", "text 0 hi", "stop 0", "end end_turn"}, false, ""},
		{"finish without DONE", []string{text, finish("stop")},
			[]string{"start 0 text", "text 0 hi", "stop 0", "end end_turn"}, false, ""},
		{"DONE without finish", []string{text, "[DONE]"},
			[]string{"start 0 text", "text 0 hi", "stop 0", "end end_turn"}, false, ""},
		{"EOF without finish or DONE", []string{text}, nil, true, ""},
		{"empty body", nil, nil, true, ""},
		{"length", []string{text, finish("length"), "[DONE]"},
			[]string{"start 0 text", "text 0 hi", "stop 0", "end max_tokens"}, false, ""},
		{"uppercase gateway reason", []string{text, finish("MAX_TOKENS"), "[DONE]"},
			[]string{"start 0 text", "text 0 hi", "stop 0", "end max_tokens"}, false, ""},
		{"content_filter is a refusal", []string{text, finish("content_filter"), "[DONE]"},
			[]string{"start 0 text", "text 0 hi", "stop 0", "end refusal"}, false, ""},
		{"network_error", []string{text, finish("network_error")}, nil, false, "api_error"},
		{"error", []string{text, finish("error")}, nil, false, "api_error"},
		{"empty error object ignored", []string{`{"error":{},"choices":[{"delta":{"content":"hi"}}]}`, finish("stop")},
			[]string{"start 0 text", "text 0 hi", "stop 0", "end end_turn"}, false, ""},
		{"stop with tool calls is tool_use", []string{call, finish("stop"), "[DONE]"},
			[]string{"start 0 tool_use call_1 Bash", "json 0 {}", "stop 0", "end tool_use"}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs, err := convertChat(openAIStreamConfig{model: "m"}, tt.data...)
			switch {
			case tt.truncated:
				if !errors.Is(err, ErrStreamTruncated) {
					t.Fatalf("err = %v, want ErrStreamTruncated", err)
				}
				for _, ev := range evs {
					if ev.Type == "message_delta" || ev.Type == "message_stop" {
						t.Fatalf("truncated stream emitted %s", ev.Type)
					}
				}
				if c := Classify(err); c.Class != ClassTransient || c.Reason != ReasonStreamTruncated {
					t.Errorf("Classify = %+v, want transient stream_truncated", c)
				}
			case tt.errType != "":
				var se *sse.Error
				if !errors.As(err, &se) || se.Type != tt.errType {
					t.Fatalf("err = %v, want *sse.Error %s", err, tt.errType)
				}
				if c := Classify(err); !c.Retryable() {
					t.Errorf("Classify = %+v, want retryable", c)
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

func TestConvertOpenAIStream_ErrorChunks(t *testing.T) {
	tests := []struct {
		name      string
		chunk     string
		wantType  string
		wantMsg   []string
		wantClass ErrorClass
	}{
		{"openrouter numeric 429", `{"error":{"code":429,"message":"Rate limit exceeded"}}`,
			"rate_limit_error", []string{"429", "Rate limit exceeded"}, ClassRateLimited},
		{"openrouter 502 with finish error", `{"error":{"code":502,"message":"Provider returned error"},"choices":[{"delta":{"content":""},"finish_reason":"error"}]}`,
			"api_error", []string{"502", "Provider returned error"}, ClassTransient},
		{"string code overflow", `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"too long"}}`,
			"invalid_request_error", []string{"context_length_exceeded", "too long"}, ClassContextOverflow},
		{"llama.cpp n_ctx", `{"error":{"code":500,"type":"exceed_context_size_error","message":"the request exceeds the available context size, try increasing it"}}`,
			"api_error", []string{"exceed_context_size_error"}, ClassContextOverflow},
		{"auth", `{"error":{"code":"invalid_api_key","message":"bad key"}}`,
			"authentication_error", []string{"invalid_api_key"}, ClassAuth},
		{"overloaded", `{"error":{"code":503,"message":"no capacity"}}`,
			"overloaded_error", []string{"503"}, ClassTransient},
		{"bare string", `{"error":"upstream exploded"}`,
			"api_error", []string{"upstream exploded"}, ClassTransient},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evs, err := convertChat(openAIStreamConfig{model: "m"},
				`{"choices":[{"delta":{"content":"partial"}}]}`, tt.chunk, `{"choices":[{"delta":{"content":"never"}}]}`)
			var se *sse.Error
			if !errors.As(err, &se) {
				t.Fatalf("err = %v, want *sse.Error", err)
			}
			if se.Type != tt.wantType {
				t.Errorf("type = %q, want %q", se.Type, tt.wantType)
			}
			for _, part := range tt.wantMsg {
				if !strings.Contains(se.Message, part) {
					t.Errorf("message %q lacks %q", se.Message, part)
				}
			}
			if c := Classify(err); c.Class != tt.wantClass {
				t.Errorf("Classify = %v, want %v", c.Class, tt.wantClass)
			}
			for _, ev := range evs {
				if ev.Type == "message_stop" {
					t.Error("error stream emitted message_stop")
				}
			}
		})
	}
}

func TestConvertOpenAIStream_Reasoning(t *testing.T) {
	var fields []string
	cfg := openAIStreamConfig{model: "m", splitThinkTags: true, onReasoning: func(f string) { fields = append(fields, f) }}
	evs, err := convertChat(cfg,
		`{"choices":[{"delta":{"reasoning_content":"step 1"}}]}`,
		`{"choices":[{"delta":{"reasoning_content":", step 2"}}]}`,
		`{"choices":[{"delta":{"content":"answer"}}]}`,
		`{"choices":[{"delta":{"reasoning":{"not":"a string"},"content":"!"},"finish_reason":"stop"}]}`,
		"[DONE]")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"start 0 thinking", "think 0 step 1", "think 0 , step 2", "stop 0",
		"start 1 text", "text 1 answer", "text 1 !", "stop 1", "end end_turn"}
	if got := trace(t, evs); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("trace = %q\nwant    %q", got, want)
	}
	if strings.Join(fields, ",") != "reasoning_content" {
		t.Errorf("reasoning fields = %q, want [reasoning_content]", fields)
	}

	fields = nil
	evs, err = convertChat(cfg,
		`{"choices":[{"delta":{"reasoning":"openrouter style"}}]}`,
		`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	want = []string{"start 0 thinking", "think 0 openrouter style", "stop 0", "start 1 text", "text 1 ok", "stop 1", "end end_turn"}
	if got := trace(t, evs); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("trace = %q\nwant    %q", got, want)
	}
	if strings.Join(fields, ",") != "reasoning" {
		t.Errorf("reasoning fields = %q, want [reasoning]", fields)
	}
}

func TestConvertOpenAIStream_ThinkTags(t *testing.T) {
	data := []string{
		`{"choices":[{"delta":{"content":"<thi"}}]}`,
		`{"choices":[{"delta":{"content":"nk>plan it</think>\n\nDone: ` + "`<think>`" + ` stays"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"Read","arguments":"{\"p\":1}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"[DONE]",
	}
	evs, err := convertChat(openAIStreamConfig{model: "qwen", splitThinkTags: true}, data...)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"start 0 thinking", "think 0 plan it", "stop 0",
		"start 1 text", "text 1 Done: `<think>` stays", "stop 1",
		"start 2 tool_use c1 Read", `json 2 {"p":1}`, "stop 2", "end tool_use"}
	if got := trace(t, evs); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("trace = %q\nwant    %q", got, want)
	}

	evs, err = convertChat(openAIStreamConfig{model: "gpt"}, data...)
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, line := range trace(t, evs) {
		if after, ok := strings.CutPrefix(line, "text 0 "); ok {
			text.WriteString(after)
		}
	}
	if !strings.HasPrefix(text.String(), "<think>plan it</think>") {
		t.Errorf("with splitting off, text = %q, want the raw tags", text.String())
	}
}

// TestConvertOpenAIStream_ToolBlocksOutOfOrder: tool calls whose indices are
// not 0..n-1 still get closed, each with its own arguments.
func TestConvertOpenAIStream_ToolBlocksOutOfOrder(t *testing.T) {
	evs, err := convertChat(openAIStreamConfig{model: "m"},
		`{"choices":[{"delta":{"content":"checking"}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":3,"id":"a","function":{"name":"A","arguments":""}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"B","arguments":"{\"x\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"2}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		"[DONE]")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"start 0 text", "text 0 checking", "stop 0",
		"start 1 tool_use a A", "start 2 tool_use b B", `json 2 {"x":`, "json 2 2}",
		"json 1 {}", "stop 1", "stop 2", "end tool_use"}
	if got := trace(t, evs); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("trace = %q\nwant    %q", got, want)
	}
}
