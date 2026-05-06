package tui

import (
	"fmt"
	"strings"
)

// CostSummary returns a human-readable cost/token summary for the /cost command.
func (m *Model) CostSummary() string {
	if m.totalInputTokens == 0 && m.costUSD == 0 {
		return "No API calls made yet in this session."
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Input tokens:  %d\n", m.totalInputTokens)
	fmt.Fprintf(&sb, "Output tokens: %d\n", m.totalOutputTokens)
	if m.costUSD > 0 {
		fmt.Fprintf(&sb, "Estimated cost: $%.4f", m.costUSD)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// TasksSummary returns the list of active tasks for /tasks.
// Tasks are tracked by the TaskTool — for now we surface the tool messages.
func (m *Model) TasksSummary() string {
	var tasks []string
	for _, msg := range m.messages {
		if msg.Role == RoleTool && strings.HasPrefix(msg.ToolName, "Task") {
			tasks = append(tasks, fmt.Sprintf("  [%s] %s", msg.ToolName, msg.Content))
		}
	}
	if len(tasks) == 0 {
		return "No active tasks."
	}
	return "Active tasks:\n" + strings.Join(tasks, "\n")
}

// LastThinking returns the last thinking block text from the assistant.
// TurnCosts returns a copy of the per-turn cost deltas recorded this session.
func (m *Model) TurnCosts() []float64 {
	out := make([]float64, len(m.turnCosts))
	copy(out, m.turnCosts)
	return out
}

func (m *Model) LastThinking() string {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Role == RoleAssistant && strings.Contains(m.messages[i].Content, "<thinking>") {
			return m.messages[i].Content
		}
	}
	return ""
}

// CopyLastResponse copies the last assistant text to clipboard.
// Returns "" on success, error message otherwise.
func (m *Model) CopyLastResponse() string {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Role == RoleAssistant {
			copyToClipboard(m.messages[i].Content)
			return ""
		}
	}
	return "No assistant response to copy."
}
