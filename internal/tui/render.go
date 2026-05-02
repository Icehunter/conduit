package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderMessage renders a single message entry for display.
func renderMessage(msg Message, width int) string {
	if width < 10 {
		width = 80
	}
	switch msg.Role {
	case RoleUser:
		prefix := styleUserPrefix.Render("▶ You")
		body := styleUser.Width(width - 2).Render(msg.Content)
		return prefix + "\n" + body
	case RoleAssistant:
		prefix := styleAssistantPrefix.Render("◀ Claude")
		body := styleAssistant.Width(width - 2).Render(renderMarkdown(msg.Content, width-2))
		return prefix + "\n" + body
	case RoleTool:
		label := styleToolLabel.Render("[" + msg.ToolName + "]")
		content := msg.Content
		if len(content) > 500 {
			content = content[:500] + "\n… (truncated)"
		}
		return label + " " + styleAssistantPrefix.Render(content)
	case RoleError:
		return styleError.Render("✗ " + msg.Content)
	case RoleSystem:
		return styleStatusLine.Render("• " + msg.Content)
	}
	return msg.Content
}

// renderMarkdown does very lightweight markdown rendering for terminal output.
// Full syntax highlighting (M3+): code fences, bold, italic.
func renderMarkdown(text string, width int) string {
	if width < 10 {
		width = 80
	}
	lines := strings.Split(text, "\n")
	var out strings.Builder
	inCode := false
	var codeBlock strings.Builder
	codeLang := ""

	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			if inCode {
				// Close code block
				rendered := styleCodeBlock.Width(width).Render(codeBlock.String())
				out.WriteString(rendered)
				out.WriteByte('\n')
				codeBlock.Reset()
				codeLang = ""
				inCode = false
			} else {
				// Open code block
				codeLang = strings.TrimPrefix(line, "```")
				_ = codeLang
				inCode = true
			}
			continue
		}
		if inCode {
			codeBlock.WriteString(line)
			codeBlock.WriteByte('\n')
			continue
		}

		// Bold: **text**
		line = renderBold(line)
		// Inline code: `text`
		line = renderInlineCode(line)

		out.WriteString(line)
		out.WriteByte('\n')
	}

	// Flush unclosed code block
	if inCode && codeBlock.Len() > 0 {
		rendered := styleCodeBlock.Width(width).Render(codeBlock.String())
		out.WriteString(rendered)
		out.WriteByte('\n')
	}

	return strings.TrimRight(out.String(), "\n")
}

// renderBold processes **text** markers.
func renderBold(line string) string {
	boldStyle := lipgloss.NewStyle().Bold(true)
	return processDelimited(line, "**", boldStyle)
}

// renderInlineCode processes `text` markers.
func renderInlineCode(line string) string {
	codeStyle := lipgloss.NewStyle().Foreground(colorCode)
	return processDelimited(line, "`", codeStyle)
}

// processDelimited finds delimiter-wrapped spans and applies style.
func processDelimited(line, delim string, style lipgloss.Style) string {
	var out strings.Builder
	for {
		start := strings.Index(line, delim)
		if start < 0 {
			out.WriteString(line)
			break
		}
		end := strings.Index(line[start+len(delim):], delim)
		if end < 0 {
			out.WriteString(line)
			break
		}
		end += start + len(delim)
		out.WriteString(line[:start])
		inner := line[start+len(delim) : end]
		out.WriteString(style.Render(inner))
		line = line[end+len(delim):]
	}
	return out.String()
}
