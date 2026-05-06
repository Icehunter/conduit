package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/icehunter/conduit/internal/api"
)

// plainText strips ANSI escapes so substring assertions don't fail when
// lipgloss v2 emits per-rune styling that interleaves with the text.
func plainText(s string) string { return ansi.Strip(s) }

func TestRenderMarkdown_Heading1(t *testing.T) {
	out := plainText(renderMarkdown("# Hello World", 80))
	if !strings.Contains(out, "Hello World") {
		t.Errorf("h1 text missing: %q", out)
	}
}

func TestRenderMarkdown_Heading2(t *testing.T) {
	out := plainText(renderMarkdown("## Section", 80))
	if !strings.Contains(out, "Section") {
		t.Errorf("h2 text missing: %q", out)
	}
}

func TestRenderMarkdown_Strikethrough(t *testing.T) {
	out := plainText(renderMarkdown("~~deleted text~~", 80))
	// Should contain the text (strikethrough styling is cosmetic)
	if !strings.Contains(out, "deleted text") {
		t.Errorf("strikethrough text missing: %q", out)
	}
}

func TestRenderMarkdown_Italic(t *testing.T) {
	out := plainText(renderMarkdown("*italic* and _also italic_", 80))
	if !strings.Contains(out, "italic") {
		t.Errorf("italic text missing: %q", out)
	}
}

func TestRenderMarkdown_TaskList_Unchecked(t *testing.T) {
	out := renderMarkdown("- [ ] todo item", 80)
	if !strings.Contains(out, "todo item") {
		t.Errorf("task list text missing: %q", out)
	}
	if !strings.Contains(out, "☐") {
		t.Errorf("unchecked box missing: %q", out)
	}
}

func TestRenderMarkdown_TaskList_Checked(t *testing.T) {
	out := renderMarkdown("- [x] done item", 80)
	if !strings.Contains(out, "done item") {
		t.Errorf("task list text missing: %q", out)
	}
	if !strings.Contains(out, "☑") {
		t.Errorf("checked box missing: %q", out)
	}
}

func TestRenderMarkdown_Table(t *testing.T) {
	table := "| Name | Value |\n|------|-------|\n| foo  | bar   |"
	out := renderMarkdown(table, 80)
	if !strings.Contains(out, "Name") {
		t.Errorf("table header missing: %q", out)
	}
	if !strings.Contains(out, "foo") {
		t.Errorf("table row missing: %q", out)
	}
	if !strings.Contains(out, "bar") {
		t.Errorf("table cell missing: %q", out)
	}
}

func TestRenderMarkdown_Table_Separator(t *testing.T) {
	// Separator rows (|---|---| lines) should not appear verbatim.
	table := "| A | B |\n|---|---|\n| 1 | 2 |"
	out := renderMarkdown(table, 80)
	if strings.Contains(out, "---") {
		t.Errorf("separator row should be removed: %q", out)
	}
}

func TestRenderMarkdown_BulletList(t *testing.T) {
	out := renderMarkdown("- item one\n- item two", 80)
	if !strings.Contains(out, "item one") {
		t.Errorf("bullet item missing: %q", out)
	}
}

func TestRenderMarkdown_CodeBlock_Preserved(t *testing.T) {
	out := renderMarkdown("```go\nfmt.Println(\"hi\")\n```", 80)
	if !strings.Contains(out, "Println") {
		t.Errorf("code content missing: %q", out)
	}
}

func TestRenderMarkdown_Bold(t *testing.T) {
	out := renderMarkdown("**important**", 80)
	if !strings.Contains(out, "important") {
		t.Errorf("bold text missing: %q", out)
	}
}

func TestRenderMarkdown_InlineCode(t *testing.T) {
	out := renderMarkdown("run `make build`", 80)
	if !strings.Contains(out, "make build") {
		t.Errorf("inline code missing: %q", out)
	}
}

func TestRenderMarkdown_HorizontalRule(t *testing.T) {
	out := renderMarkdown("---", 80)
	// Should render as a separator line, not literal "---"
	if strings.TrimSpace(out) == "---" {
		t.Errorf("horizontal rule should be rendered, not literal: %q", out)
	}
}

func TestRenderMarkdown_OrderedList(t *testing.T) {
	out := renderMarkdown("1. first\n2. second", 80)
	if !strings.Contains(out, "first") || !strings.Contains(out, "second") {
		t.Errorf("ordered list items missing: %q", out)
	}
}

func TestRenderMarkdown_Blockquote(t *testing.T) {
	out := renderMarkdown("> This is a quote", 80)
	if !strings.Contains(out, "This is a quote") {
		t.Errorf("blockquote text missing: %q", out)
	}
}

func TestRenderMessage_AssistantInfo(t *testing.T) {
	out := plainText(renderMessage(Message{
		Role:              RoleAssistantInfo,
		AssistantModel:    "Sonnet 4.6",
		AssistantDuration: 12 * time.Second,
		AssistantCost:     0.03,
	}, 80, false))

	for _, want := range []string{"Sonnet 4.6", "12s", "$0.03"} {
		if !strings.Contains(out, want) {
			t.Fatalf("assistant info missing %q: %q", want, out)
		}
	}
}

func TestRenderMessage_ToolSummary(t *testing.T) {
	out := plainText(renderMessage(Message{
		Role:         RoleTool,
		ToolName:     "BashTool",
		ToolInput:    `{"command":"make verify"}`,
		Content:      "All checks passed.",
		ToolDuration: 2 * time.Second,
	}, 80, false))

	for _, want := range []string{"Bash", "ran", "2s", "make verify"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tool render missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "All checks passed.") {
		t.Fatalf("completed successful tool render should hide result preview: %q", out)
	}
	if got := strings.Count(out, "\n"); got != 0 {
		t.Fatalf("completed successful tool row should stay one line, got %d newlines: %q", got, out)
	}
}

func TestRenderMessage_RunningToolSummaryStaysOneLine(t *testing.T) {
	longPrompt := "Write a complete, production-quality Go webserver that serves cached data from an S3 bucket. Address all of the identified reliability, security, cache invalidation, observability, and deployment issues without omitting edge cases."
	out := renderMessage(Message{
		Role:      RoleTool,
		ToolName:  "qwen_router__qwen_implement",
		ToolInput: `{"prompt":"` + longPrompt + `"}`,
		Content:   "running…",
	}, 72, false)

	plain := plainText(out)
	if !strings.Contains(plain, "Write a complete") {
		t.Fatalf("running summary lost prompt content: %q", plain)
	}
	if got := strings.Count(out, "\n"); got != 0 {
		t.Fatalf("running successful tool row should stay one line, got %d newlines: %q", got, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if got := ansi.StringWidth(line); got > 72 {
			t.Fatalf("line width = %d, want <= 72: %q", got, line)
		}
	}
}

func TestRenderMessage_ToolErrorShowsDetails(t *testing.T) {
	out := plainText(renderMessage(Message{
		Role:      RoleTool,
		ToolName:  "BashTool",
		ToolInput: `{"command":"make verify"}`,
		Content:   "exit status 1: lint failed",
		ToolError: true,
	}, 80, false))

	for _, want := range []string{"Bash", "failed", "exit status 1: lint failed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tool error render missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "make verify") {
		t.Fatalf("completed error tool render should hide prompt summary: %q", out)
	}
}

func TestHistoryToDisplayMessage_ToolUsePreservesInput(t *testing.T) {
	msg := historyToDisplayMessage(api.Message{
		Role: "assistant",
		Content: []api.ContentBlock{{
			Type:  "tool_use",
			ID:    "toolu_1",
			Name:  "BashTool",
			Input: map[string]any{"command": "git status --short"},
		}},
	})

	out := plainText(renderMessage(msg, 80, false))
	for _, want := range []string{"Bash", "used", "git status --short"} {
		if !strings.Contains(out, want) {
			t.Fatalf("resumed tool render missing %q: %q", want, out)
		}
	}
}

func TestHistoryToDisplayMessages_PairsToolResultWithToolUse(t *testing.T) {
	msgs := historyToDisplayMessages([]api.Message{
		{
			Role: "assistant",
			Content: []api.ContentBlock{{
				Type:  "tool_use",
				ID:    "toolu_1",
				Name:  "Bash",
				Input: map[string]any{"command": "git status --short"},
			}},
		},
		{
			Role: "user",
			Content: []api.ContentBlock{{
				Type:          "tool_result",
				ToolUseID:     "toolu_1",
				ResultContent: " M internal/tui/render.go",
			}},
		},
	})

	if len(msgs) != 1 {
		t.Fatalf("historyToDisplayMessages len = %d, want 1: %#v", len(msgs), msgs)
	}
	out := plainText(renderMessage(msgs[0], 80, false))
	for _, want := range []string{"Bash", "ran", "git status --short"} {
		if !strings.Contains(out, want) {
			t.Fatalf("paired resumed tool render missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "M internal/tui/render.go") {
		t.Fatalf("paired successful tool result should stay hidden: %q", out)
	}
}

func TestHistoryToDisplayMessages_PreservesTextAroundToolUse(t *testing.T) {
	msgs := historyToDisplayMessages([]api.Message{
		{
			Role: "assistant",
			Content: []api.ContentBlock{
				{Type: "text", Text: "I'll check status."},
				{
					Type:  "tool_use",
					ID:    "toolu_1",
					Name:  "Bash",
					Input: map[string]any{"command": "git status --short"},
				},
			},
		},
		{
			Role: "user",
			Content: []api.ContentBlock{{
				Type:          "tool_result",
				ToolUseID:     "toolu_1",
				ResultContent: "",
			}},
		},
	})

	if len(msgs) != 2 {
		t.Fatalf("historyToDisplayMessages len = %d, want 2: %#v", len(msgs), msgs)
	}
	if msgs[0].Role != RoleAssistant || !strings.Contains(msgs[0].Content, "I'll check status.") {
		t.Fatalf("assistant text not preserved: %#v", msgs[0])
	}
	out := plainText(renderMessage(msgs[1], 80, false))
	if !strings.Contains(out, "git status --short") {
		t.Fatalf("tool row missing command: %q", out)
	}
}

func TestRenderMessage_ToolResultFallbackSummary(t *testing.T) {
	out := plainText(renderMessage(Message{
		Role:     RoleTool,
		ToolName: "Bash",
		Content:  "first line\nsecond line\nthird line",
	}, 80, false))

	for _, want := range []string{"Bash", "ran", "first line +2 lines"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tool fallback summary missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "second line") {
		t.Fatalf("tool fallback summary should not expand successful output: %q", out)
	}
}
