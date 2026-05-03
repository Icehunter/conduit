package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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
