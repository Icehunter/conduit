package localimplementtool

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

func implementToolDef() mcp.ToolDef {
	return mcp.ToolDef{Name: defaultImplementTool}
}

func fileAwareImplementToolDef() mcp.ToolDef {
	return mcp.ToolDef{
		Name:        defaultImplementTool,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"prompt":{},"files":{},"root":{}}}`),
	}
}

func TestResolveConfigPrefersCoderTierDeterministically(t *testing.T) {
	// Regression test: manager.Servers() iterates a Go map, so without
	// tier-aware, sorted selection this pick is effectively random across
	// restarts — including landing on the CPU helper for code-writing work.
	for i := 0; i < 20; i++ {
		manager := mcp.NewManagerWithServers(map[string]*mcp.ConnectedServer{
			"local-helper": {
				Name:   "local-helper",
				Status: mcp.StatusConnected,
				Config: mcp.ServerConfig{Env: map[string]string{"LOCAL_LLM_TIER": "helper", "LOCAL_LLM_MODEL": "qwen-cpu-helper"}},
				Tools:  []mcp.ToolDef{implementToolDef()},
			},
			"local-coder": {
				Name:   "local-coder",
				Status: mcp.StatusConnected,
				Config: mcp.ServerConfig{Env: map[string]string{"LOCAL_LLM_TIER": "coder", "LOCAL_LLM_MODEL": "qwen3.8-27b"}},
				Tools:  []mcp.ToolDef{implementToolDef()},
			},
		})
		cfg, ok := ResolveConfig(manager, nil)
		if !ok {
			t.Fatalf("ResolveConfig() ok = false, want true")
		}
		if cfg.Server != "local-coder" {
			t.Fatalf("ResolveConfig().Server = %q, want local-coder (iteration %d)", cfg.Server, i)
		}
		if cfg.Model != "qwen3.8-27b" {
			t.Fatalf("ResolveConfig().Model = %q, want qwen3.8-27b", cfg.Model)
		}
	}
}

func TestResolveConfigFallsBackToDefaultServerName(t *testing.T) {
	manager := mcp.NewManagerWithServers(map[string]*mcp.ConnectedServer{
		"zzz-other": {
			Name:   "zzz-other",
			Status: mcp.StatusConnected,
			Tools:  []mcp.ToolDef{implementToolDef()},
		},
		"local-router": {
			Name:   "local-router",
			Status: mcp.StatusConnected,
			Tools:  []mcp.ToolDef{implementToolDef()},
		},
	})
	cfg, ok := ResolveConfig(manager, nil)
	if !ok || cfg.Server != "local-router" {
		t.Fatalf("ResolveConfig() = %+v, ok=%v, want local-router", cfg, ok)
	}
}

func TestResolveConfigDetectsFileSupport(t *testing.T) {
	manager := mcp.NewManagerWithServers(map[string]*mcp.ConnectedServer{
		"local-coder": {
			Name:   "local-coder",
			Status: mcp.StatusConnected,
			Config: mcp.ServerConfig{Env: map[string]string{"LOCAL_LLM_TIER": "coder"}},
			Tools:  []mcp.ToolDef{fileAwareImplementToolDef()},
		},
	})
	cfg, ok := ResolveConfig(manager, nil)
	if !ok {
		t.Fatalf("ResolveConfig() ok = false, want true")
	}
	if !cfg.SupportsFiles {
		t.Fatalf("cfg.SupportsFiles = false, want true for a tool schema declaring files+root")
	}
}

func TestExecutePassesFilesAndRootWhenSupported(t *testing.T) {
	caller := &fakeCaller{res: mcp.CallResult{Content: []mcp.ContentBlock{{Type: "text", Text: "diff"}}}}
	lt := New(caller, Config{Server: "local-coder", ImplementTool: "local_implement", SupportsFiles: true})

	res, err := lt.Execute(tool.WithCwd(context.Background(), "/repo"), json.RawMessage(`{
		"prompt": "implement fizzbuzz",
		"files": ["main.go"]
	}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned tool error: %+v", res)
	}
	files, _ := caller.input["files"].([]any)
	if len(files) != 1 || files[0] != "main.go" {
		t.Fatalf("caller.input[files] = %#v, want [main.go]", caller.input["files"])
	}
	if caller.input["root"] != "/repo" {
		t.Fatalf("caller.input[root] = %#v, want /repo", caller.input["root"])
	}
	prompt, _ := caller.input["prompt"].(string)
	if strings.Contains(prompt, "Target files:") {
		t.Fatalf("prompt = %q, files should not be inlined when SupportsFiles is true", prompt)
	}
}

func TestExecuteFallsBackToInlineFilesWhenUnsupported(t *testing.T) {
	caller := &fakeCaller{res: mcp.CallResult{Content: []mcp.ContentBlock{{Type: "text", Text: "diff"}}}}
	lt := New(caller, Config{Server: "local-coder", ImplementTool: "local_implement"})

	_, err := lt.Execute(context.Background(), json.RawMessage(`{
		"prompt": "implement fizzbuzz",
		"files": ["main.go"]
	}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if _, ok := caller.input["files"]; ok {
		t.Fatalf("caller.input[files] set = %#v, want no files arg when SupportsFiles is false", caller.input["files"])
	}
	prompt, _ := caller.input["prompt"].(string)
	if !strings.Contains(prompt, "Target files:") || !strings.Contains(prompt, "main.go") {
		t.Fatalf("prompt = %q, want inlined files list", prompt)
	}
}

func TestDynamicConfigResolverIsUsedForDescriptionAndExecute(t *testing.T) {
	caller := &fakeCaller{res: mcp.CallResult{Content: []mcp.ContentBlock{{Type: "text", Text: "diff"}}}}
	cfg := Config{Server: "first-router", ImplementTool: "local_implement", Model: "first-model"}
	lt := NewDynamic(caller, func() (Config, bool) {
		return cfg, true
	})

	if desc := lt.Description(); !strings.Contains(desc, "first-model on first-router") {
		t.Fatalf("description = %q, want first target", desc)
	}
	cfg = Config{Server: "second-router", ImplementTool: "local_implement", Model: "second-model"}
	res, err := lt.Execute(context.Background(), json.RawMessage(`{"prompt":"implement x"}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute returned tool error: %+v", res)
	}
	if caller.name != "mcp__second_router__local_implement" {
		t.Fatalf("called %q, want updated implement target", caller.name)
	}
}
