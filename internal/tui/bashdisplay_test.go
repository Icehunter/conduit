package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestDisplayBashCommand(t *testing.T) {
	tests := []struct {
		name, input, cwd, want string
	}{
		{"strips cd to cwd", `{"command":"cd /work/proj && go test ./..."}`, "/work/proj", "go test ./..."},
		{"strips quoted cwd", `{"command":"cd \"/work/proj\" && ls"}`, "/work/proj", "ls"},
		{"keeps cd elsewhere", `{"command":"cd /other && ls"}`, "/work/proj", "cd /other && ls"},
		{"keeps cd with variable", `{"command":"cd $HOME && ls"}`, "/work/proj", "cd $HOME && ls"},
		{"keeps cd with semicolon", `{"command":"cd /work/proj; ls"}`, "/work/proj", "cd /work/proj; ls"},
		{"bare command from permission prompt", "cd /work/proj && make", "/work/proj", "make"},
		{"trailing slash cwd", `{"command":"cd /work/proj/ && make"}`, "/work/proj", "make"},
		{"unknown cwd keeps prefix", `{"command":"cd /work/proj && make"}`, "", "cd /work/proj && make"},
		{"long command is not truncated", `{"command":"` + strings.Repeat("a", 900) + `"}`, "/w", strings.Repeat("a", 900)},
		{"multiline kept", `{"command":"echo a\necho b"}`, "/w", "echo a\necho b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := displayBashCommand(tt.input, tt.cwd); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestToolRowShowsFullWrappedBashCommand(t *testing.T) {
	setDisplayCwd("/work/proj")
	t.Cleanup(func() { setDisplayCwd("") })
	long := "go test -race -count=1 -run TestSomethingVeryLong ./internal/tui/... ./internal/agent/... ./internal/api/... && echo finished-marker"
	msg := Message{Role: RoleTool, ToolName: "Bash", ToolInput: `{"command":"cd /work/proj && ` + long + `"}`, Content: "ok", ToolDuration: 1}
	out := ansi.Strip(renderToolMessageWithPrefix(msg, 60, true, "  ╰─ ", true))
	if strings.Contains(out, "cd /work/proj") {
		t.Errorf("redundant cd prefix shown:\n%s", out)
	}
	if strings.Contains(out, "…") {
		t.Errorf("command truncated:\n%s", out)
	}
	flat := strings.Join(strings.Fields(strings.NewReplacer("╰─", "", "│", "").Replace(out)), " ")
	if !strings.Contains(flat, "finished-marker") {
		t.Errorf("end of command missing:\n%s", out)
	}
	for _, ln := range strings.Split(out, "\n") {
		if w := ansi.StringWidth(ln); w > 60 {
			t.Errorf("line wider than 60 (%d): %q", w, ln)
		}
	}
}

func TestShortBashCommandStaysInline(t *testing.T) {
	setDisplayCwd("")
	msg := Message{Role: RoleTool, ToolName: "Bash", ToolInput: `{"command":"ls -la"}`, Content: "ok", ToolDuration: 1}
	out := ansi.Strip(renderToolMessageWithPrefix(msg, 80, true, "", true))
	if first := strings.SplitN(out, "\n", 2)[0]; !strings.Contains(first, "ls -la") {
		t.Errorf("short command should be inline in header: %q", first)
	}
}

func TestPermissionPromptShowsWholeBashCommand(t *testing.T) {
	setDisplayCwd("/work/proj")
	t.Cleanup(func() { setDisplayCwd("") })
	m, _ := makePermPromptModel(t, false)
	cmd := "cd /work/proj && " + strings.Repeat("echo word && ", 30) + "rm -rf build"
	m.permPrompt.toolInput = cmd
	out := ansi.Strip(m.renderPermissionPrompt())
	flat := strings.Join(strings.Fields(out), " ")
	if !strings.Contains(flat, "rm -rf build") {
		t.Errorf("tail of command hidden:\n%s", out)
	}
	if strings.Contains(out, "cd /work/proj") {
		t.Errorf("redundant cd shown:\n%s", out)
	}
}

func TestSubagentPermissionPromptKeepsCdPrefix(t *testing.T) {
	setDisplayCwd("/work/proj")
	t.Cleanup(func() { setDisplayCwd("") })
	m, _ := makePermPromptModel(t, false)
	m.permPrompt.subAgentLabel = "worker"
	m.permPrompt.toolInput = "cd /work/proj && make"
	if out := ansi.Strip(m.renderPermissionPrompt()); !strings.Contains(out, "cd /work/proj") {
		t.Errorf("sub-agent command must be shown as sent:\n%s", out)
	}
}
