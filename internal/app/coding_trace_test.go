package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/coding"
	"github.com/icehunter/conduit/internal/episodes"
	"github.com/icehunter/conduit/internal/mcp"
	"github.com/icehunter/conduit/internal/settings"
)

const traceCodeSuffix = "\n\n---\n\nReturn only code. No prose, no fences unless syntactically required by the language."

func codingTraceFixture(prompt, output string) map[string]any {
	return map[string]any{"local_router_trace": map[string]any{
		"version": 1, "request_id": "call-1", "endpoint_id": "sha256:" + strings.Repeat("a", 64), "complete": true,
		"request":  map[string]any{"model": "qwen", "messages": []any{map[string]any{"role": "system", "content": "router system"}, map[string]any{"role": "user", "content": prompt + traceCodeSuffix}}, "parameters": map[string]any{"max_tokens": 8192, "temperature": 0.2, "top_p": 0.8, "top_k": 20, "min_p": 0.05, "repeat_penalty": 1.1, "chat_template_kwargs": map[string]any{"enable_thinking": false}}},
		"response": map[string]any{"content": output, "finish_reason": "stop", "usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 5}},
	}}
}

func TestCodingTraceValidation(t *testing.T) {
	for _, tc := range []struct {
		name                string
		edit                func(map[string]any)
		invalid, incomplete bool
	}{
		{"complete", func(map[string]any) {}, false, false},
		{"explicitly incomplete", func(m map[string]any) { m["complete"] = false }, false, true},
		{"unsupported finish", func(m map[string]any) { m["response"].(map[string]any)["finish_reason"] = "tool_calls" }, false, true},
		{"absent finish", func(m map[string]any) { m["response"].(map[string]any)["finish_reason"] = "" }, false, true},
		{"null finish", func(m map[string]any) { m["response"].(map[string]any)["finish_reason"] = nil }, false, true},
		{"omitted finish", func(m map[string]any) { delete(m["response"].(map[string]any), "finish_reason") }, false, true},
		{"no usage", func(m map[string]any) { m["response"].(map[string]any)["usage"] = nil }, false, false},
		{"missing complete", func(m map[string]any) { delete(m, "complete") }, true, false},
		{"wrong version", func(m map[string]any) { m["version"] = 2 }, true, false},
		{"empty id", func(m map[string]any) { m["request_id"] = "" }, true, false},
		{"raw endpoint", func(m map[string]any) { m["endpoint_id"] = "http://host/private?key=secret" }, true, false},
		{"wrong model", func(m map[string]any) { m["request"].(map[string]any)["model"] = "other" }, true, false},
		{"wrong prompt", func(m map[string]any) {
			m["request"].(map[string]any)["messages"].([]any)[1].(map[string]any)["content"] = "unrelated" + traceCodeSuffix
		}, true, false},
		{"tool message", func(m map[string]any) {
			m["request"].(map[string]any)["messages"].([]any)[1].(map[string]any)["role"] = "tool"
		}, true, false},
		{"multimodal", func(m map[string]any) {
			m["request"].(map[string]any)["messages"].([]any)[1].(map[string]any)["content"] = []any{}
		}, true, false},
		{"missing parameters", func(m map[string]any) { delete(m["request"].(map[string]any), "parameters") }, true, false},
		{"parameter messages override", func(m map[string]any) {
			m["request"].(map[string]any)["parameters"].(map[string]any)["messages"] = []any{}
		}, true, false},
		{"parameter invalid tokens", func(m map[string]any) {
			m["request"].(map[string]any)["parameters"].(map[string]any)["max_tokens"] = "8192"
		}, true, false},
		{"unknown tool parameters", func(m map[string]any) {
			m["request"].(map[string]any)["parameters"].(map[string]any)["tools"] = []any{}
		}, true, false},
		{"unknown system parameter", func(m map[string]any) {
			m["request"].(map[string]any)["parameters"].(map[string]any)["system"] = "unrecorded"
		}, true, false},
		{"missing sampling parameter", func(m map[string]any) {
			delete(m["request"].(map[string]any)["parameters"].(map[string]any), "temperature")
		}, true, false},
		{"template missing thinking", func(m map[string]any) {
			m["request"].(map[string]any)["parameters"].(map[string]any)["chat_template_kwargs"] = map[string]any{}
		}, true, false},
		{"template unknown key", func(m map[string]any) {
			m["request"].(map[string]any)["parameters"].(map[string]any)["chat_template_kwargs"].(map[string]any)["extra"] = "private"
		}, true, false},
		{"template absent", func(m map[string]any) {
			delete(m["request"].(map[string]any)["parameters"].(map[string]any), "chat_template_kwargs")
		}, false, false},
		{"wrong response", func(m map[string]any) { m["response"].(map[string]any)["content"] = "different" }, true, false},
		{"private reasoning field", func(m map[string]any) { m["response"].(map[string]any)["reasoning_content"] = "private" }, true, false},
		{"usage text", func(m map[string]any) {
			m["response"].(map[string]any)["usage"] = map[string]any{"prompt_tokens": "secret"}
		}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := codingTraceFixture("task", "draft")
			tc.edit(fixture["local_router_trace"].(map[string]any))
			raw, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			got, err := validateCodingTrace(raw, "draft", "qwen", "task")
			if (err != nil) != tc.invalid {
				t.Fatalf("request=%+v err=%v", got, err)
			}
			if err == nil && (got.ContextComplete == tc.incomplete || len(got.Messages) != 2 || got.ModelID != "qwen" || got.Trace == nil) {
				t.Fatalf("request=%+v", got)
			}
		})
	}
	for _, raw := range []string{`{}`, `null`, `{"local_router_trace":null}`, `{} {}`, `{"local_router_trace":{"version":1,"version":2}}`} {
		if _, err := validateCodingTrace(json.RawMessage(raw), "draft", "qwen", "task"); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

// A synthetic MCP server exercises discovery, transport, actual request capture,
// patch application, the journal, and export without calling a model.
func codingTraceManager(t *testing.T, advertised bool, reply func(map[string]any) mcp.CallResult) *mcp.Manager {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
		case "tools/list":
			props := map[string]any{"task": map[string]any{"type": "string", "enum": []string{"implement", "fix"}}}
			if advertised {
				props["include_trace"] = map[string]any{"type": "boolean"}
			}
			result = map[string]any{"tools": []any{map[string]any{"name": "local_implement", "inputSchema": map[string]any{"type": "object", "properties": props}}}}
		case "tools/call":
			result = reply(req.Params.Arguments)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	config := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", config)
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	b, err := json.Marshal(mcp.McpJSON{McpServers: map[string]mcp.ServerConfig{"trace-fixture": {Type: "http", URL: server.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(config, "mcp.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	mgr := mcp.NewManager()
	t.Cleanup(mgr.Close)
	if err = mgr.Reconnect(context.Background(), "trace-fixture", t.TempDir(), false); err != nil {
		t.Fatal(err)
	}
	return mgr
}

func TestCodingMCPTraceJournalExportAndFailureBeforeEdits(t *testing.T) {
	for _, tc := range []struct {
		name                                                     string
		advertised, content, invalid, incomplete, secret, denied bool
	}{
		{name: "complete", advertised: true, content: true},
		{name: "legacy", content: true},
		{name: "private default", advertised: true},
		{name: "missing trace", advertised: true, content: true, invalid: true},
		{name: "mismatched trace", advertised: true, content: true, invalid: true},
		{name: "failed tool", advertised: true, content: true, invalid: true},
		{name: "incomplete", advertised: true, content: true, incomplete: true},
		{name: "secret input", advertised: true, content: true, secret: true},
		{name: "denied source", advertised: true, content: true, denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, _, root := codingFixture(t, false)
			if !rt.providers.CaptureMCPTrace {
				t.Fatal("runtime content opt-in did not enable trace")
			}
			if err := rt.recorder.Close(); err != nil {
				t.Fatal(err)
			}
			ep := episodes.Config{Enabled: true, IncludeContent: tc.content, RepositoryRoot: root, Dir: t.TempDir(), SessionID: "trace", AllowedPaths: []string{"*.txt"}}
			if tc.denied {
				ep.ExcludedPaths = []string{"answer.txt"}
			}
			var err error
			rt.recorder, err = episodes.New(ep)
			if err != nil {
				t.Fatal(err)
			}
			draft := `{"edits":[{"path":"answer.txt","old":"wrong","new":"correct"}]}`
			mgr := codingTraceManager(t, tc.advertised, func(args map[string]any) mcp.CallResult {
				_, requested := args["include_trace"]
				if requested != (tc.advertised && tc.content) || (requested && args["include_trace"] != true) {
					t.Errorf("trace opt-in=%+v", args)
				}
				for _, key := range []string{"files", "root", "targets"} {
					if _, ok := args[key]; ok {
						t.Errorf("non-inline key %s", key)
					}
				}
				res := mcp.CallResult{Content: []mcp.ContentBlock{{Type: "text", Text: "<local_output id=\"id\">\n" + draft + "\n</local_output id=\"id\">"}}}
				if tc.name == "failed tool" {
					return mcp.CallResult{IsError: true, Content: []mcp.ContentBlock{{Type: "text", Text: "upstream request failed"}}}
				}
				if requested && tc.name != "missing trace" {
					fixture := codingTraceFixture(args["prompt"].(string), draft)
					fixture["local_router_trace"].(map[string]any)["complete"] = !tc.incomplete
					if tc.name == "mismatched trace" {
						fixture["local_router_trace"].(map[string]any)["response"].(map[string]any)["content"] = "different draft"
					}
					res.StructuredContent, _ = json.Marshal(fixture)
				}
				return res
			})
			p := settings.ActiveProviderSettings{Kind: settings.ProviderKindMCP, Server: "trace-fixture", Model: "qwen"}
			rt.providers.Settings = &settings.Merged{Providers: map[string]settings.ActiveProviderSettings{"coder": p}, Roles: map[string]string{"implement": "coder"}}
			rt.providers.MCP = mgr
			rt.providers.CaptureMCPTrace = tc.content
			rt.policies.DefinitionOfDone.RequireReview = false
			prompt := "make answer correct"
			if tc.secret {
				prompt += " password=private-value"
			}
			out, runErr := rt.Run(context.Background(), coding.Task{Prompt: prompt, Files: []string{"answer.txt"}})
			b, err := os.ReadFile(filepath.Join(root, "answer.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.invalid {
				if runErr == nil || string(b) != "wrong" {
					t.Fatalf("malformed trace applied: out=%+v err=%v answer=%q", out, runErr, b)
				}
			} else if runErr != nil || out.Status != "verified" || string(b) != "correct" {
				t.Fatalf("out=%+v err=%v answer=%q", out, runErr, b)
			}
			events, err := episodes.ReadFile(rt.recorder.Path())
			if err != nil {
				t.Fatal(err)
			}
			requests := 0
			var candidate episodes.Candidate
			for _, e := range events {
				if e.Kind == episodes.KindRequest {
					requests++
					if requests != 1 {
						t.Fatal("duplicate model request")
					}
				}
				if e.Kind == episodes.KindCandidate {
					if requests != 1 {
						t.Fatal("candidate before request")
					}
					raw, _ := json.Marshal(e.Payload)
					_ = json.Unmarshal(raw, &candidate)
				}
			}
			if requests != 1 {
				t.Fatalf("requests=%d", requests)
			}
			if candidate.ID != "" {
				if err := rt.recorder.Record(episodes.Event{Kind: episodes.KindDecision, EpisodeID: events[0].EpisodeID, Payload: episodes.Decision{CandidateID: candidate.ID, Action: "accepted", Source: episodes.SourceExplicitHuman}}); err != nil {
					t.Fatal(err)
				}
			}
			dataset, err := episodes.ExportFile(rt.recorder.Path(), episodes.ExportOptions{})
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.advertised && tc.content && !tc.invalid && !tc.incomplete && !tc.secret && !tc.denied {
				want = 1
			}
			if len(dataset.SFT) != want {
				t.Fatalf("sft=%d manifest=%+v", len(dataset.SFT), dataset.Manifest)
			}
			ledger, err := os.ReadFile(rt.recorder.Path())
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(ledger), "private-value") {
				t.Fatal("raw secret persisted")
			}
		})
	}
}

func TestCodingRuntimeTraceRequiresContentCollection(t *testing.T) {
	base, _, _ := codingFixture(t, false)
	for _, tc := range []struct {
		name       string
		config     *settings.EpisodeSettings
		evaluation bool
		want       bool
	}{
		{"absent", nil, false, false},
		{"disabled", &settings.EpisodeSettings{IncludeContent: true}, false, false},
		{"metadata only", &settings.EpisodeSettings{Enabled: true}, false, false},
		{"content opt-in", &settings.EpisodeSettings{Enabled: true, IncludeContent: true}, false, true},
		{"evaluation", &settings.EpisodeSettings{Enabled: true, IncludeContent: true}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policies := base.policies
			policies.Episodes = tc.config
			opts := base.opts
			opts.Policies = &policies
			opts.StateDir = t.TempDir()
			if tc.evaluation {
				t.Setenv("CONDUIT_EVAL_ORIGIN", episodes.OriginEvaluation)
			}
			rt, err := NewCodingRuntime(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer rt.Close()
			if rt.providers.CaptureMCPTrace != tc.want {
				t.Fatalf("capture=%v want=%v", rt.providers.CaptureMCPTrace, tc.want)
			}
		})
	}
}
