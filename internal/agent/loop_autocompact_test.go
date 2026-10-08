package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/compact"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
)

const autoCompactWindow = 50_000 // threshold: 50000 - 20000 - 13000 = 17000

// autoReq is one request an autoServer saw.
type autoReq struct {
	model      string
	compaction bool
}

// autoServer answers main requests with a text reply reporting mainTokens of
// prompt, and compaction requests from compactions in order (the last repeats;
// nil means a summary). It speaks the Anthropic or the OpenAI chat dialect.
type autoServer struct {
	openAI     bool
	mainTokens int // guarded by mu after construction; see setTokens
	// compactions holds the compaction statuses in order; 0 means success.
	compactions []int

	mu   sync.Mutex
	reqs []autoReq
	srv  *httptest.Server
}

func newAutoServer(t *testing.T, openAI bool, mainTokens int, compactions ...int) *autoServer {
	t.Helper()
	a := &autoServer{openAI: openAI, mainTokens: mainTokens, compactions: compactions}
	a.srv = httptest.NewServer(a)
	t.Cleanup(a.srv.Close)
	return a
}

func (a *autoServer) client() *api.Client {
	cfg := api.Config{BaseURL: a.srv.URL, AuthToken: "tok"}
	if a.openAI {
		cfg = api.Config{BaseURL: a.srv.URL, APIKey: "k", ProviderKind: api.ProviderKindOpenAICompatible}
	}
	return api.NewClient(cfg, a.srv.Client())
}

func (a *autoServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(raw, &body)
	compaction := strings.Contains(string(raw), "summarizing a coding conversation")

	a.mu.Lock()
	a.reqs = append(a.reqs, autoReq{model: body.Model, compaction: compaction})
	nth := 0
	for _, q := range a.reqs {
		if q.compaction {
			nth++
		}
	}
	a.mu.Unlock()

	if compaction && len(a.compactions) > 0 {
		if status := a.compactions[min(nth, len(a.compactions))-1]; status != 0 {
			w.Header().Set("Content-Type", "application/json")
			if status == 503 {
				w.Header().Set("x-should-retry", "false")
			}
			w.WriteHeader(status)
			_, _ = io.WriteString(w, apiErrBody("api_error", "compaction failed"))
			return
		}
	}
	a.mu.Lock()
	text, tokens := "done", a.mainTokens
	a.mu.Unlock()
	if compaction {
		text, tokens = "<summary>S</summary>", 10
	}
	w.Header().Set("Content-Type", "text/event-stream")
	if a.openAI {
		_, _ = fmt.Fprintf(w, `data: {"choices":[{"delta":{"content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":%d,"completion_tokens":1}}`+"\n\ndata: [DONE]\n\n", jsonStr(text), tokens)
		return
	}
	_, _ = io.WriteString(w, strings.Replace(sseMessage("end_turn", sseTextBlock(text)), `"input_tokens":10`, fmt.Sprintf(`"input_tokens":%d`, tokens), 1))
}

func (a *autoServer) requests() []autoReq {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]autoReq(nil), a.reqs...)
}

func (a *autoServer) compactionModels() []string {
	var models []string
	for _, q := range a.requests() {
		if q.compaction {
			models = append(models, q.model)
		}
	}
	return models
}

func autoLoop(c *api.Client, model string, mut func(*LoopConfig)) *Loop {
	cfg := LoopConfig{
		Model:       model,
		MaxTokens:   1024,
		MaxTurns:    10,
		AutoCompact: true,
		Sleep:       func(context.Context, time.Duration) error { return nil },
	}
	if mut != nil {
		mut(&cfg)
	}
	return NewLoop(c, tool.NewRegistry(), cfg)
}

func TestLoop_AutoCompact(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", fmt.Sprint(autoCompactWindow))
	over := internalmodel.AutoCompactThresholdForWindow(autoCompactWindow) + 1000
	haiku := func() string { return compact.DefaultModel }

	tests := []struct {
		name string
		// active is the server and model the loop talks to; role, when set,
		// is a second server the background role resolves to.
		activeOpenAI bool
		model        string
		background   func() string
		withRole     bool
		wantOnRole   bool
		wantModel    string
	}{
		{name: "Anthropic uses Haiku", model: "claude-sonnet-4-6", background: haiku, wantModel: compact.DefaultModel},
		{name: "OpenAI uses the active model", activeOpenAI: true, model: "qwen3-coder", background: haiku, wantModel: "qwen3-coder"},
		{name: "Copilot-style Claude naming uses the active model", model: "claude-sonnet-4.5", background: haiku, wantModel: "claude-sonnet-4.5"},
		{name: "no background model uses the active model", model: "claude-sonnet-4-6", wantModel: "claude-sonnet-4-6"},
		{name: "background role uses the role's server", model: "claude-sonnet-4-6", background: haiku, withRole: true, wantOnRole: true, wantModel: "role-model"},
		{name: "background role works for an OpenAI active client", activeOpenAI: true, model: "qwen3-coder", background: haiku, withRole: true, wantOnRole: true, wantModel: "role-model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			active := newAutoServer(t, tt.activeOpenAI, over)
			role := newAutoServer(t, true, over)
			lp := autoLoop(active.client(), tt.model, func(c *LoopConfig) {
				c.BackgroundModel = tt.background
				if tt.withRole {
					c.RoleResolver = func(r string) (string, *api.Client, bool) {
						if r != settings.RoleBackground {
							return "", nil, false
						}
						return "role-model", role.client(), true
					}
				}
			})
			ev := &eventLog{}
			if _, err := lp.Run(context.Background(), convo(), ev.handle); err != nil {
				t.Fatalf("Run: %v", err)
			}
			got, other := active, role
			if tt.wantOnRole {
				got, other = role, active
			}
			if models := got.compactionModels(); len(models) != 1 || models[0] != tt.wantModel {
				t.Errorf("compaction models on the expected server = %v, want [%s]", models, tt.wantModel)
			}
			if models := other.compactionModels(); len(models) != 0 {
				t.Errorf("compaction went to the wrong server too: %v", models)
			}
			c := ev.ofType(EventCompacted)
			if len(c) != 1 || c[0].CompactKind != compact.KindAuto || c[0].CompactModel != tt.wantModel ||
				len(c[0].History) == 0 || c[0].CompactedThreshold == 0 {
				t.Errorf("EventCompacted = %+v, want one auto event by %s with history", c, tt.wantModel)
			}
		})
	}
}

func TestLoop_AutoCompact_FollowsFailover(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", fmt.Sprint(autoCompactWindow))
	over := internalmodel.AutoCompactThresholdForWindow(autoCompactWindow) + 1000

	var primaryHits sync.Mutex
	primaryCompactions := 0
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "summarizing a coding conversation") {
			primaryHits.Lock()
			primaryCompactions++
			primaryHits.Unlock()
		}
		w.Header().Set("Retry-After", "9999") // too long to wait: the loop rotates
		http.Error(w, "429 rate limit exceeded", http.StatusTooManyRequests)
	}))
	defer primary.Close()
	fallback := newAutoServer(t, false, over)

	chain := []settings.ActiveProviderSettings{
		{Kind: settings.ProviderKindAnthropicAPI, Model: "primary-model"},
		{Kind: settings.ProviderKindAnthropicAPI, Model: "fallback-model"},
	}
	lp := autoLoop(api.NewClient(api.Config{BaseURL: primary.URL, AuthToken: "t"}, primary.Client()), "primary-model", func(c *LoopConfig) {
		c.BackgroundModel = func() string { return compact.DefaultModel }
	})
	lp.SetProviderChain(settings.RoleDefault,
		func(string) []settings.ActiveProviderSettings { return chain },
		func(p settings.ActiveProviderSettings) (*api.Client, error) {
			if p.Model == "fallback-model" {
				return fallback.client(), nil
			}
			return api.NewClient(api.Config{BaseURL: primary.URL, AuthToken: "t"}, primary.Client()), nil
		})
	if _, err := lp.Run(context.Background(), convo(), (&eventLog{}).handle); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The failed-over model is not dash-style Claude, so the summary is written
	// by it, on its server; the failed provider is not asked again.
	if models := fallback.compactionModels(); len(models) != 1 || models[0] != "fallback-model" {
		t.Errorf("fallback compaction models = %v, want [fallback-model]", models)
	}
	primaryHits.Lock()
	defer primaryHits.Unlock()
	if primaryCompactions != 0 {
		t.Errorf("compaction went to the failed provider %d times", primaryCompactions)
	}
}

func TestLoop_AutoCompact_TriggerCap(t *testing.T) {
	tests := []struct {
		name        string
		windowEnv   string
		window      int
		capFn       func() int
		inputTokens int
		want        bool
	}{
		{"nil cap uses the 256K default", "", 1_000_000, nil, 300_000, true},
		{"nil cap, below default", "", 1_000_000, nil, 200_000, false},
		{"zero cap is uncapped", "", 1_000_000, func() int { return 0 }, 300_000, false},
		{"configured cap fires", "", 1_000_000, func() int { return 100_000 }, 120_000, true},
		{"configured cap above the window threshold changes nothing", "", 200_000, func() int { return 900_000 }, 160_000, false},
		{"window threshold wins under a high cap", "", 200_000, func() int { return 900_000 }, 170_000, true},
		{"CLAUDE_CODE_AUTO_COMPACT_WINDOW caps a configured window", "50000", 1_000_000, nil, 40_000, true},
		// 32K local models: window - 33K is negative; the floor is 75%.
		{"32K window does not compact on a short prompt", "", 32_768, nil, 5_000, false},
		{"32K window compacts past 75%", "", 32_768, nil, 25_000, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", tt.windowEnv)
			srv := newAutoServer(t, false, tt.inputTokens)
			lp := autoLoop(srv.client(), "claude-sonnet-4-6", func(c *LoopConfig) {
				c.ContextWindow = tt.window
				c.AutoCompactTriggerCap = tt.capFn
			})
			if _, err := lp.Run(context.Background(), convo(), (&eventLog{}).handle); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if got := len(srv.compactionModels()) == 1; got != tt.want {
				t.Errorf("compacted = %v, want %v (input %d)", got, tt.want, tt.inputTokens)
			}
		})
	}
}

func TestLoop_AutoCompact_Breaker(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", fmt.Sprint(autoCompactWindow))
	over := internalmodel.AutoCompactThresholdForWindow(autoCompactWindow) + 1000

	tests := []struct {
		name string
		// compactions are the statuses the compaction endpoint returns in order
		// (0 = success); one Run is made per element plus a final probe.
		compactions []int
		reset       func(*Loop)
		// wantAttempts is how many compactions were asked for in total,
		// including the probe Run.
		wantAttempts int
	}{
		{"three failures open the breaker", []int{400, 400, 400}, nil, 3},
		{"two failures leave it closed", []int{400, 400}, nil, 3},
		{"transient failures never count", []int{503, 503, 503, 503, 503}, nil, 6},
		{"a success resets the count", []int{400, 400, 0, 400, 400}, nil, 6},
		{"SetModel resets it", []int{400, 400, 400}, func(l *Loop) { l.SetModel("claude-sonnet-4-6") }, 4},
		{"SetClient resets it", []int{400, 400, 400}, func(l *Loop) { l.SetClient(l.client) }, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The probe Run's compaction answers like the last status.
			srv := newAutoServer(t, false, over, tt.compactions...)
			lp := autoLoop(srv.client(), "claude-sonnet-4-6", nil)
			for range tt.compactions {
				lp.compactStuck.Store(0) // the tiny test history cannot get under the trigger; not under test here
				if _, err := lp.Run(context.Background(), convo(), (&eventLog{}).handle); err != nil {
					t.Fatalf("Run: %v", err)
				}
			}
			if tt.reset != nil {
				tt.reset(lp)
			}
			lp.compactStuck.Store(0)
			if _, err := lp.Run(context.Background(), convo(), (&eventLog{}).handle); err != nil {
				t.Fatalf("probe Run: %v", err)
			}
			if got := len(srv.compactionModels()); got != tt.wantAttempts {
				t.Errorf("compaction attempts = %d, want %d", got, tt.wantAttempts)
			}
		})
	}
}

func TestLoop_AutoCompact_ChildrenSkipEndTurnCompaction(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", fmt.Sprint(autoCompactWindow))
	over := internalmodel.AutoCompactThresholdForWindow(autoCompactWindow) + 1000
	srv := newAutoServer(t, false, over)
	lp := autoLoop(srv.client(), "claude-sonnet-4-6", nil)

	if _, err := lp.RunSubAgentTyped(context.Background(), "go", SubAgentSpec{}); err != nil {
		t.Fatalf("RunSubAgentTyped: %v", err)
	}
	if got := srv.compactionModels(); len(got) != 0 {
		t.Errorf("a sub-agent compacted at end_turn: %v", got)
	}
	if _, err := lp.Run(context.Background(), convo(), (&eventLog{}).handle); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := srv.compactionModels(); len(got) != 1 {
		t.Errorf("the root loop compactions = %v, want 1", got)
	}
}

func TestLoop_ChildKeepsReactiveCompaction(t *testing.T) {
	// The child calls a tool first, so the overflow finds a prompt plus an
	// assistant turn: something before the verbatim tail to summarize.
	toolTurn := scripted{body: sseMessage("tool_use", sseToolBlock(0, "toolu_1", "Echo", `{}`, true))}
	ss := &scriptServer{script: []scripted{toolTurn, overflowResp(promptTooLong), summaryResp("S"), okResp("child done")}}
	srv := httptest.NewServer(ss)
	defer srv.Close()
	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Echo", result: "ok"})
	lp := NewLoop(api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client()), reg, LoopConfig{
		Model: "m", MaxTokens: 1024, AutoCompact: true,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if _, err := lp.RunSubAgentTyped(context.Background(), "go", SubAgentSpec{}); err != nil {
		t.Fatalf("RunSubAgentTyped: %v", err)
	}
	if got, want := requestKinds(ss), []string{"main", "main", "compaction", "main"}; !equalStrings(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
}

func TestLoop_AutoCompact_ReportsSummarizerUsage(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", fmt.Sprint(autoCompactWindow))
	over := internalmodel.AutoCompactThresholdForWindow(autoCompactWindow) + 1000
	srv := newAutoServer(t, false, over)
	var (
		mu     sync.Mutex
		models []string
		used   []api.Usage
	)
	lp := autoLoop(srv.client(), "claude-sonnet-4-6", func(c *LoopConfig) {
		c.BackgroundModel = func() string { return compact.DefaultModel }
		c.OnSubAgentUsage = func(model string, u api.Usage) {
			mu.Lock()
			defer mu.Unlock()
			models, used = append(models, model), append(used, u)
		}
	})
	if _, err := lp.Run(context.Background(), convo(), (&eventLog{}).handle); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(models) != 1 || models[0] != compact.DefaultModel || used[0].InputTokens != 10 {
		t.Errorf("reported usage = %v %+v, want the summarizer's 10 input tokens on %s", models, used, compact.DefaultModel)
	}
}

func (a *autoServer) setTokens(n int) {
	a.mu.Lock()
	a.mainTokens = n
	a.mu.Unlock()
}

// On a small window the system prompt and tools alone sit near the trigger, so
// a compaction cannot get under it. It must not run again on every end_turn.
func TestLoop_AutoCompact_NoThrashOnSmallWindow(t *testing.T) {
	const window = 32_768
	trigger := internalmodel.AutoCompactThreshold(window, internalmodel.DefaultAutoCompactTriggerCap)
	stuckTokens := trigger + 500 // conversation is tiny; ~all of this is system + tools
	srv := newAutoServer(t, false, stuckTokens)
	lp := autoLoop(srv.client(), "claude-sonnet-4-6", func(c *LoopConfig) { c.ContextWindow = window })
	run := func() {
		t.Helper()
		if _, err := lp.Run(context.Background(), convo(), (&eventLog{}).handle); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	compactions := func() int { return len(srv.compactionModels()) }

	run()
	if compactions() != 1 {
		t.Fatalf("first end_turn: compactions = %d, want 1", compactions())
	}
	run()
	run()
	if compactions() != 1 {
		t.Errorf("after two more end_turns compactions = %d, want still 1 (the last one could not help)", compactions())
	}

	// Real growth since lets it try again.
	srv.setTokens(stuckTokens * 2)
	run()
	if compactions() != 2 {
		t.Errorf("after the prompt grew compactions = %d, want 2", compactions())
	}

	// Dropping under the trigger clears the guard.
	srv.setTokens(1_000)
	run()
	srv.setTokens(stuckTokens)
	run()
	if compactions() != 3 {
		t.Errorf("after falling under the trigger compactions = %d, want 3", compactions())
	}

	// So do model and client changes.
	run()
	if compactions() != 3 {
		t.Fatalf("setup: compactions = %d, want 3 (guard re-armed)", compactions())
	}
	lp.SetModel("claude-sonnet-4-6")
	run()
	if compactions() != 4 {
		t.Errorf("after SetModel compactions = %d, want 4", compactions())
	}
}
