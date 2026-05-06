package localimplementtool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/mcp"
)

type fakeCaller struct {
	name  string
	input map[string]any
	res   mcp.CallResult
}

func (f *fakeCaller) CallTool(_ context.Context, name string, input []byte) (mcp.CallResult, error) {
	f.name = name
	_ = json.Unmarshal(input, &f.input)
	return f.res, nil
}

func TestExecuteCallsConfiguredLocalImplementTool(t *testing.T) {
	caller := &fakeCaller{res: mcp.CallResult{Content: []mcp.ContentBlock{{Type: "text", Text: "diff --git a/main.go b/main.go"}}}}
	lt := New(caller, Config{Server: "local-router", ImplementTool: "local_implement", Model: "qwen3-coder"})

	res, err := lt.Execute(context.Background(), json.RawMessage(`{
		"prompt": "implement fizzbuzz",
		"context": "helper: printLine",
		"files": ["main.go"]
	}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned tool error: %+v", res)
	}
	if caller.name != "mcp__local_router__local_implement" {
		t.Fatalf("called %q, want local implement MCP tool", caller.name)
	}
	prompt, _ := caller.input["prompt"].(string)
	if !strings.Contains(prompt, "implement fizzbuzz") || !strings.Contains(prompt, "main.go") || !strings.Contains(prompt, "helper: printLine") {
		t.Fatalf("prompt = %q, want prompt/files/context", prompt)
	}
	if caller.input["output_format"] != "diff" {
		t.Fatalf("output_format = %#v, want diff", caller.input["output_format"])
	}
	if caller.input["include_review_reminder"] != false {
		t.Fatalf("include_review_reminder = %#v, want false", caller.input["include_review_reminder"])
	}
	if got := res.Content[0].Text; !strings.Contains(got, "diff --git") {
		t.Fatalf("result text = %q, want diff", got)
	}
}

func TestExecuteRejectsEmptyPrompt(t *testing.T) {
	lt := New(&fakeCaller{}, Config{Server: "local-router"})
	res, err := lt.Execute(context.Background(), json.RawMessage(`{"prompt":"   "}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "prompt is required") {
		t.Fatalf("result = %+v, want prompt required tool error", res)
	}
}
