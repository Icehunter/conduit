package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

const (
	prefixYou    = "› You"
	prefixClaude = "‹ Claude"
	prefixLocal  = "‹ Local"

	// outerPad is spaces on each side of all viewport content.
	outerPad = 0
)

// renderMessage renders one message for display.
// width is the full viewport width.
func renderMessage(msg Message, width int, verbose bool) string {
	if width < 20 {
		width = 80
	}
	inner := width - outerPad*2
	inner = max(inner, 10)
	pad := surfaceSpaces(outerPad)

	switch msg.Role {
	case RoleUser:
		// Wrap user text at inner width minus the prefix width ("❯ You  " = 8 cols).
		prefixStr := styleYouPrefix.Render(prefixYou) + surfaceSpaces(2)
		prefixW := lipgloss.Width(prefixStr)
		body := styleUserText.Width(inner - prefixW).Render(msg.Content)
		return pad + prefixStr + body

	case RoleAssistant:
		content := stripCompanionMarkerGlobal(msg.Content)
		if content == "" {
			return "" // pure companion quip — bubble handles display, skip chat row
		}
		prefix := msg.AssistantLabel
		if prefix == "" {
			prefix = prefixClaude
		}
		body := renderMarkdown(content, inner)
		return pad + styleClaudePrefix.Render(prefix) + "\n" + indentLines(body, pad)

	case RoleLocal:
		label := prefixLocal
		if msg.ToolName != "" {
			label += " " + msg.ToolName
		}
		body := renderMarkdown(formatLocalOutput(msg.Content), inner)
		return pad + styleToolBadge.Render(label) + "\n" + indentLines(body, pad)

	case RoleAssistantInfo:
		return pad + renderAssistantInfo(msg, inner)

	case RoleTool:
		return pad + renderToolMessage(msg, inner, verbose)

	case RoleError:
		// Wrap long error text — OAuth/API errors regularly run hundreds
		// of characters with URL chains. lipgloss.Width handles word
		// wrapping. The "✗ " marker sits on the first line; continuation
		// lines indent under the body so the marker stands out.
		const errPrefix = "✗ "
		prefixW := lipgloss.Width(errPrefix)
		body := styleErrorText.Width(inner - prefixW).Render(msg.Content)
		// hangIndent: prefix on the first line, blanks on the rest.
		lines := strings.Split(body, "\n")
		var sb strings.Builder
		for i, ln := range lines {
			sb.WriteString(pad)
			if i == 0 {
				sb.WriteString(styleErrorText.Render(errPrefix))
			} else {
				sb.WriteString(surfaceSpaces(prefixW))
			}
			sb.WriteString(ln)
			if i < len(lines)-1 {
				sb.WriteByte('\n')
			}
		}
		return sb.String()

	case RoleCouncil:
		label := msg.ToolName
		if label == "" {
			label = "Council"
		}
		if strings.HasPrefix(msg.Content, "⚠ ") {
			// Ejection/warning: muted inline.
			return pad + surfaceSpaces(2) + stylePickerDesc.Render(label+": ") + stylePickerDesc.Render(msg.Content)
		}
		// Multi-line responses (member plans, synthesis) get a header + markdown body.
		// Single-line status messages ("Synthesising…", "✓ agrees…") stay inline.
		if strings.Contains(msg.Content, "\n") || strings.HasPrefix(msg.Content, "```") {
			header := styleStatusAccent.Render(label + ":")
			body := renderMarkdown(msg.Content, inner)
			return pad + surfaceSpaces(2) + header + "\n" + indentLines(body, pad+surfaceSpaces(2))
		}
		return pad + surfaceSpaces(2) + styleStatusAccent.Render(label+":") + " " + stylePickerDesc.Render(msg.Content)

	case RoleSystem:
		if msg.WelcomeCard {
			return renderWelcomeCard(msg.Content, width)
		}
		// If the content contains markdown (fenced block, heading), render it
		// as markdown so code blocks, diff highlighting, etc. work.
		trimmed := strings.TrimSpace(msg.Content)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "#") {
			body := renderMarkdown(msg.Content, inner)
			return pad + styleSystemText.Render("· ") + "\n" + indentLines(body, pad)
		}
		// Wrap to terminal width — /session, /status, /usage etc. emit long
		// lines (file paths, session IDs, rate-limit URLs) that otherwise
		// blow past the right edge and force horizontal scroll. The "· "
		// prefix sits on the first line; continuation rows indent under
		// the body so the prefix marks the message boundary.
		const sysPrefix = "· "
		prefixW := lipgloss.Width(sysPrefix)
		body := styleSystemText.Width(inner - prefixW).Render(msg.Content)
		lines := strings.Split(body, "\n")
		var sb strings.Builder
		for i, ln := range lines {
			sb.WriteString(pad)
			if i == 0 {
				sb.WriteString(styleSystemText.Render(sysPrefix))
			} else {
				sb.WriteString(surfaceSpaces(prefixW))
			}
			sb.WriteString(ln)
			if i < len(lines)-1 {
				sb.WriteByte('\n')
			}
		}
		return sb.String()
	}
	return msg.Content
}

func renderAssistantInfo(msg Message, width int) string {
	parts := []string{}
	if msg.AssistantModel != "" {
		parts = append(parts, styleStatusAccent.Render("◇ "+msg.AssistantModel))
	}
	if msg.AssistantDuration > 0 {
		parts = append(parts, styleStatus.Render(formatMessageDuration(msg.AssistantDuration)))
	}
	if msg.AssistantCost > 0 {
		parts = append(parts, styleStatus.Render(fmt.Sprintf("$%.2f", msg.AssistantCost)))
	}
	if len(parts) == 0 {
		return ""
	}
	line := strings.Join(parts, styleStatus.Render(" · "))
	return styleStatus.Width(width).Render(surfaceSpaces(2) + line)
}

func formatLocalOutput(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.Contains(trimmed, "```") {
		return text
	}
	lang := localOutputLang(trimmed)
	if lang == "" {
		return text
	}
	return "```" + lang + "\n" + trimmed + "\n```"
}

func localOutputLang(text string) string {
	first := firstNonEmptyLine(text)
	switch {
	case strings.HasPrefix(first, "diff --git ") ||
		strings.HasPrefix(first, "--- ") ||
		strings.HasPrefix(first, "+++ ") ||
		strings.HasPrefix(first, "@@ "):
		return "diff"
	case strings.HasPrefix(first, "package "):
		return "go"
	case strings.HasPrefix(first, "func ") && strings.Contains(text, "{"):
		return "go"
	case strings.HasPrefix(first, "import ") && strings.Contains(text, "func "):
		return "go"
	}
	return ""
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func renderToolMessage(msg Message, width int, verbose bool) string {
	return renderToolMessageWithPrefix(msg, width, verbose, "", true)
}

func renderToolMessageWithPrefix(msg Message, width int, verbose bool, treePrefix string, isLast bool) string {
	// Constants for collapsible tool output (inspired by crush)
	const (
		collapsedLines     = 10
		truncationFormat   = "… (%d lines hidden) [click to expand]"
		expandedHintFormat = "[click to collapse]"
	)

	statusIcon := styleStatusAccent.Render("✓")
	statusText := toolDoneVerb(msg.ToolName)
	archived := msg.Content == "" && msg.ToolDuration == 0 && !msg.ToolError
	running := msg.Content == "running…"

	if running {
		statusIcon = styleModeYellow.Render("●")
		statusText = "running"
	} else if msg.ToolError {
		statusIcon = styleErrorText.Render("✗")
		statusText = "failed"
	} else if archived {
		statusIcon = styleStatus.Render("◦")
		statusText = "used"
	}

	result := strings.TrimSpace(msg.Content)
	resultLines := strings.Split(result, "\n")
	canExpand := len(resultLines) > collapsedLines && !running && !archived && result != ""

	headerParts := []string{
		statusIcon,
		styleToolBadge.Render(toolDisplayName(msg.ToolName)),
		styleStatus.Render(statusText),
	}
	if msg.ToolDuration > 0 {
		headerParts = append(headerParts, styleStatus.Render(formatMessageDuration(msg.ToolDuration)))
	}
	header := strings.Join(headerParts, surfaceSpaces(1))

	summary := toolInputSummary(msg.ToolName, msg.ToolInput)
	if summary == "" && !msg.ToolError && !running {
		summary = toolResultSummary(msg.ToolName, msg.Content)
	}
	if !msg.ToolError && summary != "" {
		available := width - lipgloss.Width(header) - lipgloss.Width(" · ") - 4 // -4 for tree prefix
		if available >= 8 {
			header += styleStatus.Render(" · " + truncatePlainToWidth(summary, available))
		}
	}

	if running {
		result = ""
	}

	bodyWidth := max(10, width-6) // Adjusted for tree indentation

	// Render header
	var lines []string
	lines = append(lines, header)

	// Determine continuation character for multi-line output
	// When in a tree (treePrefix != ""), body lines don't need their own prefix
	// because the tree rendering will add it. When standalone, use 5 spaces.
	continuationPrefix := ""
	if treePrefix == "" {
		continuationPrefix = "     " // 5 spaces for standalone tools
	}
	// Note: For tree rendering, body lines have NO prefix here;
	// the prefix gets added in the final tree assembly below.

	// Add body content if we have output (indented with continuation)
	if verbose && !msg.ToolError && !running && !archived && result != "" {
		var body string

		// Apply collapsing if not expanded and content is long
		if canExpand && !msg.ToolExpanded {
			displayLines := resultLines[:collapsedLines]
			truncatedContent := strings.Join(displayLines, "\n")

			if strings.HasPrefix(result, "```") {
				body = renderMarkdown(truncatedContent, bodyWidth)
			} else {
				body = styleStatus.Width(bodyWidth).Render(truncatedContent)
			}

			// Add truncation hint
			hiddenCount := len(resultLines) - collapsedLines
			hint := styleStatusFaded.Render(fmt.Sprintf(truncationFormat, hiddenCount))
			body += "\n" + hint
		} else {
			// Full content
			if strings.HasPrefix(result, "```") {
				body = renderMarkdown(result, bodyWidth)
			} else {
				body = styleStatus.Width(bodyWidth).Render(result)
			}

			// Add collapse hint for expanded content
			if canExpand && msg.ToolExpanded {
				hint := styleStatusFaded.Render(expandedHintFormat)
				body += "\n" + hint
			}
		}
		// Indent body lines with continuation character
		bodyLines := strings.Split(body, "\n")
		for _, ln := range bodyLines {
			lines = append(lines, continuationPrefix+ln)
		}
	}

	// Add error output if we have an error (indented with continuation)
	if msg.ToolError && result != "" {
		errorBody := styleErrorText.Width(bodyWidth).Render(result)
		errorLines := strings.Split(errorBody, "\n")
		for _, ln := range errorLines {
			lines = append(lines, continuationPrefix+ln)
		}
	}

	rendered := strings.Join(lines, "\n")

	// Apply tree prefix if provided
	if treePrefix != "" {
		renderedLines := strings.Split(rendered, "\n")
		if len(renderedLines) > 0 {
			// First line gets the tree prefix (e.g., "  ├─ ")
			renderedLines[0] = treePrefix + renderedLines[0]

			// Continuation lines: replace tree connector with vertical or spaces
			var contPrefix string
			if isLast {
				contPrefix = "     " // 5 spaces (align with content after "  ╰─ ")
			} else {
				contPrefix = "  │  " // 2 spaces, vertical bar, 2 spaces
			}

			for i := 1; i < len(renderedLines); i++ {
				// Lines already have continuation prefix from rendering above,
				// so just add the positional prefix
				renderedLines[i] = contPrefix + renderedLines[i]
			}
			return strings.Join(renderedLines, "\n")
		}
		return treePrefix + rendered
	}
	return surfaceSpaces(2) + rendered
}

func toolDisplayName(name string) string {
	if name == "" {
		return "Tool"
	}
	name = strings.TrimSuffix(name, "Tool")
	if strings.HasPrefix(name, "mcp__") {
		parts := strings.Split(strings.TrimPrefix(name, "mcp__"), "__")
		if len(parts) >= 2 {
			return parts[0] + "/" + parts[len(parts)-1]
		}
	}
	if strings.Contains(name, "__") {
		parts := strings.Split(name, "__")
		return parts[len(parts)-1]
	}
	return name
}

func toolDoneVerb(toolName string) string {
	lower := strings.ToLower(toolName)
	switch {
	case strings.Contains(lower, "bash"), strings.Contains(lower, "shell"), strings.Contains(lower, "repl"):
		return "ran"
	case strings.Contains(lower, "grep"), strings.Contains(lower, "glob"), strings.Contains(lower, "search"):
		return "searched"
	case strings.Contains(lower, "read"), strings.Contains(lower, "fetch"):
		return "read"
	case strings.Contains(lower, "edit"), strings.Contains(lower, "write"), strings.Contains(lower, "notebook"):
		return "updated"
	case strings.Contains(lower, "todo"):
		return "updated"
	case strings.Contains(lower, "task"), strings.Contains(lower, "agent"):
		return "finished"
	}
	return "done"
}

func toolInputSummary(toolName, raw string) string {
	fields := parseToolInput(raw)
	if len(fields) == 0 {
		return ""
	}
	lower := strings.ToLower(toolName)
	switch {
	case strings.Contains(lower, "bash"):
		return firstToolField(fields, "command", "cmd")
	case strings.Contains(lower, "grep"):
		pattern := firstToolField(fields, "pattern", "query")
		include := firstToolField(fields, "include", "path")
		if pattern != "" && include != "" {
			return pattern + " in " + include
		}
		return pattern
	case strings.Contains(lower, "glob"):
		pattern := firstToolField(fields, "pattern")
		path := firstToolField(fields, "path")
		if pattern != "" && path != "" {
			return pattern + " under " + path
		}
		return pattern
	case strings.Contains(lower, "edit"), strings.Contains(lower, "write"), strings.Contains(lower, "read"), strings.Contains(lower, "notebook"):
		return firstToolField(fields, "file_path", "path")
	case strings.Contains(lower, "fetch"), strings.Contains(lower, "search"):
		return firstToolField(fields, "url", "query")
	case lower == "taskcreate":
		return firstToolField(fields, "subject", "description")
	case lower == "taskupdate":
		subj := firstToolField(fields, "subject")
		status := firstToolField(fields, "status")
		if subj != "" && status != "" {
			return subj + " → " + status
		}
		return subj + status
	case lower == "taskget", lower == "tasklist", lower == "taskoutput", lower == "taskstop":
		return firstToolField(fields, "subject", "id")
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		if fields[k] == "" {
			continue
		}
		parts = append(parts, k+"="+fields[k])
		if len(parts) == 2 {
			break
		}
	}
	return strings.Join(parts, " ")
}

func toolResultSummary(toolName, content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return "no output"
	}
	lower := strings.ToLower(toolName)
	if strings.Contains(lower, "bash") || strings.Contains(lower, "shell") || strings.Contains(lower, "repl") {
		lines := strings.Split(content, "\n")
		nonEmpty := 0
		first := ""
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			nonEmpty++
			if first == "" {
				first = line
			}
		}
		if first == "" {
			return "no output"
		}
		if nonEmpty == 1 {
			return first
		}
		return fmt.Sprintf("%s +%d lines", first, nonEmpty-1)
	}
	return ""
}

func parseToolInput(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		switch t := v.(type) {
		case string:
			out[k] = truncate(t, 500)
		case float64, bool:
			out[k] = fmt.Sprint(t)
		case []any:
			out[k] = fmt.Sprintf("%d item(s)", len(t))
		case map[string]any:
			out[k] = "object"
		}
	}
	return out
}

func firstToolField(fields map[string]string, keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(fields[key]); v != "" {
			return v
		}
	}
	return ""
}

func formatMessageDuration(d time.Duration) string {
	if d < time.Second {
		return "<1s"
	}
	if d < time.Minute {
		return d.Round(time.Second).String()
	}
	if d < time.Hour {
		min := int(d / time.Minute)
		sec := int((d % time.Minute) / time.Second)
		if sec == 0 {
			return fmt.Sprintf("%dm", min)
		}
		return fmt.Sprintf("%dm%02ds", min, sec)
	}
	return d.Round(time.Minute).String()
}
