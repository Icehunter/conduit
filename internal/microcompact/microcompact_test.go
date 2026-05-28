package microcompact

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/api"
)

func msgs() []api.Message {
	// 8 turns: assistant tool_use (toolu_1..8) -> user tool_result.
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

// isPlaceholder returns true if content looks like any microcompact placeholder
// (legacy ClearedMessage, informative 1-liner, or dedup marker).
func isPlaceholder(content string) bool {
	return isAlreadyCleared(content)
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
	// 8 tool_uses, keep 5. The 3 oldest are identical, so dedup collapses
	// them: the first gets an informative placeholder (Cleared=1) and the
	// remaining 2 get dedup markers. At least 1 must be explicitly cleared.
	if got.Cleared < 1 {
		t.Errorf("Cleared = %d; want >= 1", got.Cleared)
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
			if isPlaceholder(b.ResultContent) {
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
	// Only 3 tool_uses but keepRecent=5 -- nothing eligible to clear.
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
	// keepRecent floored to 1 → keep toolu_8. The 7 candidates are identical,
	// so dedup collapses them: toolu_1 gets an informative placeholder, toolu_2..7
	// get dedup markers. At least 1 must be cleared; all 7 must be processed.
	if got.Cleared < 1 {
		t.Errorf("with keepRecent=0 floored to 1, expected Cleared >= 1; got %d", got.Cleared)
	}
	processed := 0
	for _, m := range got.Messages {
		for _, b := range m.Content {
			if b.Type == "tool_result" && isPlaceholder(b.ResultContent) {
				processed++
			}
		}
	}
	if processed != 7 {
		t.Errorf("expected 7 processed (cleared+deduped); got %d", processed)
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
			if b.IsError && isPlaceholder(b.ResultContent) {
				t.Errorf("error tool_result %s was cleared but should be protected", b.ToolUseID)
			}
		}
	}

	// Of the 8 tool_results, 5 are in keepRecent window (4,5,6,7,8).
	// Outside window: 1, 2, 3. Tool 2 is an error (protected). Tools 1 and 3
	// have identical content; dedup processes only clear candidates, so toolu_1
	// gets an informative placeholder and toolu_3 gets a dedup marker.
	// Cleared counts only explicit informative-placeholder replacements = 1.
	if got.Cleared < 1 {
		t.Errorf("Cleared = %d; want >= 1", got.Cleared)
	}
}

func TestApply_InformativePlaceholders(t *testing.T) {
	// Verify that tool-specific informative placeholders are used.
	tests := []struct {
		name     string
		toolName string
		content  string
		wantPfx  string
	}{
		{"bash tool", "Bash", strings.Repeat("output line\n", 10), "[Bash]"},
		{"read tool", "Read", strings.Repeat("file content\n", 10), "[Read]"},
		{"grep tool", "Grep", strings.Repeat("match line\n", 10), "[Grep]"},
		{"webfetch tool", "WebFetch", strings.Repeat("html content", 50), "[WebFetch]"},
		{"unknown tool", "MyTool", strings.Repeat("data\n", 20), "[MyTool]"},
		{"no tool name", "", strings.Repeat("data\n", 20), "[tool]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := "toolu_test"
			in := []api.Message{
				{Role: "assistant", Content: []api.ContentBlock{
					{Type: "tool_use", ID: id, Name: tt.toolName},
				}},
				{Role: "user", Content: []api.ContentBlock{
					{Type: "tool_result", ToolUseID: id, ResultContent: tt.content},
				}},
				// Add a dummy keep message so the test one is outside the keep window.
				{Role: "assistant", Content: []api.ContentBlock{
					{Type: "tool_use", ID: "toolu_keep1", Name: "Bash"},
				}},
				{Role: "user", Content: []api.ContentBlock{
					{Type: "tool_result", ToolUseID: "toolu_keep1", ResultContent: "keep1"},
				}},
				{Role: "assistant", Content: []api.ContentBlock{
					{Type: "tool_use", ID: "toolu_keep2", Name: "Bash"},
				}},
				{Role: "user", Content: []api.ContentBlock{
					{Type: "tool_result", ToolUseID: "toolu_keep2", ResultContent: "keep2"},
				}},
				{Role: "assistant", Content: []api.ContentBlock{
					{Type: "tool_use", ID: "toolu_keep3", Name: "Bash"},
				}},
				{Role: "user", Content: []api.ContentBlock{
					{Type: "tool_result", ToolUseID: "toolu_keep3", ResultContent: "keep3"},
				}},
				{Role: "assistant", Content: []api.ContentBlock{
					{Type: "tool_use", ID: "toolu_keep4", Name: "Bash"},
				}},
				{Role: "user", Content: []api.ContentBlock{
					{Type: "tool_result", ToolUseID: "toolu_keep4", ResultContent: "keep4"},
				}},
				{Role: "assistant", Content: []api.ContentBlock{
					{Type: "tool_use", ID: "toolu_keep5", Name: "Bash"},
				}},
				{Role: "user", Content: []api.ContentBlock{
					{Type: "tool_result", ToolUseID: "toolu_keep5", ResultContent: "keep5"},
				}},
			}
			got := Apply(in, time.Now().Add(-2*time.Hour), DefaultThreshold, DefaultKeepRecent)
			if !got.Triggered {
				t.Fatalf("expected trigger")
			}
			// Find the cleared block.
			for _, m := range got.Messages {
				for _, b := range m.Content {
					if b.ToolUseID == id {
						if !strings.HasPrefix(b.ResultContent, tt.wantPfx) {
							t.Errorf("got placeholder %q; want prefix %q", b.ResultContent, tt.wantPfx)
						}
						return
					}
				}
			}
			t.Errorf("did not find tool_result for id %s", id)
		})
	}
}

func TestApply_DedupIdenticalCandidates(t *testing.T) {
	// 4 identical tool_results outside the keep window: first one gets the
	// informative placeholder, the next 3 get dedup markers.
	const sameContent = "identical output content\n"
	in := []api.Message{}
	// 4 identical outside keep
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("toolu_%d", i)
		in = append(in, api.Message{
			Role: "assistant",
			Content: []api.ContentBlock{
				{Type: "tool_use", ID: id, Name: "Bash"},
			},
		})
		in = append(in, api.Message{
			Role: "user",
			Content: []api.ContentBlock{
				{Type: "tool_result", ToolUseID: id, ResultContent: sameContent},
			},
		})
	}
	// 5 kept (different content)
	for i := 5; i <= 9; i++ {
		id := fmt.Sprintf("toolu_%d", i)
		in = append(in, api.Message{
			Role: "assistant",
			Content: []api.ContentBlock{
				{Type: "tool_use", ID: id, Name: "Bash"},
			},
		})
		in = append(in, api.Message{
			Role: "user",
			Content: []api.ContentBlock{
				{Type: "tool_result", ToolUseID: id, ResultContent: "unique " + id},
			},
		})
	}

	got := Apply(in, time.Now().Add(-2*time.Hour), DefaultThreshold, DefaultKeepRecent)
	if !got.Triggered {
		t.Fatalf("expected trigger")
	}
	// 4 candidates: 1 unique placeholder + 3 dedup markers = 4 total placeholders
	placeholders := 0
	dupMarkers := 0
	for _, m := range got.Messages {
		for _, b := range m.Content {
			if b.Type != "tool_result" {
				continue
			}
			if strings.HasPrefix(b.ResultContent, dupPrefix) {
				dupMarkers++
				placeholders++
			} else if isPlaceholder(b.ResultContent) {
				placeholders++
			}
		}
	}
	if placeholders != 4 {
		t.Errorf("expected 4 placeholders; got %d", placeholders)
	}
	if dupMarkers != 3 {
		t.Errorf("expected 3 dedup markers; got %d", dupMarkers)
	}
}
