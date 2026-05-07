package tui

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// copyToClipboard writes text to the system clipboard. OSC 52 is still emitted
// for remote/terminal-native copy support, but native clipboard commands are
// attempted first because some terminals disable OSC 52 writes by default.
func copyToClipboard(text string) {
	_ = copyToNativeClipboard(text)
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	// OSC 52 sequence: ESC ] 52 ; c ; <base64> BEL
	seq := fmt.Sprintf("\x1b]52;c;%s\x07", encoded)
	_, _ = os.Stdout.Write([]byte(seq))
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
