package localhelpertool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/tool"
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

func directToolDef() mcp.ToolDef {
	return mcp.ToolDef{Name: defaultDirectTool}
}

func fileAwareDirectToolDef() mcp.ToolDef {
	return mcp.ToolDef{
		Name:        defaultDirectTool,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"prompt":{},"files":{},"root":{}}}`),
	}
}

func TestExecuteCallsConfiguredLocalDirectTool(t *testing.T) {
	caller := &fakeCaller{res: mcp.CallResult{Content: []mcp.ContentBlock{{Type: "text", Text: "summary text"}}}}
	lt := New(caller, Config{Server: "local-helper", DirectTool: "local_direct", Model: "qwen-cpu-helper"})

	res, err := lt.Execute(context.Background(), json.RawMessage(`{
		"task": "summarize",
		"prompt": "very long log text..."
	}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned tool error: %+v", res)
	}
	if caller.name != "mcp__local_helper__local_direct" {
		t.Fatalf("called %q, want local direct MCP tool", caller.name)
	}
	if caller.input["task"] != "summarize" {
		t.Fatalf("task = %#v, want summarize", caller.input["task"])
	}
	if got := res.Content[0].Text; !strings.Contains(got, "summary text") {
		t.Fatalf("result text = %q", got)
	}
}

func TestExecuteRejectsMissingTask(t *testing.T) {
	lt := New(&fakeCaller{}, Config{Server: "local-helper"})
	res, err := lt.Execute(context.Background(), json.RawMessage(`{"prompt":"x"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "task is required") {
		t.Fatalf("result = %+v, want task required error", res)
	}
}

func TestExecuteRejectsEmptyPrompt(t *testing.T) {
	lt := New(&fakeCaller{}, Config{Server: "local-helper"})
	res, err := lt.Execute(context.Background(), json.RawMessage(`{"task":"summarize","prompt":"  "}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "prompt is required") {
		t.Fatalf("result = %+v, want prompt required error", res)
	}
}

func TestExecuteRejectsClassifyWithoutExamples(t *testing.T) {
	lt := New(&fakeCaller{}, Config{Server: "local-helper"})
	res, err := lt.Execute(context.Background(), json.RawMessage(`{"task":"classify","prompt":"is this a bug?"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !res.IsError || !strings.Contains(res.Content[0].Text, "requires at least two examples") {
		t.Fatalf("result = %+v, want classify-needs-examples error", res)
	}
}

func TestExecuteClassifyWithExamples(t *testing.T) {
	caller := &fakeCaller{res: mcp.CallResult{Content: []mcp.ContentBlock{{Type: "text", Text: "TRIVIAL"}}}}
	lt := New(caller, Config{Server: "local-helper"})

	res, err := lt.Execute(context.Background(), json.RawMessage(`{
		"task": "classify",
		"prompt": "typo in comment",
		"examples": [
			{"input": "typo fix", "output": "TRIVIAL"},
			{"input": "add auth system", "output": "NONTRIVIAL"}
		]
	}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned tool error: %+v", res)
	}
	examples, _ := caller.input["examples"].([]any)
	if len(examples) != 2 {
		t.Fatalf("examples = %#v, want 2 entries", caller.input["examples"])
	}
}

func TestResolveConfigPrefersHelperTierDeterministically(t *testing.T) {
	for i := 0; i < 20; i++ {
		manager := mcp.NewManagerWithServers(map[string]*mcp.ConnectedServer{
			"local-coder": {
				Name:   "local-coder",
				Status: mcp.StatusConnected,
				Config: mcp.ServerConfig{Env: map[string]string{"LOCAL_LLM_TIER": "coder", "LOCAL_LLM_MODEL": "qwen3.8-27b"}},
				Tools:  []mcp.ToolDef{directToolDef()},
			},
			"local-helper": {
				Name:   "local-helper",
				Status: mcp.StatusConnected,
				Config: mcp.ServerConfig{Env: map[string]string{"LOCAL_LLM_TIER": "helper", "LOCAL_LLM_MODEL": "qwen-cpu-helper"}},
				Tools:  []mcp.ToolDef{directToolDef()},
			},
		})
		cfg, ok := ResolveConfig(manager)
		if !ok {
			t.Fatalf("ResolveConfig() ok = false, want true")
		}
		if cfg.Server != "local-helper" {
			t.Fatalf("ResolveConfig().Server = %q, want local-helper (iteration %d)", cfg.Server, i)
		}
	}
}

func TestResolveConfigDetectsFileSupport(t *testing.T) {
	manager := mcp.NewManagerWithServers(map[string]*mcp.ConnectedServer{
		"local-helper": {
			Name:   "local-helper",
			Status: mcp.StatusConnected,
			Config: mcp.ServerConfig{Env: map[string]string{"LOCAL_LLM_TIER": "helper"}},
			Tools:  []mcp.ToolDef{fileAwareDirectToolDef()},
		},
	})
	cfg, ok := ResolveConfig(manager)
	if !ok {
		t.Fatalf("ResolveConfig() ok = false, want true")
	}
	if !cfg.SupportsFiles {
		t.Fatalf("cfg.SupportsFiles = false, want true for a tool schema declaring files+root")
	}
}

func TestExecutePassesFilesAndRootWhenSupported(t *testing.T) {
	caller := &fakeCaller{res: mcp.CallResult{Content: []mcp.ContentBlock{{Type: "text", Text: "summary"}}}}
	lt := New(caller, Config{Server: "local-helper", DirectTool: "local_direct", SupportsFiles: true})

	res, err := lt.Execute(tool.WithCwd(context.Background(), "/repo"), json.RawMessage(`{
		"task": "summarize",
		"prompt": "summarize these files",
		"files": ["build.log"]
	}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned tool error: %+v", res)
	}
	files, _ := caller.input["files"].([]any)
	if len(files) != 1 || files[0] != "build.log" {
		t.Fatalf("caller.input[files] = %#v, want [build.log]", caller.input["files"])
	}
	if caller.input["root"] != "/repo" {
		t.Fatalf("caller.input[root] = %#v, want /repo", caller.input["root"])
	}
}

func TestDynamicConfigResolverIsUsedForDescriptionAndExecute(t *testing.T) {
	caller := &fakeCaller{res: mcp.CallResult{Content: []mcp.ContentBlock{{Type: "text", Text: "summary"}}}}
	cfg := Config{Server: "first-helper", DirectTool: "local_direct", Model: "first-model"}
	lt := NewDynamic(caller, func() (Config, bool) {
		return cfg, true
	})

	if desc := lt.Description(); !strings.Contains(desc, "first-model on first-helper") {
		t.Fatalf("description = %q, want first target", desc)
	}
	cfg = Config{Server: "second-helper", DirectTool: "local_direct", Model: "second-model"}
	res, err := lt.Execute(context.Background(), json.RawMessage(`{"task":"summarize","prompt":"x"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned tool error: %+v", res)
	}
	if caller.name != "mcp__second_helper__local_direct" {
		t.Fatalf("called %q, want updated helper target", caller.name)
	}
}
