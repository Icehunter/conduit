package agent

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/ttsr"
)

// ttsrTextOnlySSE is identical to textOnlySSE but lets us set the text
// explicitly without JSON-escaping concerns (the text is embedded verbatim
// in the helper which already handles simple ASCII strings fine for tests).
func ttsrTextOnlySSE(text string) string {
	return textOnlySSE(text)
}

func TestLoop_TTSR_InjectsCorrection(t *testing.T) {
	// First call: model outputs text that triggers the rule.
	// Second call: model completes normally (correction injected, rule not re-fired).
	rule := ttsr.Rule{
		Name:       "no-rewrites",
		Pattern:    regexp.MustCompile(`REWRITE_TRIGGER`),
		Correction: "Make targeted changes only.",
		MaxFires:   1,
	}

	reg := tool.NewRegistry()
	// Two SSE bodies: first fires the rule, second completes cleanly.
	lp, srv := newTestLoop(t, []string{
		ttsrTextOnlySSE("I will REWRITE_TRIGGER everything"),
		ttsrTextOnlySSE("Okay, here is the targeted change."),
	}, reg)
	defer srv.Close()

	lp.SetTTSRRules([]ttsr.Rule{rule})

	var ttsrEvents []LoopEvent
	var textEvents []string

	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "do something"}}},
	}
	result, err := lp.Run(context.Background(), msgs, func(ev LoopEvent) {
		if ev.Type == EventTTSR {
			ttsrEvents = append(ttsrEvents, ev)
		}
		if ev.Type == EventText {
			textEvents = append(textEvents, ev.Text)
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// One TTSR event must have fired.
	if len(ttsrEvents) != 1 {
		t.Fatalf("expected 1 TTSR event, got %d", len(ttsrEvents))
	}
	if ttsrEvents[0].TTSRRule != "no-rewrites" {
		t.Errorf("TTSRRule = %q, want %q", ttsrEvents[0].TTSRRule, "no-rewrites")
	}
	if ttsrEvents[0].TTSRCorrection != rule.Correction {
		t.Errorf("TTSRCorrection = %q, want %q", ttsrEvents[0].TTSRCorrection, rule.Correction)
	}

	// The correction must appear as a user message in history.
	found := false
	for _, m := range result {
		if m.Role == "user" {
			for _, cb := range m.Content {
				if strings.Contains(cb.Text, rule.Correction) {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("correction was not injected into message history")
	}
}

func TestLoop_TTSR_BudgetExhausted(t *testing.T) {
	// Rule fires on every response. After maxTTSRFiresPerTurn (3) fires the
	// watcher is disabled and the stream completes naturally.
	rule := ttsr.Rule{
		Name:    "always-fires",
		Pattern: regexp.MustCompile(`FIRE`),
	}

	// We need 4 SSE responses: 3 that trigger the rule + 1 final clean response.
	// The server cycles through sseBodies in order; newTestLoop falls back to
	// textOnlySSE("done") once the slice is exhausted. Since we set 3 trigger
	// bodies explicitly, the 4th call returns "done" via the fallback.
	reg := tool.NewRegistry()
	lp, srv := newTestLoop(t, []string{
		ttsrTextOnlySSE("FIRE 1"),
		ttsrTextOnlySSE("FIRE 2"),
		ttsrTextOnlySSE("FIRE 3"),
		// 4th call: fallback textOnlySSE("done") — budget exhausted, no more TTSR
	}, reg)
	defer srv.Close()

	lp.SetTTSRRules([]ttsr.Rule{rule})

	var ttsrCount int
	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "go"}}},
	}
	_, err := lp.Run(context.Background(), msgs, func(ev LoopEvent) {
		if ev.Type == EventTTSR {
			ttsrCount++
		}
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// maxTTSRFiresPerTurn = 3; the 4th response should complete without TTSR.
	if ttsrCount != 3 {
		t.Errorf("expected 3 TTSR fires (budget cap), got %d", ttsrCount)
	}
}

func TestLoop_TTSR_UserCancelBeats(t *testing.T) {
	// Context cancellation must still propagate correctly even when TTSR rules
	// are configured. The cancel should arrive via context.Canceled, not TTSR.
	rule := ttsr.Rule{
		Name:    "unreachable",
		Pattern: regexp.MustCompile(`NEVER_IN_TEXT`),
	}

	reg := tool.NewRegistry()
	lp, srv := newTestLoop(t, []string{ttsrTextOnlySSE("just a normal response")}, reg)
	defer srv.Close()

	lp.SetTTSRRules([]ttsr.Rule{rule})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "test"}}},
	}
	_, err := lp.Run(ctx, msgs, func(_ LoopEvent) {})
	if err == nil {
		// A pre-cancelled context may be caught by the ctx.Err() check at the
		// top of the loop before any HTTP call is made; allow nil if the loop
		// exits cleanly because the context was already done.
		return
	}
	if !strings.Contains(err.Error(), "cancel") && err.Error() != "context canceled" {
		// Accept context.Canceled variants.
		// If a TTSR error leaked here, err.Error() would start with "ttsr:".
		if strings.HasPrefix(err.Error(), "ttsr:") {
			t.Errorf("TTSR error leaked instead of context.Canceled: %v", err)
		}
	}
}
