package tui

// lastAssistantText returns the text of the most recent assistant message.
func (m Model) lastAssistantText() string {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Role == RoleAssistant {
			return m.messages[i].Content
		}
	}
	return ""
}
