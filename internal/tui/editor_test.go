package tui

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestEditorCommand(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want []string
	}{
		{"visual wins", map[string]string{"VISUAL": "nvim", "EDITOR": "nano"}, []string{"nvim"}},
		{"editor with args", map[string]string{"EDITOR": "code -w"}, []string{"code", "-w"}},
		{"blank visual falls to editor", map[string]string{"VISUAL": "  ", "EDITOR": "nano"}, []string{"nano"}},
		{"default vi", map[string]string{}, []string{"vi"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := editorCommand(func(k string) string { return tt.env[k] })
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNormalizeEditorText(t *testing.T) {
	tests := []struct{ in, want string }{
		{"hello\n", "hello"},
		{"a\r\nb\r\n", "a\nb"},
		{"\n", ""},
		{"", ""},
		{"  keep indent\n\n", "  keep indent"},
	}
	for _, tt := range tests {
		if got := normalizeEditorText(tt.in); got != tt.want {
			t.Errorf("normalizeEditorText(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestEditorKeyOpensEditorWhenIdle(t *testing.T) {
	t.Setenv("VISUAL", "true")
	m := idleModel()
	m.input.SetValue("draft")
	_, cmd, consumed := m.handleKeyBuiltins(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if !consumed || cmd == nil {
		t.Fatalf("ctrl+x idle: consumed=%v cmd=%v", consumed, cmd != nil)
	}

	m.running = true
	m2, _, consumed := m.handleKeyBuiltins(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl})
	if !consumed || !strings.Contains(m2.flashMsg, "running") {
		t.Fatalf("ctrl+x while running should flash, got consumed=%v flash=%q", consumed, m2.flashMsg)
	}
}

func TestEditorDoneMsg(t *testing.T) {
	m := idleModel()
	m.input.SetValue("old")
	next, _ := m.Update(editorDoneMsg{text: "new text"})
	if got := next.(Model).input.Value(); got != "new text" {
		t.Errorf("input = %q, want edited text", got)
	}
	// An empty result is a valid edit (user cleared the prompt).
	next, _ = m.Update(editorDoneMsg{text: ""})
	if got := next.(Model).input.Value(); got != "" {
		t.Errorf("input = %q, want cleared", got)
	}
	// An error leaves the draft alone and surfaces the problem.
	next, _ = m.Update(editorDoneMsg{err: errors.New("exit 1")})
	nm := next.(Model)
	if nm.input.Value() != "old" || !strings.Contains(nm.flashMsg, "exit 1") {
		t.Errorf("error path: input=%q flash=%q", nm.input.Value(), nm.flashMsg)
	}
}

func TestCtrlHomeEndScroll(t *testing.T) {
	m := idleModel()
	m, _ = m.handleWindowSize(tea.WindowSizeMsg{Width: 100, Height: 20})
	lines := make([]string, 0, 200)
	for i := range 200 {
		lines = append(lines, strings.Repeat("x", 10)+string(rune('a'+i%26)))
	}
	m.vp.SetContent(strings.Join(lines, "\n"))
	m.vp.GotoBottom()
	m2, _, consumed := m.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyHome, Mod: tea.ModCtrl})
	if !consumed || m2.vp.YOffset() != 0 {
		t.Fatalf("ctrl+home: consumed=%v offset=%d", consumed, m2.vp.YOffset())
	}
	m3, _, consumed := m2.handleKeyBuiltins(tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
	if !consumed || !m3.vp.AtBottom() {
		t.Fatalf("ctrl+end: consumed=%v atBottom=%v", consumed, m3.vp.AtBottom())
	}
}

func TestPrepareEditorWritesDraftAndBuildsCommand(t *testing.T) {
	t.Setenv("VISUAL", "true")
	msg := prepareEditorCmd("my draft")()
	ready, ok := msg.(editorReadyMsg)
	if !ok {
		t.Fatalf("got %T: %+v", msg, msg)
	}
	defer func() { _ = os.Remove(ready.file) }()
	data, err := os.ReadFile(ready.file)
	if err != nil || string(data) != "my draft" {
		t.Fatalf("temp file = %q, %v", data, err)
	}
	if got := ready.cmd.Args[len(ready.cmd.Args)-1]; got != ready.file {
		t.Errorf("editor not pointed at temp file: %v", ready.cmd.Args)
	}
	m := idleModel()
	if _, cmd := m.Update(ready); cmd == nil {
		t.Error("ready message must launch the editor")
	}
}

func TestPrepareEditorMissingBinary(t *testing.T) {
	t.Setenv("VISUAL", "definitely-not-an-editor-xyz")
	done, ok := prepareEditorCmd("x")().(editorDoneMsg)
	if !ok || done.err == nil {
		t.Fatalf("want error done msg, got %+v", done)
	}
}
