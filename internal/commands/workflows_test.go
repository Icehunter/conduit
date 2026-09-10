package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterWorkflowCommands(t *testing.T) {
	cwd := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	dir := filepath.Join(cwd, ".claude", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "graph-audit.js"),
		"export const meta = { name: 'graph-audit', description: 'Audit files', whenToUse: 'same check across many files' }\nreturn 1\n")
	writeFile(t, filepath.Join(dir, "broken.js"), "return 1\n")
	writeFile(t, filepath.Join(dir, "help.js"),
		"export const meta = { name: 'help', description: 'collides with a builtin' }\nreturn 1\n")

	r := New()
	r.Register(Command{Name: "help", Description: "builtin", Handler: func(string) Result { return Result{Type: "text", Text: "builtin"} }})
	RegisterWorkflowCommands(r, cwd)

	if _, ok := r.cmds["workflows"]; !ok {
		t.Fatal("/workflows not registered")
	}
	if res := r.cmds["workflows"].Handler(""); res.Type != "workflow-panel" {
		t.Errorf("/workflows result = %+v", res)
	}
	cmd, ok := r.cmds["graph-audit"]
	if !ok {
		t.Fatal("saved workflow not registered as a command")
	}
	if !strings.Contains(cmd.Description, "Audit files") || !strings.Contains(cmd.Description, "same check") {
		t.Errorf("description = %q", cmd.Description)
	}
	res := cmd.Handler("internal/tui the null bug")
	if res.Type != "prompt" || !strings.Contains(res.Text, `Workflow({name: "graph-audit", args: "internal/tui the null bug"})`) {
		t.Errorf("prompt = %+v", res)
	}
	res = cmd.Handler(`{"cap": 5}`)
	if !strings.Contains(res.Text, `args: {"cap": 5}`) {
		t.Errorf("json args prompt = %q", res.Text)
	}
	if _, ok := r.cmds["broken"]; ok {
		t.Error("unloadable workflow was registered")
	}
	if got := r.cmds["help"].Handler("").Text; got != "builtin" {
		t.Errorf("builtin /help was overridden: %q", got)
	}
}

func TestHasUltracode(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"ultracode audit this repo", true},
		{"please, ULTRACODE.", true},
		{"(ultracode) go", true},
		{"ultracoder", false},
		{"my-ultracode-thing", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := HasUltracode(tt.in); got != tt.want {
			t.Errorf("HasUltracode(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestRegisterUltracodeCommand(t *testing.T) {
	on := false
	r := New()
	RegisterUltracodeCommand(r, func() bool { return on }, func(v bool) { on = v })
	h := r.cmds["ultracode"].Handler
	if res := h(""); !on || res.Type != "flash" || !strings.Contains(res.Text, "ON") {
		t.Errorf("toggle on: on=%v res=%+v", on, res)
	}
	if res := h(""); on || !strings.Contains(res.Text, "off") {
		t.Errorf("toggle off: on=%v res=%+v", on, res)
	}
	h("on")
	if !on {
		t.Error("explicit on failed")
	}
	h("off")
	if on {
		t.Error("explicit off failed")
	}
	if res := h("maybe"); res.Type != "error" {
		t.Errorf("bad arg = %+v", res)
	}
}
