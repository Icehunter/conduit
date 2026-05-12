package microcompact

import (
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/api"
)

func msgs() []api.Message {
	// 8 turns: assistant tool_use (toolu_1..8) → user tool_result.
	out := []api.Message{}
	for i := 1; i <= 8; i++ {
		id := "toolu_" + string(rune('0'+i))
		out = append(out, api.Message{
			Role: "assistant",
			Content: []api.ContentBlock{
				{Type: "tool_use", ID: id, Name: "Bash"},
			},
		})
		out = append(out, api.Message{
			Role: "user",
			Content: []api.ContentBlock{
				{Type: "tool_result", ToolUseID: id, ResultContent: strings.Repeat("output line\n", 100)},
			},
		})
	}
	return out
}

func TestApply_NoOpBelowThreshold(t *testing.T) {
	got := Apply(msgs(), time.Now(), DefaultThreshold, DefaultKeepRecent)
	if got.Triggered {
		t.Errorf("expected no-op below threshold; got %+v", got)
	}
	if got.Cleared != 0 {
		t.Errorf("Cleared = %d; want 0", got.Cleared)
	}
}

func TestApply_NoOpWithZeroLastAssistantTime(t *testing.T) {
	got := Apply(msgs(), time.Time{}, DefaultThreshold, DefaultKeepRecent)
	if got.Triggered {
		t.Errorf("expected no-op with zero time; got %+v", got)
	}
}

func TestApply_ClearsOlderToolResults(t *testing.T) {
	in := msgs()
	got := Apply(in, time.Now().Add(-2*time.Hour), DefaultThreshold, DefaultKeepRecent)
	if !got.Triggered {
		t.Fatalf("expected trigger after long gap; got %+v", got)
	}
	// 8 tool_uses, keep 5 → clear 3.
	if got.Cleared != 3 {
		t.Errorf("Cleared = %d; want 3", got.Cleared)
	}
	if got.TokensSaved == 0 {
		t.Error("expected non-zero tokens saved")
	}
	// Verify earliest tool_results are now placeholders, last 5 are intact.
	cleared := 0
	intact := 0
	for _, m := range got.Messages {
		if m.Role != "user" {
			continue
		}
		for _, b := range m.Content {
			if b.Type != "tool_result" {
				continue
			}
			if b.ResultContent == ClearedMessage {
				cleared++
			} else {
				intact++
			}
		}
	}
	if cleared != 3 {
		t.Errorf("placeholders = %d; want 3", cleared)
	}
	if intact != 5 {
		t.Errorf("intact tool_results = %d; want 5", intact)
	}
}

func TestApply_NoOpWhenAllInKeepWindow(t *testing.T) {
	// Only 3 tool_uses but keepRecent=5 — nothing eligible to clear.
	in := []api.Message{
		{Role: "assistant", Content: []api.ContentBlock{{Type: "tool_use", ID: "a"}}},
		{Role: "user", Content: []api.ContentBlock{{Type: "tool_result", ToolUseID: "a", ResultContent: "x"}}},
	}
	got := Apply(in, time.Now().Add(-2*time.Hour), DefaultThreshold, DefaultKeepRecent)
	if got.Triggered {
		t.Errorf("expected no-op when nothing eligible; got %+v", got)
	}
}

func TestApply_KeepRecentFlooredAtOne(t *testing.T) {
	got := Apply(msgs(), time.Now().Add(-2*time.Hour), DefaultThreshold, 0)
	if got.Cleared != 7 {
		t.Errorf("with keepRecent=0 floored to 1, expected 7 cleared; got %d", got.Cleared)
	}
}

func TestApply_IdempotentOnSecondPass(t *testing.T) {
	first := Apply(msgs(), time.Now().Add(-2*time.Hour), DefaultThreshold, DefaultKeepRecent)
	second := Apply(first.Messages, time.Now().Add(-2*time.Hour), DefaultThreshold, DefaultKeepRecent)
	if second.Cleared != 0 {
		t.Errorf("second pass should clear nothing (already placeholders); cleared=%d", second.Cleared)
	}
}

func TestApply_ProtectsErrorToolResults(t *testing.T) {
	// Create messages with both error and success tool_results.
	in := []api.Message{}
	for i := 1; i <= 8; i++ {
		id := "toolu_" + string(rune('0'+i))
		in = append(in, api.Message{
			Role: "assistant",
			Content: []api.ContentBlock{
				{Type: "tool_use", ID: id, Name: "Bash"},
			},
		})
		// Make tools 2 and 4 be errors.
		isError := i == 2 || i == 4
		in = append(in, api.Message{
			Role: "user",
			Content: []api.ContentBlock{
				{Type: "tool_result", ToolUseID: id, IsError: isError, ResultContent: strings.Repeat("output line\n", 100)},
			},
		})
	}

	got := Apply(in, time.Now().Add(-2*time.Hour), DefaultThreshold, DefaultKeepRecent)
	if !got.Triggered {
		t.Fatalf("expected trigger; got %+v", got)
	}

	// Verify error tool_results are never cleared.
	for _, m := range got.Messages {
		if m.Role != "user" {
			continue
		}
		for _, b := range m.Content {
			if b.Type != "tool_result" {
				continue
			}
			if b.IsError && b.ResultContent == ClearedMessage {
				t.Errorf("error tool_result %s was cleared but should be protected", b.ToolUseID)
			}
		}
	}

	// Of the 8 tool_results, 5 are in keepRecent window (4,5,6,7,8).
	// Outside window: 1, 2, 3. Tools 2 and 4 are errors, but 4 is in keepRecent.
	// So outside and not error: tools 1 and 3 → 2 should be cleared.
	if got.Cleared != 2 {
		t.Errorf("Cleared = %d; want 2 (3 outside keepRecent minus 1 error)", got.Cleared)
	}
}
