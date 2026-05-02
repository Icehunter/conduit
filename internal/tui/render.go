package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	prefixYou    = "▶ You"
	prefixClaude = "◀ Claude"
)

// renderMessage renders one message for display in the viewport.
// width is the usable column count (viewport width).
func renderMessage(msg Message, width int) string {
	if width < 20 {
		width = 80
	}
	switch msg.Role {
	case RoleUser:
		prefix := styleYouPrefix.Render(prefixYou)
		body := styleUserText.Render(msg.Content)
		return prefix + "  " + body

	case RoleAssistant:
		prefix := styleClaudePrefix.Render(prefixClaude)
		body := renderMarkdown(msg.Content, width)
		return prefix + "\n" + body

	case RoleTool:
		badge := styleToolBadge.Render("⚙ " + msg.ToolName)
		content := styleToolContent.Render(msg.Content)
		return "  " + badge + "  " + content

	case RoleError:
		return styleErrorText.Render("✗ " + msg.Content)

	case RoleSystem:
		return styleSystemText.Render("· " + msg.Content)
	}
	return msg.Content
}

// renderMarkdown does lightweight markdown for terminal output.
func renderMarkdown(text string, width int) string {
	lines := strings.Split(text, "\n")
	var out strings.Builder
	inCode := false
	var codeBuf strings.Builder

	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			if inCode {
				// Render accumulated code block
				code := strings.TrimRight(codeBuf.String(), "\n")
				rendered := styleCodeBlock.Width(width).Render(code)
				out.WriteString(rendered)
				out.WriteByte('\n')
				codeBuf.Reset()
				inCode = false
			} else {
				inCode = true
			}
			continue
		}
		if inCode {
			codeBuf.WriteString(line)
			codeBuf.WriteByte('\n')
			continue
		}
		out.WriteString(renderLine(line))
		out.WriteByte('\n')
	}
	// Flush unclosed code block
	if inCode && codeBuf.Len() > 0 {
		code := strings.TrimRight(codeBuf.String(), "\n")
		out.WriteString(styleCodeBlock.Width(width).Render(code))
		out.WriteByte('\n')
	}
	return strings.TrimRight(out.String(), "\n")
}

// renderLine applies inline styling (bold, inline code) to a single line.
func renderLine(line string) string {
	line = applyDelim(line, "**", lipgloss.NewStyle().Bold(true))
	line = applyDelim(line, "`", styleInlineCode)
	return styleAssistantText.Render(line)
}

// applyDelim finds delimiter-wrapped spans and applies style.
func applyDelim(line, delim string, style lipgloss.Style) string {
	var out strings.Builder
	for {
		i := strings.Index(line, delim)
		if i < 0 {
			out.WriteString(line)
			break
		}
		j := strings.Index(line[i+len(delim):], delim)
		if j < 0 {
			out.WriteString(line)
			break
		}
		j += i + len(delim)
		out.WriteString(line[:i])
		out.WriteString(style.Render(line[i+len(delim) : j]))
		line = line[j+len(delim):]
	}
	return out.String()
}

// separator returns a dim horizontal rule.
func separator(width int) string {
	if width < 1 {
		width = 1
	}
	return styleSep.Render(strings.Repeat("─", width))
}
