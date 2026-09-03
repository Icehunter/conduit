package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/permissions"
	"github.com/icehunter/conduit/internal/tool"
)

func TestSubAgentModeIsolation(t *testing.T) {
	// Build a minimal parent gate in default mode.
	parentGate := permissions.New("", nil, permissions.ModeDefault, nil, nil, nil)

	// Clone produces an independent gate.
	childGate := parentGate.Clone()
	childGate.SetMode(permissions.ModePlan)

	// Parent must remain unchanged after child mode is set.
	if parentGate.Mode() != permissions.ModeDefault {
		t.Errorf("parent mode changed: got %v, want ModeDefault", parentGate.Mode())
	}
	if childGate.Mode() != permissions.ModePlan {
		t.Errorf("child mode wrong: got %v, want ModePlan", childGate.Mode())
	}

	// Changing parent after clone must not affect the child.
	parentGate.SetMode(permissions.ModeBypassPermissions)
	if childGate.Mode() != permissions.ModePlan {
		t.Errorf("child mode leaked from parent: got %v, want ModePlan", childGate.Mode())
	}

	// Verify we can construct a Loop with the cloned gate without panicking.
	parentLoop := &Loop{
		cfg: LoopConfig{
			Gate:  parentGate,
			Model: "claude-haiku-4-5-20251001",
		},
	}
	_ = parentLoop
}

// newAskSubAgentTestLoop builds a Loop shaped like a sub-agent — AskPermission
// nil (sub-agents never have one), a gate in ModeDefault, and
// AskSubAgentPermission wired to fn — running against a server that has the
// model call one Bash tool_use then finish. Mirrors
// TestLoop_AskMode_AlwaysAllowAddsSessionRule's harness in loop_test.go.
func newAskSubAgentTestLoop(t *testing.T, fn func(ctx context.Context, label, toolName, toolInput string) SubAgentPermVerdict) *Loop {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls <= 1 {
			_, _ = w.Write([]byte(singleToolUseSSE("toolu_sub_01")))
		} else {
			_, _ = w.Write([]byte(textOnlySSE("done")))
		}
	}))
	t.Cleanup(srv.Close)

	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Bash", result: "output"})

	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "test"}, srv.Client())
	gate := permissions.New("", nil, permissions.ModeDefault, nil, nil, nil)
	lp := NewLoop(c, reg, LoopConfig{
		Model: "m", MaxTokens: 1024,
		System:                []api.SystemBlock{{Type: "text", Text: "s"}},
		Gate:                  gate,
		AskSubAgentPermission: fn,
	})
	lp.subAgentLabel = "test-subagent"
	return lp
}

// TestLoop_AskSubAgentPermission_Deny verifies a relayed Deny prevents the
// tool from running and the loop still finishes cleanly.
func TestLoop_AskSubAgentPermission_Deny(t *testing.T) {
	var gotLabel, gotTool string
	lp := newAskSubAgentTestLoop(t, func(_ context.Context, label, toolName, _ string) SubAgentPermVerdict {
		gotLabel, gotTool = label, toolName
		return SubAgentPermDeny
	})

	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "go"}}},
	}, func(LoopEvent) {})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotLabel != "test-subagent" {
		t.Errorf("label passed to relay = %q, want %q", gotLabel, "test-subagent")
	}
	if gotTool != "Bash" {
		t.Errorf("toolName passed to relay = %q, want Bash", gotTool)
	}
}

// TestLoop_AskSubAgentPermission_SwitchToAuto verifies that choosing "switch
// to auto mode" flips the gate to bypassPermissions, so a second Ask-tier
// call in the same run no longer invokes the relay at all.
func TestLoop_AskSubAgentPermission_SwitchToAuto(t *testing.T) {
	relayCalls := 0
	toolCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		toolCalls++
		if toolCalls <= 2 {
			_, _ = w.Write([]byte(singleToolUseSSE("toolu_sub_0" + string(rune('0'+toolCalls)))))
		} else {
			_, _ = w.Write([]byte(textOnlySSE("done")))
		}
	}))
	t.Cleanup(srv.Close)

	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Bash", result: "output"})

	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "test"}, srv.Client())
	gate := permissions.New("", nil, permissions.ModeDefault, nil, nil, nil)
	lp := NewLoop(c, reg, LoopConfig{
		Model: "m", MaxTokens: 1024,
		System: []api.SystemBlock{{Type: "text", Text: "s"}},
		Gate:   gate,
		AskSubAgentPermission: func(context.Context, string, string, string) SubAgentPermVerdict {
			relayCalls++
			return SubAgentPermSwitchToAuto
		},
	})
	lp.subAgentLabel = "test-subagent"

	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "go"}}},
	}, func(LoopEvent) {})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if relayCalls != 1 {
		t.Errorf("relay called %d times; want exactly 1 (second Bash call should auto-allow via bypassPermissions)", relayCalls)
	}
	if gate.Mode() != permissions.ModeBypassPermissions {
		t.Errorf("gate.Mode() = %v, want ModeBypassPermissions after switch-to-auto", gate.Mode())
	}
}

// TestRunSubAgentTyped_PropagatesLabelToRelay is a regression test for a bug
// a live TUI test caught: runSubAgentOnce (which RunSubAgentTyped calls) and
// SpawnTeammate both build their child via the shared buildChildLoop helper,
// which never set the new Loop.subAgentLabel field. The relay fired
// correctly but with an empty label, rendering identical to the old direct
// permission prompt and masking that the bubble-up was even active. This
// test goes through the real public entrypoint (RunSubAgentTyped), not a
// hand-constructed Loop, so it actually exercises buildChildLoop's caller.
func TestRunSubAgentTyped_PropagatesLabelToRelay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(singleToolUseSSE("toolu_typed_01")))
	}))
	t.Cleanup(srv.Close)

	reg := tool.NewRegistry()
	reg.Register(&fakeTool{name: "Bash", result: "output"})

	c := api.NewClient(api.Config{BaseURL: srv.URL, AuthToken: "test"}, srv.Client())
	gate := permissions.New("", nil, permissions.ModeDefault, nil, nil, nil)

	var gotLabel string
	lp := NewLoop(c, reg, LoopConfig{
		Model: "m", MaxTokens: 1024,
		System: []api.SystemBlock{{Type: "text", Text: "s"}},
		Gate:   gate,
		AskSubAgentPermission: func(_ context.Context, label, _, _ string) SubAgentPermVerdict {
			gotLabel = label
			return SubAgentPermDeny
		},
	})

	_, _ = lp.RunSubAgentTyped(context.Background(), "do the touch thing", SubAgentSpec{})

	if gotLabel == "" {
		t.Error("label passed to AskSubAgentPermission is empty — buildChildLoop's caller didn't set child.subAgentLabel")
	}
	if gotLabel != "do the touch thing" {
		t.Errorf("label = %q, want %q (derived from the prompt)", gotLabel, "do the touch thing")
	}
}

// TestLoop_AskSubAgentPermission_NilFallsBackToSilentAllow verifies the
// pre-existing headless/print-mode behavior is unchanged when no relay is
// wired at all: a sub-agent-shaped loop with neither AskPermission nor
// AskSubAgentPermission still auto-allows in non-Plan modes.
func TestLoop_AskSubAgentPermission_NilFallsBackToSilentAllow(t *testing.T) {
	lp := newAskSubAgentTestLoop(t, nil)

	_, err := lp.Run(context.Background(), []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "go"}}},
	}, func(LoopEvent) {})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
}
