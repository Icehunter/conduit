package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func numberedLines(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line%d", i+1)
	}
	return strings.Join(lines, "\n")
}

func TestToolOutputCollapse(t *testing.T) {
	tests := []struct {
		name       string
		lines      int
		wantHint   string
		wantAbsent string
	}{
		{"ten lines show fully", 10, "", "lines hidden"},
		{"eleven lines show fully", 11, "", "lines hidden"},
		{"twelve lines fold two", 12, "(2 lines hidden)", "line11"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := Message{Role: RoleTool, ToolName: "Bash", Content: numberedLines(tt.lines), ToolDuration: 1}
			out := ansi.Strip(renderToolMessageWithPrefix(msg, 100, true, "", true))
			if tt.wantHint != "" && !strings.Contains(out, tt.wantHint) {
				t.Errorf("missing %q in:\n%s", tt.wantHint, out)
			}
			if strings.Contains(out, tt.wantAbsent) {
				t.Errorf("unexpected %q in:\n%s", tt.wantAbsent, out)
			}
			if tt.lines <= collapsedLines+1 && !strings.Contains(out, fmt.Sprintf("line%d", tt.lines)) {
				t.Errorf("last line missing in:\n%s", out)
			}
		})
	}
}

func TestToolOutputKeepsFirstLineIndent(t *testing.T) {
	msg := Message{Role: RoleTool, ToolName: "Read", Content: "     1\tpackage main\n     2\tfunc main() {}", ToolDuration: 1}
	out := ansi.Strip(renderToolMessageWithPrefix(msg, 100, true, "", true))
	if !strings.Contains(out, "     1") {
		t.Errorf("first-line indentation lost:\n%s", out)
	}
}

func TestTrimOutput(t *testing.T) {
	tests := []struct{ in, want string }{
		{"\n\n  indented\nnext  \n\n", "  indented\nnext"},
		{"   \n", ""},
		{"plain", "plain"},
	}
	for _, tt := range tests {
		if got := trimOutput(tt.in); got != tt.want {
			t.Errorf("trimOutput(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCanCollapseMatchesToggleHandlers(t *testing.T) {
	// Click/space toggles must agree with the renderer about which output folds.
	for n, want := range map[int]bool{10: false, 11: false, 12: true} {
		if got := canCollapse(n); got != want {
			t.Errorf("canCollapse(%d) = %v, want %v", n, got, want)
		}
	}
}
