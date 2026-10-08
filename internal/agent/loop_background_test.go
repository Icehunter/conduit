package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
)

func TestBackgroundTarget(t *testing.T) {
	anthropic := api.NewClient(api.Config{BaseURL: "http://127.0.0.1:1", AuthToken: "t"}, nil)
	firstParty := api.NewClient(api.Config{AuthToken: "t"}, nil)
	openAI := api.NewClient(api.Config{BaseURL: "http://127.0.0.1:1", APIKey: "k", ProviderKind: api.ProviderKindOpenAICompatible}, nil)
	responses := api.NewClient(api.Config{BaseURL: "http://127.0.0.1:1", APIKey: "k", ProviderKind: api.ProviderKindOpenAIResponses}, nil)
	roleClient := api.NewClient(api.Config{BaseURL: "http://127.0.0.1:2", APIKey: "k", ProviderKind: api.ProviderKindOpenAICompatible}, nil)
	copilot := api.NewClient(api.Config{BaseURL: "https://api.githubcopilot.com", AuthToken: "t"}, nil)

	haiku := func() string { return compact.DefaultModel }
	role := func(model string, c *api.Client) func(string) (string, *api.Client, bool) {
		return func(r string) (string, *api.Client, bool) {
			if r != settings.RoleBackground {
				t.Errorf("RoleResolver asked for %q, want the background role", r)
			}
			return model, c, true
		}
	}

	tests := []struct {
		name        string
		active      *api.Client
		model       string
		background  func() string
		roleFn      func(string) (string, *api.Client, bool)
		wantClient  *api.Client
		wantModel   string
		wantBGModel string // Loop.BackgroundModel()
	}{
		{"first-party Anthropic uses Haiku", firstParty, "claude-sonnet-4-6", haiku, nil, firstParty, compact.DefaultModel, compact.DefaultModel},
		{"first-party Anthropic with a non-dash model still uses Haiku", firstParty, "claude-sonnet-4.5", haiku, nil, firstParty, compact.DefaultModel, compact.DefaultModel},
		{"Messages gateway with dash-style IDs uses Haiku", anthropic, "claude-sonnet-4-6", haiku, nil, anthropic, compact.DefaultModel, compact.DefaultModel},
		{"Copilot-style dotted IDs use the active model", anthropic, "claude-sonnet-4.5", haiku, nil, anthropic, "claude-sonnet-4.5", "claude-sonnet-4.5"},
		{"Messages gateway with a custom model name uses the active model", anthropic, "my-model", haiku, nil, anthropic, "my-model", "my-model"},
		{"OpenAI-compatible uses the active model", openAI, "qwen3", haiku, nil, openAI, "qwen3", "qwen3"},
		{"OpenAI Responses uses the active model", responses, "gpt-5", haiku, nil, responses, "gpt-5", "gpt-5"},
		{"no background model configured uses the active model", anthropic, "claude-sonnet-4-6", nil, nil, anthropic, "claude-sonnet-4-6", "claude-sonnet-4-6"},
		// A configured non-Claude model belongs to the background role's own
		// provider, which RoleResolver's client carries; without it the active
		// model is used.
		{"a non-Claude background model without its client is not used", openAI, "qwen3", func() string { return "qwen3-small" }, nil, openAI, "qwen3", "qwen3"},
		{"a non-Claude background model on an Anthropic client is not used", anthropic, "claude-sonnet-4-6", func() string { return "qwen3-small" }, nil, anthropic, "claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"Copilot with dash-style sonnet-4 uses the active model", copilot, "claude-sonnet-4", haiku, nil, copilot, "claude-sonnet-4", "claude-sonnet-4"},
		{"Copilot with dash-style opus-4 uses the active model", copilot, "claude-opus-4", haiku, nil, copilot, "claude-opus-4", "claude-opus-4"},
		{"Copilot with dash-style opus-41 uses the active model", copilot, "claude-opus-41", haiku, nil, copilot, "claude-opus-41", "claude-opus-41"},
		{"role with its own client wins", anthropic, "claude-sonnet-4-6", haiku, role("role-model", roleClient), roleClient, "role-model", compact.DefaultModel},
		{"role without a client uses the active Anthropic client", anthropic, "claude-sonnet-4-6", haiku, role("claude-sonnet-4-6", nil), anthropic, "claude-sonnet-4-6", compact.DefaultModel},
		{"role without a client naming a non-Claude model is refused", anthropic, "claude-sonnet-4-6", haiku, role("role-model", nil), anthropic, compact.DefaultModel, compact.DefaultModel},
		{"role without a client is refused on an OpenAI client", openAI, "qwen3", haiku, role("claude-haiku-5-5", nil), openAI, "qwen3", "qwen3"},
		{"role with an empty model is ignored", openAI, "qwen3", nil, role("", roleClient), openAI, "qwen3", "qwen3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lp := NewLoop(tt.active, nil, LoopConfig{Model: tt.model, BackgroundModel: tt.background, RoleResolver: tt.roleFn})
			c, m := lp.backgroundTarget(tt.active, tt.model)
			if c != tt.wantClient || m != tt.wantModel {
				t.Errorf("backgroundTarget = (%p, %q), want (%p, %q)", c, m, tt.wantClient, tt.wantModel)
			}
			if got := lp.BackgroundModel(); got != tt.wantBGModel {
				t.Errorf("BackgroundModel() = %q, want %q", got, tt.wantBGModel)
			}
		})
	}
}

// TestBackgroundTarget_NeverSendsClaudeToANonAnthropicClient is the contract
// behind memory extraction and the "haiku" alias on local providers.
func TestBackgroundModel_NeverClaudeForNonAnthropicClient(t *testing.T) {
	openAI := api.NewClient(api.Config{BaseURL: "http://127.0.0.1:1", APIKey: "k", ProviderKind: api.ProviderKindOpenAICompatible}, nil)
	lp := NewLoop(openAI, nil, LoopConfig{Model: "qwen3", BackgroundModel: func() string { return compact.DefaultModel }})
	for _, alias := range []string{"", "inherit", "background", "haiku", "fast"} {
		if got := lp.resolveModelAlias(alias); got != "qwen3" {
			t.Errorf("resolveModelAlias(%q) = %q, want the local model", alias, got)
		}
	}
}

// TestBuildChildLoop_BackgroundRole is G's routing: a review runner asking for
// the background role gets the role's client and model, and without a role
// never a Claude model on a client that cannot serve one.
func TestBuildChildLoop_BackgroundRole(t *testing.T) {
	openAI := api.NewClient(api.Config{BaseURL: "http://127.0.0.1:1", APIKey: "k", ProviderKind: api.ProviderKindOpenAICompatible}, nil)
	roleClient := api.NewClient(api.Config{BaseURL: "http://127.0.0.1:2", AuthToken: "t"}, nil)
	haiku := func() string { return compact.DefaultModel }

	t.Run("role client and model", func(t *testing.T) {
		lp := NewLoop(openAI, nil, LoopConfig{
			Model: "qwen3", BackgroundModel: haiku,
			RoleResolver: func(string) (string, *api.Client, bool) { return "role-model", roleClient, true },
		})
		child, model := lp.buildChildLoop(SubAgentSpec{Role: settings.RoleBackground, Background: true})
		if child.client != roleClient || model != "role-model" || child.cfg.Model != "role-model" {
			t.Errorf("child = (%p, %q, cfg %q), want the role's client and model", child.client, model, child.cfg.Model)
		}
	})
	t.Run("no role on a local client", func(t *testing.T) {
		lp := NewLoop(openAI, nil, LoopConfig{Model: "qwen3", BackgroundModel: haiku})
		child, model := lp.buildChildLoop(SubAgentSpec{Role: settings.RoleBackground, Background: true})
		if child.client != openAI || model != "qwen3" {
			t.Errorf("child = (%p, %q), want the active client and model (Haiku cannot be served there)", child.client, model)
		}
	})
	t.Run("an explicit model still wins", func(t *testing.T) {
		lp := NewLoop(openAI, nil, LoopConfig{Model: "qwen3", BackgroundModel: haiku})
		_, model := lp.buildChildLoop(SubAgentSpec{Role: settings.RoleBackground, Model: "qwen3-small"})
		if model != "qwen3-small" {
			t.Errorf("model = %q, want the explicit one", model)
		}
	})
}

// TestRunBackgroundAgent_Routing sends a real background run through the
// background role's server, not the active one.
func TestRunBackgroundAgent_Routing(t *testing.T) {
	active := newAutoServer(t, false, 10)
	role := newAutoServer(t, true, 10)
	lp := autoLoop(active.client(), "claude-sonnet-4-6", func(c *LoopConfig) {
		c.AutoCompact = false
		c.BackgroundModel = func() string { return compact.DefaultModel }
		c.RoleResolver = func(string) (string, *api.Client, bool) { return "role-model", role.client(), true }
	})
	if _, err := lp.RunBackgroundAgent(context.Background(), "extract memories"); err != nil {
		t.Fatalf("RunBackgroundAgent: %v", err)
	}
	if got := role.requests(); len(got) != 1 || got[0].model != "role-model" {
		t.Errorf("role server saw %+v, want one request for role-model", got)
	}
	if got := active.requests(); len(got) != 0 {
		t.Errorf("active server saw %+v, want nothing", got)
	}
}

func TestRunBackgroundAgent_LocalProviderNeverSeesClaude(t *testing.T) {
	local := newAutoServer(t, true, 10)
	lp := autoLoop(local.client(), "qwen3", func(c *LoopConfig) {
		c.AutoCompact = false
		c.BackgroundModel = func() string { return compact.DefaultModel }
	})
	if _, err := lp.RunBackgroundAgent(context.Background(), "extract memories"); err != nil {
		t.Fatalf("RunBackgroundAgent: %v", err)
	}
	if got := local.requests(); len(got) != 1 || got[0].model != "qwen3" {
		t.Errorf("local server saw %+v, want one request for qwen3", got)
	}
}

func TestSummarizerWindow(t *testing.T) {
	t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", "")
	active := api.NewClient(api.Config{BaseURL: "http://127.0.0.1:1", APIKey: "k", ProviderKind: api.ProviderKindOpenAICompatible}, nil)
	role := api.NewClient(api.Config{BaseURL: "http://127.0.0.1:2", APIKey: "k", ProviderKind: api.ProviderKindOpenAICompatible}, nil)
	lp := NewLoop(active, nil, LoopConfig{RoleContextWindow: func(r string) int {
		if r != settings.RoleBackground {
			t.Errorf("asked for role %q", r)
		}
		return 65_536
	}})
	rs := &runState{client: active, model: "qwen3", window: 32_768}
	tests := []struct {
		name  string
		c     *api.Client
		model string
		want  int
	}{
		{"active model uses the Run's window", active, "qwen3", 32_768},
		{"role client uses its provider's window", role, "role-model", 65_536},
		{"a Claude model on the active client is derived from its name", active, "claude-haiku-5-5", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lp.summarizerWindow(rs, tt.c, tt.model); got != tt.want {
				t.Errorf("summarizerWindow = %d, want %d", got, tt.want)
			}
		})
	}
}

// The summarizer's transcript is capped to its own window, not the default 200K.
func TestCompactHistory_SizesInputToTheSummarizer(t *testing.T) {
	var sizes []int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		sizes = append(sizes, len(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseMessage("end_turn", sseTextBlock("<summary>S</summary>")))
	}))
	defer srv.Close()
	client := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	lp := NewLoop(client, tool.NewRegistry(), LoopConfig{Model: "local-model", MaxTokens: 1024, ContextWindow: 16_000})
	msgs := make([]api.Message, 0, 120)
	for i := range 60 {
		msgs = append(msgs, user(fmt.Sprintf("q%d %s", i, strings.Repeat("w ", 1500))), asst(text(strings.Repeat("a ", 1500))))
	}
	rs := &runState{client: client, model: "local-model", window: 16_000}
	if _, _, err := lp.compactHistory(context.Background(), rs, msgs, compact.KindAuto, false, func(LoopEvent) {}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	// (16000 - 8192 - 4096) tokens at 3 chars, plus the wrapper: well under 200K.
	if len(sizes) != 1 || sizes[0] > 40_000 {
		t.Errorf("summarizer request sizes = %v, want one under 40KB (capped to the 16K window)", sizes)
	}
}
