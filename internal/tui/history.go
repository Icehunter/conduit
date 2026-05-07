package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/settings"
)

// historyToDisplayMessages converts API history back into display messages.
// It keeps tool_use/tool_result pairs together so resumed conversations render
// the same compact tool rows as live conversations.
func historyToDisplayMessages(msgs []api.Message) []Message {
	out := make([]Message, 0, len(msgs))
	toolIndexByID := make(map[string]int)
	for _, apiMsg := range msgs {
		var text strings.Builder
		flushText := func() {
			if text.Len() == 0 {
				return
			}
			content := text.String()
			text.Reset()
			if apiMsg.Role == "user" {
				out = append(out, Message{Role: RoleUser, Content: content})
				return
			}
			content = stripCompanionMarkerGlobal(content)
			if content != "" {
				if apiMsg.ProviderKind == settings.ProviderKindMCP {
					out = append(out, Message{Role: RoleLocal, Content: content, ToolName: apiMsg.Provider})
				} else {
					out = append(out, Message{Role: RoleAssistant, Content: content, AssistantLabel: assistantLabelForProviderMetadata(apiMsg.ProviderKind, apiMsg.Provider, content)})
				}
			}
		}
		appendText := func(s string) {
			if s == "" {
				return
			}
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			text.WriteString(s)
		}

		for _, block := range apiMsg.Content {
			switch block.Type {
			case "text":
				appendText(block.Text)
			case "tool_use":
				flushText()
				input := ""
				if block.Input != nil {
					if raw, err := json.Marshal(block.Input); err == nil {
						input = string(raw)
					}
				}
				display := Message{Role: RoleTool, ToolName: block.Name, ToolID: block.ID, ToolInput: input, Content: ""}
				toolIndexByID[display.ToolID] = len(out)
				out = append(out, display)
			case "tool_result":
				flushText()
				display := Message{Role: RoleTool, ToolName: "result", ToolID: block.ToolUseID, Content: block.ResultContent, ToolError: block.IsError}
				if idx, ok := toolIndexByID[display.ToolID]; ok {
					out[idx].Content = display.Content
					out[idx].ToolError = display.ToolError
					continue
				}
				out = append(out, display)
			}
		}
		flushText()
	}
	return out
}

// historyToDisplayMessage converts an api.Message back into a display Message.
func historyToDisplayMessage(msg api.Message) Message {
	var text strings.Builder
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			if block.Text != "" {
				if text.Len() > 0 {
					text.WriteString("\n")
				}
				text.WriteString(block.Text)
			}
		case "tool_use":
			input := ""
			if block.Input != nil {
				if raw, err := json.Marshal(block.Input); err == nil {
					input = string(raw)
				}
			}
			return Message{Role: RoleTool, ToolName: block.Name, ToolID: block.ID, ToolInput: input, Content: ""}
		case "tool_result":
			return Message{Role: RoleTool, ToolName: "result", ToolID: block.ToolUseID, Content: block.ResultContent, ToolError: block.IsError}
		}
	}
	if msg.Role == "user" {
		return Message{Role: RoleUser, Content: text.String()}
	}
	// Strip companion markers ([Name: ...]) stored from previous sessions.
	content := text.String()
	content = stripCompanionMarkerGlobal(content)
	if msg.ProviderKind == settings.ProviderKindMCP {
		return Message{Role: RoleLocal, Content: content, ToolName: msg.Provider}
	}
	return Message{Role: RoleAssistant, Content: content, AssistantLabel: assistantLabelForProviderMetadata(msg.ProviderKind, msg.Provider, content)}
}

func assistantLabelForProviderMetadata(kind, provider, content string) string {
	switch kind {
	case settings.ProviderKindOpenAICompatible:
		return "‹ " + openAICompatibleAssistantName(provider)
	case settings.ProviderKindAnthropicAPI, settings.ProviderKindClaudeSubscription:
		return prefixClaude
	default:
		return assistantLabelForLegacyContent(content)
	}
}

func assistantLabelForLegacyContent(content string) string {
	lower := strings.ToLower(content)
	if strings.Contains(lower, "gemini-flash") || strings.Contains(lower, "gemini-pro") || strings.Contains(lower, " gemini ") {
		return "‹ Gemini"
	}
	return ""
}

// exportConversation writes the conversation display messages to a markdown file.
func (m *Model) exportConversation(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, msg := range m.messages {
		switch msg.Role {
		case RoleUser:
			fmt.Fprintf(f, "**You:** %s\n\n", msg.Content)
		case RoleAssistant:
			fmt.Fprintf(f, "**Claude:** %s\n\n", msg.Content)
		case RoleSystem:
			fmt.Fprintf(f, "> %s\n\n", strings.ReplaceAll(msg.Content, "\n", "\n> "))
		case RoleError:
			fmt.Fprintf(f, "> ⚠ %s\n\n", msg.Content)
		}
	}
	return nil
}

// persistNewMessages appends any messages not yet written to the session file.
func (m *Model) persistNewMessages(history []api.Message) {
	if m.cfg.Session == nil {
		return
	}
	for i := m.persistedCount; i < len(history); i++ {
		_ = m.cfg.Session.AppendMessage(history[i]) // best-effort
	}
	m.persistedCount = len(history)
}

// findNoAuthMsgIdx returns the index of the "Not logged in" welcome message,
// or -1 if it isn't present (e.g. user was already authenticated at startup).
func (m Model) findNoAuthMsgIdx() int {
	for i, msg := range m.messages {
		if msg.Role == RoleSystem && strings.HasPrefix(msg.Content, "Not logged in") {
			return i
		}
	}
	return -1
}
