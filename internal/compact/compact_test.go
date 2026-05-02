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
