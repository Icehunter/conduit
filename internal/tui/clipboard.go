package tui

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
)

// copyToClipboard writes text to the system clipboard via OSC 52.
// This works in iTerm2, kitty, WezTerm, and most modern terminals
// without needing pbcopy or xclip — the terminal handles it.
func copyToClipboard(text string) {
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	// OSC 52 sequence: ESC ] 52 ; c ; <base64> BEL
	seq := fmt.Sprintf("\x1b]52;c;%s\x07", encoded)
	_, _ = os.Stdout.Write([]byte(seq))
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
