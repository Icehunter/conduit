package compact

import (
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/api"
)

func TestExtractSummary_Found(t *testing.T) {
	input := "<analysis>stuff</analysis>\n<summary>the real summary</summary>"
	got := extractSummary(input)
	if got != "the real summary" {
		t.Errorf("got %q", got)
	}
}

func TestExtractSummary_Missing(t *testing.T) {
	got := extractSummary("no tags here")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestExtractSummary_UnclosedTag(t *testing.T) {
	got := extractSummary("<summary>trailing text")
	if got != "trailing text" {
		t.Errorf("got %q", got)
	}
}

func TestBuildTranscript_Basic(t *testing.T) {
	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "Hello"}}},
		{Role: "assistant", Content: []api.ContentBlock{{Type: "text", Text: "Hi there"}}},
	}
	got := buildTranscript(msgs)
	if !strings.Contains(got, "User: Hello") {
		t.Errorf("missing user line; got: %s", got)
	}
	if !strings.Contains(got, "Assistant: Hi there") {
		t.Errorf("missing assistant line; got: %s", got)
	}
}

func TestBuildTranscript_ToolCall(t *testing.T) {
	msgs := []api.Message{
		{Role: "assistant", Content: []api.ContentBlock{
			{Type: "tool_use", Name: "Bash"},
		}},
		{Role: "user", Content: []api.ContentBlock{
			{Type: "tool_result", ResultContent: "output"},
		}},
	}
	got := buildTranscript(msgs)
	if !strings.Contains(got, "tool call: Bash") {
		t.Errorf("missing tool call; got: %s", got)
	}
	if !strings.Contains(got, "Tool result: output") {
		t.Errorf("missing tool result; got: %s", got)
	}
}

func TestBuildTranscript_LongToolResultTruncated(t *testing.T) {
	long := strings.Repeat("x", 1000)
	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{
			{Type: "tool_result", ResultContent: long},
		}},
	}
	got := buildTranscript(msgs)
	if !strings.Contains(got, "truncated") {
		t.Errorf("expected truncation marker; got: %s", got[:100])
	}
}

func TestExtractPreviousSummary_Found(t *testing.T) {
	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{
			Type: "text",
			Text: "<summary>\n## Current State\nWorking on X\n</summary>\n\nAbove is a summary of our conversation so far.",
		}}},
		{Role: "assistant", Content: []api.ContentBlock{{Type: "text", Text: "Continuing..."}}},
	}
	got := extractPreviousSummary(msgs)
	if !strings.Contains(got, "## Current State") {
		t.Errorf("expected summary content; got: %s", got)
	}
}

func TestExtractPreviousSummary_NotFound(t *testing.T) {
	msgs := []api.Message{
		{Role: "user", Content: []api.ContentBlock{{Type: "text", Text: "Just a normal message"}}},
	}
	got := extractPreviousSummary(msgs)
	if got != "" {
		t.Errorf("expected empty; got: %s", got)
	}
}

func TestExtractPreviousSummary_EmptyMessages(t *testing.T) {
	got := extractPreviousSummary(nil)
	if got != "" {
		t.Errorf("expected empty; got: %s", got)
	}
}

func TestExtractPreviousSummary_AssistantFirst(t *testing.T) {
	// If somehow assistant message comes first (shouldn't happen), skip it
	msgs := []api.Message{
		{Role: "assistant", Content: []api.ContentBlock{{Type: "text", Text: "<summary>assistant summary</summary>"}}},
	}
	got := extractPreviousSummary(msgs)
	if got != "" {
		t.Errorf("expected empty for assistant-first; got: %s", got)
	}
}
