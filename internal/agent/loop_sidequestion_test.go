package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
)

func asstText(s string) api.Message {
	return api.Message{Role: "assistant", Content: []api.ContentBlock{{Type: "text", Text: s}}}
}

func asstToolUse(id string) api.Message {
	return api.Message{Role: "assistant", Content: []api.ContentBlock{{Type: "tool_use", ID: id, Name: "Rec", Input: map[string]any{}}}}
}

func toolResultMsg(id string) api.Message {
	return api.Message{Role: "user", Content: []api.ContentBlock{{Type: "tool_result", ToolUseID: id, ResultContent: "r"}}}
}

func TestTrimToCompletedAssistant(t *testing.T) {
	tests := []struct {
		name string
		in   []api.Message
		want []api.Message
	}{
		{"nil", nil, nil},
		{"only a user prompt", []api.Message{user("q")}, nil},
		{"completed turn kept", []api.Message{user("q"), asstText("a")}, []api.Message{user("q"), asstText("a")}},
		{"in-flight prompt dropped", []api.Message{user("q"), asstText("a"), user("next")}, []api.Message{user("q"), asstText("a")}},
		{"unanswered tool_use dropped", []api.Message{user("q"), asstText("a"), user("n"), asstToolUse("t1")}, []api.Message{user("q"), asstText("a")}},
		{"tool_result tail dropped", []api.Message{user("q"), asstText("a"), user("n"), asstToolUse("t1"), toolResultMsg("t1")}, []api.Message{user("q"), asstText("a")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trimToCompletedAssistant(tt.in)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// captureServer answers every request with text and records the bodies.
func captureServer(t *testing.T, answer string) (*api.Client, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		mu.Lock()
		bodies = append(bodies, m)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(textOnlySSE(answer)))
	}))
	t.Cleanup(srv.Close)
	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "test"}, srv.Client())
	return c, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([]map[string]any(nil), bodies...)
	}
}

func TestSideQuestion(t *testing.T) {
	c, bodies := captureServer(t, "It is blue.")
	reg := tool.NewRegistry()
	reg.Register(&recTool{name: "Rec", result: "ok", readOnly: true})
	lp := NewLoop(c, reg, LoopConfig{Model: "main-model", MaxTokens: 8000, ThinkingBudget: 4000,
		System: []api.SystemBlock{{Type: "text", Text: "sys"}}})

	// A completed tool exchange, then a mid-turn tail.
	hist := []api.Message{user("paint it"), asstToolUse("t0"), toolResultMsg("t0"), asstText("painted"), user("go on"), asstToolUse("t1")}
	before := append([]api.Message(nil), hist...)

	ans, err := lp.SideQuestion(context.Background(), hist, "what colour?")
	if err != nil {
		t.Fatalf("SideQuestion: %v", err)
	}
	if ans != "It is blue." {
		t.Errorf("answer = %q", ans)
	}
	if !reflect.DeepEqual(hist, before) {
		t.Error("caller's history was modified")
	}

	got := bodies()
	if len(got) != 1 {
		t.Fatalf("requests = %d, want 1", len(got))
	}
	body := got[0]
	// The history holds a tool_use, so the parent's tool definitions must
	// ride along (Anthropic 400s otherwise) and keep the cached prefix.
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("request tools = %v, want the parent's one tool", body["tools"])
	}
	if name, _ := tools[0].(map[string]any)["name"].(string); name != "Rec" {
		t.Errorf("tool = %v", tools[0])
	}
	if mt, _ := body["max_tokens"].(float64); mt != sideQuestionMaxTokens {
		t.Errorf("max_tokens = %v, want %d", body["max_tokens"], sideQuestionMaxTokens)
	}
	if _, ok := body["thinking"]; ok {
		t.Errorf("side question requested thinking: %v", body["thinking"])
	}
	if body["model"] != "main-model" {
		t.Errorf("model = %v, want the main model", body["model"])
	}
	raw, _ := json.Marshal(body)
	s := string(raw)
	for _, want := range []string{"painted", "Side question: what colour?", "SIDE question"} {
		if !strings.Contains(s, want) {
			t.Errorf("request lacks %q", want)
		}
	}
	if !strings.Contains(s, `"id":"t0"`) {
		t.Error("completed tool exchange was dropped")
	}
	// The mid-turn tail was trimmed away.
	if strings.Contains(s, `"id":"t1"`) {
		t.Error("unanswered tool_use was sent")
	}
}

func TestSideQuestion_Errors(t *testing.T) {
	c, _ := captureServer(t, "x")
	lp := NewLoop(c, tool.NewRegistry(), LoopConfig{Model: "m", MaxTokens: 100, System: []api.SystemBlock{{Type: "text", Text: "s"}}})
	if _, err := lp.SideQuestion(context.Background(), nil, "   "); err == nil {
		t.Error("empty question accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := lp.SideQuestion(ctx, nil, "q"); err == nil {
		t.Error("cancelled context returned no error")
	}
}

// A tool call in the answer cannot run: the child's registry is empty, and no
// hook of the parent fires for the side question.
func TestSideQuestion_NeverRunsTools(t *testing.T) {
	rt := &recTool{name: "Rec", result: "ran", readOnly: true}
	reg := tool.NewRegistry()
	reg.Register(rt)
	stop := filepath.Join(t.TempDir(), "stop-ran")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(singleToolUseSSEWithInput("Rec", "x1", `{}`)))
	}))
	defer srv.Close()
	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "test"}, srv.Client())
	lp := NewLoop(c, reg, LoopConfig{Model: "m", MaxTokens: 100, System: []api.SystemBlock{{Type: "text", Text: "s"}},
		Hooks: &settings.HooksSettings{Stop: []settings.HookMatcher{{Hooks: []settings.Hook{{Type: "command", Command: "touch " + stop}}}}}})
	if _, err := lp.SideQuestion(context.Background(), []api.Message{user("q"), asstText("a")}, "do it"); err == nil {
		t.Error("expected an error: the model only called a tool")
	}
	if rt.calls() != 0 {
		t.Errorf("tool ran %d times in a side question", rt.calls())
	}
	if _, err := os.Stat(stop); err == nil {
		t.Error("Stop hook fired for the side question")
	}
}

// The side question reads as a user-typed main request on the wire.
func TestSideQuestion_SendsMainClassUserTurn(t *testing.T) {
	c, _ := captureServer(t, "ok")
	lp := NewLoop(c, tool.NewRegistry(), LoopConfig{Model: "m", MaxTokens: 100, System: []api.SystemBlock{{Type: "text", Text: "s"}}})
	child, _ := lp.sideQuestionChild()
	h := child.wireHints("", "", "", true)
	if h.RequestClass != requestClassMain || !h.UserTurn {
		t.Errorf("hints = %+v, want class main with UserTurn", h)
	}
}

// The side question's tool list is built for its model, so a weak-edit model
// sees the same tools (no HashEdit) as the parent's requests.
func TestSideQuestion_ToolDefsFollowModel(t *testing.T) {
	c, bodies := captureServer(t, "ok")
	reg := tool.NewRegistry()
	reg.Register(&recTool{name: "Rec", result: "ok", readOnly: true})
	reg.Register(&recTool{name: "HashEdit", result: "ok"})
	lp := NewLoop(c, reg, LoopConfig{Model: "kimi-k2", MaxTokens: 100, System: []api.SystemBlock{{Type: "text", Text: "s"}}})
	if _, err := lp.SideQuestion(context.Background(), []api.Message{user("q"), asstText("a")}, "why?"); err != nil {
		t.Fatal(err)
	}
	got := bodies()
	if len(got) != 1 {
		t.Fatalf("requests = %d", len(got))
	}
	tools, _ := got[0]["tools"].([]any)
	for _, tl := range tools {
		if name, _ := tl.(map[string]any)["name"].(string); name == "HashEdit" {
			t.Error("weak-edit model was shown HashEdit; tools array differs from the parent's")
		}
	}
	if len(tools) != 1 {
		t.Errorf("tools = %v, want just Rec", tools)
	}
}
