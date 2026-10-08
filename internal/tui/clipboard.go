package tui

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// copyResultMsg reports the outcome of a clipboard write started by copyCmd.
type copyResultMsg struct {
	what string // flash suffix, e.g. "selection"
	err  error
}

// copyCmd copies text to the clipboard without blocking Update. The terminal
// clipboard (OSC 52) is written through the renderer via tea.SetClipboard so
// it cannot interleave with a frame; the native tool runs in its own Cmd and
// its real outcome comes back as a copyResultMsg.
func copyCmd(text, what string) tea.Cmd {
	return tea.Batch(tea.SetClipboard(text), func() tea.Msg {
		return copyResultMsg{what: what, err: copyToNativeClipboard(text)}
	})
}

// copyFlash is the status line for a finished copy. OSC 52 has no
// acknowledgement, so a native failure is reported as such rather than as a
// confirmed copy.
func copyFlash(r copyResultMsg) string {
	if r.err == nil {
		return "✓ Copied " + r.what
	}
	return fmt.Sprintf("⚠ Native clipboard failed (%v); sent %s to terminal clipboard only", r.err, r.what)
}

func copyToNativeClipboard(text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	switch runtime.GOOS {
	case "darwin":
		return pipeCommand(ctx, text, "pbcopy")
	case "windows":
		return pipeCommand(ctx, text, "clip")
	default:
		if err := pipeCommand(ctx, text, "wl-copy"); err == nil {
			return nil
		}
		return pipeCommand(ctx, text, "xclip", "-selection", "clipboard")
	}
}

func pipeCommand(ctx context.Context, text, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	_, writeErr := io.WriteString(stdin, text)
	closeErr := stdin.Close()
	waitErr := cmd.Wait()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	return waitErr
}

// extractCodeBlocks returns all raw code blocks from a message's content.
// Returns slice of (lang, code) pairs in order of appearance.
func extractCodeBlocks(content string) []codeBlock {
	var blocks []codeBlock
	lines := strings.Split(content, "\n")
	inCode := false
	var buf strings.Builder
	var lang string

	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			if inCode {
				blocks = append(blocks, codeBlock{lang: lang, code: strings.TrimRight(buf.String(), "\n")})
				buf.Reset()
				lang = ""
				inCode = false
			} else {
				lang = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "```")))
				inCode = true
			}
			continue
		}
		if inCode {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	return blocks
}

type codeBlock struct {
	lang string
	code string
}
