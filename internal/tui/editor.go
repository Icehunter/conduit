package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// editorKey opens the current prompt in $VISUAL / $EDITOR. ctrl+g (the usual
// choice) is the agent strip toggle here.
const editorKey = "ctrl+x"

// editorDoneMsg returns the edited prompt text. err is set when the editor
// could not run or exited non-zero; the prompt is left untouched then.
type editorDoneMsg struct {
	text string
	err  error
}

// editorCommand returns the editor argv from the environment, honouring
// arguments in the variable (EDITOR="code -w"). VISUAL wins over EDITOR.
func editorCommand(getenv func(string) string) []string {
	for _, key := range []string{"VISUAL", "EDITOR"} {
		if f := strings.Fields(getenv(key)); len(f) > 0 {
			return f
		}
	}
	return []string{"vi"}
}

// normalizeEditorText drops the trailing newline editors append.
func normalizeEditorText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.TrimRight(s, "\n")
}

// editorReadyMsg carries the prepared editor process (temp file written).
type editorReadyMsg struct {
	cmd  *exec.Cmd
	file string
}

// prepareEditorCmd resolves the editor and writes the prompt to a temp file
// off the event loop. An empty prompt still opens an empty file.
func prepareEditorCmd(current string) tea.Cmd {
	return func() tea.Msg {
		argv := editorCommand(os.Getenv)
		path, err := exec.LookPath(argv[0])
		if err != nil {
			return editorDoneMsg{err: fmt.Errorf("editor %q not found; set $EDITOR", argv[0])}
		}
		f, err := os.CreateTemp("", "conduit-prompt-*.md")
		if err != nil {
			return editorDoneMsg{err: err}
		}
		name := f.Name()
		_, werr := f.WriteString(current)
		cerr := f.Close()
		if werr != nil || cerr != nil {
			_ = os.Remove(name)
			return editorDoneMsg{err: fmt.Errorf("write temp file: %w", firstErr(werr, cerr))}
		}
		return editorReadyMsg{cmd: exec.CommandContext(context.Background(), path, append(argv[1:], name)...), file: name}
	}
}

// runEditorCmd suspends the TUI while the editor runs, then reads the result
// back (the callback runs off the event loop).
func runEditorCmd(r editorReadyMsg) tea.Cmd {
	return tea.ExecProcess(r.cmd, func(runErr error) tea.Msg {
		defer func() { _ = os.Remove(r.file) }()
		if runErr != nil {
			return editorDoneMsg{err: fmt.Errorf("editor exited: %w", runErr)}
		}
		data, err := os.ReadFile(r.file)
		if err != nil {
			return editorDoneMsg{err: err}
		}
		return editorDoneMsg{text: normalizeEditorText(string(data))}
	})
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
