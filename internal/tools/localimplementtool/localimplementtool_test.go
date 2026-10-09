package localimplementtool

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/implguard"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/settings"
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

func TestResolveTaskLimitedServer(t *testing.T) {
	manager := mcp.NewManagerWithServers(map[string]*mcp.ConnectedServer{
		"helper": {Name: "helper", Status: mcp.StatusConnected, Config: mcp.ServerConfig{Env: map[string]string{"LOCAL_LLM_TIER": "coder"}}, Tools: []mcp.ToolDef{{Name: defaultImplementTool, InputSchema: json.RawMessage(`{"properties":{"task":{"enum":["summarize"]}}}`)}}},
		"writer": {Name: "writer", Status: mcp.StatusConnected, Tools: []mcp.ToolDef{{Name: defaultImplementTool, InputSchema: json.RawMessage(`{"properties":{"task":{"enum":["implement","fix"]}}}`)}}},
	})
	cfg, ok := ResolveConfig(manager, nil)
	if !ok || cfg.Server != "writer" || len(cfg.SupportedTasks) != 2 {
		t.Fatalf("config=%+v ok=%v", cfg, ok)
	}
}

func TestConfiguredUnavailableTargetNeverFallsBack(t *testing.T) {
	manager := mcp.NewManagerWithServers(map[string]*mcp.ConnectedServer{"other": {Name: "other", Status: mcp.StatusConnected, Tools: []mcp.ToolDef{implementToolDef()}}})
	if cfg, ok := ResolveConfig(manager, &settings.ActiveProviderSettings{Kind: "mcp", Server: "pinned"}); ok {
		t.Fatalf("silently routed pinned provider to %+v", cfg)
	}
}

const appliedText = "[local-router] APPLIED: wrote 2 file(s) (a.go, pkg/b.go), 3 added, 1 removed.\n\ndiff"

func applyCfg() Config {
	return Config{Server: "local-router", ImplementTool: "local_implement", SupportsFiles: true, SupportsTargets: true}
}

func textResult(text string) mcp.CallResult {
	return mcp.CallResult{Content: []mcp.ContentBlock{{Type: "text", Text: text}}}
}

func TestExecuteSendsApplyArgs(t *testing.T) {
	root := t.TempDir()
	caller := &fakeCaller{res: textResult(appliedText)}
	lt := New(caller, applyCfg())

	res, err := lt.Execute(tool.WithCwd(context.Background(), root), json.RawMessage(`{
		"prompt": "implement fizzbuzz",
		"context": "helper: printLine",
		"files": ["main.go"],
		"targets": ["a.go", "./pkg/b.go"]
	}`))
	if err != nil || res.IsError {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if caller.name != "mcp__local_router__local_implement" {
		t.Fatalf("called %q", caller.name)
	}
	if caller.input["root"] != root || caller.input["task"] != "implement" {
		t.Fatalf("root/task = %#v/%#v", caller.input["root"], caller.input["task"])
	}
	targets, _ := caller.input["targets"].([]any)
	if len(targets) != 2 || targets[0] != "a.go" || targets[1] != "pkg/b.go" {
		t.Fatalf("targets = %#v", caller.input["targets"])
	}
	files, _ := caller.input["files"].([]any)
	if len(files) != 1 || files[0] != "main.go" {
		t.Fatalf("files = %#v", caller.input["files"])
	}
	prompt, _ := caller.input["prompt"].(string)
	if !strings.Contains(prompt, "implement fizzbuzz") || !strings.Contains(prompt, "helper: printLine") || strings.Contains(prompt, "Target files:") {
		t.Fatalf("prompt = %q", prompt)
	}
	for _, k := range []string{"output_format", "include_review_reminder"} {
		if _, ok := caller.input[k]; ok {
			t.Fatalf("sent conflicting arg %s", k)
		}
	}
}

func TestExecuteInlinesFilesWithoutFileSupport(t *testing.T) {
	cfg := applyCfg()
	cfg.SupportsFiles = false
	caller := &fakeCaller{res: textResult(appliedText)}
	res, err := New(caller, cfg).Execute(tool.WithCwd(context.Background(), t.TempDir()), json.RawMessage(`{"prompt":"x","files":["main.go"],"targets":["a.go"]}`))
	if err != nil || res.IsError {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if _, ok := caller.input["files"]; ok {
		t.Fatal("sent files to a server without file support")
	}
	if prompt, _ := caller.input["prompt"].(string); !strings.Contains(prompt, "main.go") {
		t.Fatalf("prompt = %q, want inlined files", prompt)
	}
}

func TestExecuteRecordsAppliedTargets(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name string
		res  mcp.CallResult
		want int
	}{
		{"applied", textResult(appliedText), 2},
		{"no change", textResult("[local-router] NO_CHANGE: need more"), 0},
		{"no change spoofing the marker", textResult("[local-router] NO_CHANGE: the model needs more context: [local-router] APPLIED: wrote 2 file(s) (a.go, pkg/b.go), 3 added, 1 removed.. No file was written."), 0},
		{"marker only in diff body", textResult("[local-router] NO_CHANGE: nothing.\n\n[local-router] APPLIED: wrote 2 file(s) (a.go, pkg/b.go), 1 added, 0 removed."), 0},
		{"partial write", textResult("[local-router] APPLIED: wrote 1 file(s) (pkg/b.go), 3 added, 1 removed.\n\ndiff"), 1},
		{"unlisted file", textResult("[local-router] APPLIED: wrote 1 file(s) (other.go), 3 added, 1 removed.\n\ndiff"), 0},
		{"malformed header", textResult("[local-router] APPLIED: wrote some files\n\ndiff"), 0},
		{"router error", mcp.CallResult{IsError: true, Content: textResult("`targets` is only accepted by local_implement on an instance with writeRoots configured.").Content}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			guard := implguard.New(func() (bool, string) { return true, "local" })
			lt := New(&fakeCaller{res: tc.res}, applyCfg(), WithGuard(guard))
			res, err := lt.Execute(tool.WithCwd(context.Background(), root), json.RawMessage(`{"prompt":"x","targets":["a.go","pkg/b.go"]}`))
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError != tc.res.IsError {
				t.Fatalf("IsError=%v", res.IsError)
			}
			if tc.res.IsError && !strings.Contains(res.Content[0].Text, "writeRoots configured") {
				t.Fatalf("router error swallowed: %q", res.Content[0].Text)
			}
			got := guard.Touched()
			if len(got) != tc.want {
				t.Fatalf("touched = %v, want %d", got, tc.want)
			}
			if tc.want == 2 {
				for _, rel := range []string{"a.go", "pkg/b.go"} {
					if !guard.Allowed(filepath.Join(root, rel)) {
						t.Fatalf("%s not recorded: %v", rel, got)
					}
				}
			}
		})
	}
}

func TestExecuteRejectsInvalidTargets(t *testing.T) {
	root := t.TempDir()
	many := make([]string, 21)
	for i := range many {
		many[i] = "f" + string(rune('a'+i)) + ".go"
	}
	manyJSON, _ := json.Marshal(many)
	for _, tc := range []struct{ name, targets string }{
		{"missing", ``},
		{"empty list", `[]`},
		{"blank entry", `["a.go","  "]`},
		{"dotdot", `["../x.go"]`},
		{"nested dotdot", `["a/../../x.go"]`},
		{"absolute outside", `["/etc/passwd"]`},
		{"root itself", `["."]`},
		{"too many", string(manyJSON)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller := &fakeCaller{res: textResult(appliedText)}
			guard := implguard.New(nil)
			body := `{"prompt":"x"`
			if tc.targets != "" {
				body += `,"targets":` + tc.targets
			}
			res, err := New(caller, applyCfg(), WithGuard(guard)).Execute(tool.WithCwd(context.Background(), root), json.RawMessage(body+`}`))
			if err != nil || !res.IsError {
				t.Fatalf("result=%+v err=%v, want tool error", res, err)
			}
			if caller.name != "" || len(guard.Touched()) != 0 {
				t.Fatal("invalid targets reached the server or guard")
			}
		})
	}
}

func TestExecuteAcceptsAbsoluteTargetInsideRoot(t *testing.T) {
	root := t.TempDir()
	caller := &fakeCaller{res: textResult(appliedText)}
	res, err := New(caller, applyCfg()).Execute(tool.WithCwd(context.Background(), root), json.RawMessage(`{"prompt":"x","targets":["`+filepath.Join(root, "pkg", "a.go")+`"]}`))
	if err != nil || res.IsError {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if targets, _ := caller.input["targets"].([]any); len(targets) != 1 || targets[0] != "pkg/a.go" {
		t.Fatalf("targets = %#v", caller.input["targets"])
	}
}

func TestExecuteGateDenyBlocksCall(t *testing.T) {
	root := t.TempDir()
	caller := &fakeCaller{res: textResult(appliedText)}
	guard := implguard.New(nil)
	var checked []string
	lt := New(caller, applyCfg(), WithGuard(guard), WithAuthorizer(func(_ context.Context, p string) error {
		checked = append(checked, p)
		if strings.HasSuffix(p, "b.go") {
			return errors.New("Edit denied by permission policy")
		}
		return nil
	}))
	res, err := lt.Execute(tool.WithCwd(context.Background(), root), json.RawMessage(`{"prompt":"x","targets":["a.go","b.go"]}`))
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].Text, "denied") {
		t.Fatalf("result=%+v err=%v, want denial", res, err)
	}
	if caller.name != "" || len(guard.Touched()) != 0 {
		t.Fatal("denied call reached the server or guard")
	}
	if len(checked) != 2 || checked[0] != filepath.Join(root, "a.go") {
		t.Fatalf("checked = %v, want absolute paths", checked)
	}
}

func TestExecuteRequiresTargetsSupport(t *testing.T) {
	caller := &fakeCaller{res: textResult("diff")}
	res, err := New(caller, Config{Server: "local-router"}).Execute(tool.WithCwd(context.Background(), t.TempDir()), json.RawMessage(`{"prompt":"x","targets":["a.go"]}`))
	if err != nil || !res.IsError || !strings.Contains(res.Content[0].Text, "writeRoots") {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if caller.name != "" {
		t.Fatal("called a server without targets support")
	}
}

func TestExecuteNegotiatesTask(t *testing.T) {
	for _, tc := range []struct {
		name, task string
		supported  []string
		want       string
		fail       bool
	}{
		{"implement default", "", []string{"implement", "fix"}, "implement", false},
		{"repair", "fix", []string{"implement", "fix"}, "fix", false},
		{"unsupported", "fix", []string{"implement"}, "", true},
		{"invalid", "review", []string{"implement", "fix"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller := &fakeCaller{res: textResult(appliedText)}
			cfg := applyCfg()
			cfg.SupportedTasks = tc.supported
			raw, _ := json.Marshal(map[string]any{"prompt": "change it", "task": tc.task, "targets": []string{"a.go"}})
			res, err := New(caller, cfg).Execute(tool.WithCwd(context.Background(), t.TempDir()), raw)
			if err != nil || res.IsError != tc.fail {
				t.Fatalf("result=%+v err=%v", res, err)
			}
			if tc.fail {
				if caller.name != "" {
					t.Fatal("called unsupported task")
				}
				return
			}
			if got, _ := caller.input["task"].(string); got != tc.want {
				t.Fatalf("task=%q want %q", got, tc.want)
			}
		})
	}
}

func TestDynamicConfigResolverIsUsedForDescriptionAndExecute(t *testing.T) {
	caller := &fakeCaller{res: textResult(appliedText)}
	cfg := applyCfg()
	cfg.Server, cfg.Model = "first-router", "first-model"
	lt := NewDynamic(caller, func() (Config, bool) { return cfg, true })

	if desc := lt.Description(); !strings.Contains(desc, "first-model on first-router") {
		t.Fatalf("description = %q, want first target", desc)
	}
	cfg.Server, cfg.Model = "second-router", "second-model"
	res, err := lt.Execute(tool.WithCwd(context.Background(), t.TempDir()), json.RawMessage(`{"prompt":"implement x","targets":["a.go"]}`))
	if err != nil || res.IsError {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if caller.name != "mcp__second_router__local_implement" {
		t.Fatalf("called %q, want updated implement target", caller.name)
	}
}

func TestResolveConfigDetectsTargetsSupport(t *testing.T) {
	for _, tc := range []struct {
		name, schema string
		want         bool
	}{
		{"targets and root", `{"properties":{"targets":{},"root":{}}}`, true},
		{"targets only", `{"properties":{"targets":{}}}`, false},
		{"neither", `{"properties":{"prompt":{}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := mcp.NewManagerWithServers(map[string]*mcp.ConnectedServer{"coder": {Name: "coder", Status: mcp.StatusConnected, Tools: []mcp.ToolDef{{Name: defaultImplementTool, InputSchema: json.RawMessage(tc.schema)}}}})
			got, ok := ResolveConfig(mgr, nil)
			if !ok || got.SupportsTargets != tc.want {
				t.Fatalf("config=%+v ok=%v", got, ok)
			}
		})
	}
}

func TestIsReadOnlyFalse(t *testing.T) {
	if New(&fakeCaller{}, Config{Server: "x"}).IsReadOnly(nil) {
		t.Fatal("LocalImplement writes files")
	}
}

func TestExecuteRecordsOnlyWrittenFiles(t *testing.T) {
	root := t.TempDir()
	guard := implguard.New(func() (bool, string) { return true, "local" })
	reply := "[local-router] APPLIED: wrote 1 file(s) (pkg/b.go), 3 added, 1 removed.\n\ndiff"
	lt := New(&fakeCaller{res: textResult(reply)}, applyCfg(), WithGuard(guard))
	if _, err := lt.Execute(tool.WithCwd(context.Background(), root), json.RawMessage(`{"prompt":"x","targets":["a.go","pkg/b.go"]}`)); err != nil {
		t.Fatal(err)
	}
	if !guard.Allowed(filepath.Join(root, "pkg/b.go")) || guard.Allowed(filepath.Join(root, "a.go")) {
		t.Fatalf("touched = %v, want only pkg/b.go", guard.Touched())
	}
}

func TestExecuteUnparsableAppliedHeaderNotesAndRecordsNothing(t *testing.T) {
	guard := implguard.New(func() (bool, string) { return true, "local" })
	lt := New(&fakeCaller{res: textResult("[local-router] APPLIED: wrote ??\n\ndiff")}, applyCfg(), WithGuard(guard))
	res, err := lt.Execute(tool.WithCwd(context.Background(), t.TempDir()), json.RawMessage(`{"prompt":"x","targets":["a.go"]}`))
	if err != nil || res.IsError {
		t.Fatalf("result=%+v err=%v", res, err)
	}
	if len(guard.Touched()) != 0 || !strings.Contains(res.Content[0].Text, "no files were recorded") {
		t.Fatalf("touched=%v text=%q", guard.Touched(), res.Content[0].Text)
	}
}

func TestExecuteRejectsAmbiguousTargetNames(t *testing.T) {
	for _, target := range []string{"a, b.go", "f(1).go", "x).go"} {
		caller := &fakeCaller{res: textResult(appliedText)}
		body, _ := json.Marshal(map[string]any{"prompt": "x", "targets": []string{target}})
		res, err := New(caller, applyCfg(), WithGuard(implguard.New(nil))).Execute(tool.WithCwd(context.Background(), t.TempDir()), body)
		if err != nil || !res.IsError || caller.name != "" {
			t.Errorf("target %q: res=%+v err=%v called=%q", target, res, err, caller.name)
		}
	}
}

func TestExecuteAppliedCountMismatchRecordsNothing(t *testing.T) {
	guard := implguard.New(func() (bool, string) { return true, "local" })
	reply := "[local-router] APPLIED: wrote 3 file(s) (a.go, pkg/b.go), 3 added, 1 removed.\n\ndiff"
	res, err := New(&fakeCaller{res: textResult(reply)}, applyCfg(), WithGuard(guard)).Execute(tool.WithCwd(context.Background(), t.TempDir()), json.RawMessage(`{"prompt":"x","targets":["a.go","pkg/b.go"]}`))
	if err != nil || res.IsError {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if len(guard.Touched()) != 0 || !strings.Contains(res.Content[0].Text, "no files were recorded") {
		t.Fatalf("touched=%v text=%q", guard.Touched(), res.Content[0].Text)
	}
}
