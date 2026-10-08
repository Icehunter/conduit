// Package transcript renders api messages as a plain-text conversation for
// consumers that need to read history as prose: memory extraction, background
// review digests and the compaction summarizer's fallback paths.
package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/icehunter/conduit/internal/api"
)

// Default per-block caps applied when the matching Options field is not
// positive.
const (
	DefaultToolInputChars  = 300
	DefaultToolResultChars = 500
)

// Options bounds a Render call. Zero values mean "no limit" for LastN and
// MaxChars and the Default* caps for the tool fields. All limits count runes.
type Options struct {
	// LastN renders only the last N messages when > 0.
	LastN int
	// MaxChars caps the whole output when > 0. The oldest messages are
	// dropped first; the newest message is truncated if it alone exceeds it.
	MaxChars int
	// ToolInputChars caps each tool call's JSON arguments.
	ToolInputChars int
	// ToolResultChars caps each tool result's text.
	ToolResultChars int
}

// Render formats msgs as "---\nROLE:\n<blocks>" sections. Tool calls carry
// their compact JSON arguments, failed tool results are marked "(error)",
// images render as "[image]", and thinking blocks are skipped. When messages
// were dropped (LastN or MaxChars) the output starts with an
// "[… N earlier messages omitted …]" marker.
func Render(msgs []api.Message, o Options) string {
	if o.ToolInputChars <= 0 {
		o.ToolInputChars = DefaultToolInputChars
	}
	if o.ToolResultChars <= 0 {
		o.ToolResultChars = DefaultToolResultChars
	}
	omitted := 0
	if o.LastN > 0 && len(msgs) > o.LastN {
		omitted = len(msgs) - o.LastN
		msgs = msgs[omitted:]
	}
	chunks := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if c := renderMessage(m, o); c != "" {
			chunks = append(chunks, c)
		}
	}
	if o.MaxChars > 0 {
		start := len(chunks)
		total := 0
		for start > 0 {
			n := utf8.RuneCountInString(chunks[start-1])
			if total+n > o.MaxChars && start < len(chunks) {
				break
			}
			total += n
			start--
		}
		omitted += start
		chunks = chunks[start:]
		if len(chunks) == 1 {
			chunks[0] = Truncate(chunks[0], o.MaxChars)
		}
	}
	var sb strings.Builder
	if omitted > 0 {
		fmt.Fprintf(&sb, "[… %d earlier messages omitted …]\n", omitted)
	}
	for _, c := range chunks {
		sb.WriteString(c)
	}
	return sb.String()
}

func renderMessage(m api.Message, o Options) string {
	var body strings.Builder
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				body.WriteString(b.Text)
				body.WriteString("\n")
			}
		case "tool_use":
			body.WriteString("[tool_use " + b.Name)
			if args := compactJSON(b.Input); args != "" {
				body.WriteString(" " + Truncate(args, o.ToolInputChars))
			}
			body.WriteString("]\n")
		case "tool_result":
			if b.IsError {
				body.WriteString("[tool_result (error)]\n")
			} else {
				body.WriteString("[tool_result]\n")
			}
			body.WriteString(Truncate(ToolResultText(b), o.ToolResultChars))
			body.WriteString("\n")
		case "image":
			body.WriteString("[image]\n")
		case "document":
			body.WriteString("[document]\n")
		}
	}
	if body.Len() == 0 {
		return ""
	}
	return "---\n" + strings.ToUpper(m.Role) + ":\n" + body.String()
}

func compactJSON(in map[string]any) string {
	if len(in) == 0 {
		return ""
	}
	b, err := json.Marshal(in)
	if err != nil {
		return ""
	}
	return string(b)
}

// ToolResultText returns the text of a tool_result block. ResultBlocks take
// precedence over ResultContent (as on the wire); text blocks are joined with
// newlines and images appear as "[image]".
func ToolResultText(b api.ContentBlock) string {
	if len(b.ResultBlocks) == 0 {
		return b.ResultContent
	}
	parts := make([]string, 0, len(b.ResultBlocks))
	for _, rb := range b.ResultBlocks {
		switch rb.Type {
		case "text":
			if rb.Text != "" {
				parts = append(parts, rb.Text)
			}
		case "image":
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n")
}

// Truncate shortens s to at most n runes, appending "…" when it cut anything.
// It never splits a multi-byte rune. n <= 0 returns s unchanged.
func Truncate(s string, n int) string {
	if n <= 0 || len(s) <= n { // len(s) >= rune count, so this is a safe fast path
		return s
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for r := 0; r < n; r++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i] + "…"
}
