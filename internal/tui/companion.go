package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/buddy"
)

// maybeFireCompanionBubble checks if the last assistant message contains a
// companion quip marker of the form [Name: ...]. When found it:
//   - Strips the marker from the assistant message (leaving any remaining text)
//   - If the message is now empty, removes it from m.messages entirely
//   - Returns a cmd that fires companionBubbleMsg to show the bubble
//
// The system prompt instructs Claude to wrap companion speech as [Name: text].
func (m Model) maybeFireCompanionBubble() (Model, tea.Cmd) {
	sc, err := buddy.Load()
	if err != nil || sc == nil || sc.Name == "" {
		return m, nil
	}
	prefix := "[" + sc.Name + ": "
	closingBracket := "]"

	// Require the last user message to mention the companion name as a word.
	// This prevents Claude from spontaneously including the marker when the
	// user wasn't addressing the companion.
	companionLower := strings.ToLower(sc.Name)
	userAddressed := false
	for i := len(m.messages) - 1; i >= 0; i-- {
		msg := m.messages[i]
		if msg.Role == RoleUser {
			lower := strings.ToLower(msg.Content)
			// Word-boundary check: name must be preceded/followed by non-letter.
			idx := strings.Index(lower, companionLower)
			for idx >= 0 {
				before := idx == 0 || !isLetter(rune(lower[idx-1]))
				after := idx+len(companionLower) >= len(lower) || !isLetter(rune(lower[idx+len(companionLower)]))
				if before && after {
					userAddressed = true
					break
				}
				next := strings.Index(lower[idx+1:], companionLower)
				if next < 0 {
					break
				}
				idx = idx + 1 + next
			}
			break
		}
		if msg.Role == RoleAssistant {
			break
		}
	}
	if !userAddressed {
		return m, nil
	}

	for i := len(m.messages) - 1; i >= 0; i-- {
		msg := m.messages[i]
		if msg.Role != RoleAssistant {
			continue
		}
		text := msg.Content
		// Look for [Name: ...] anywhere in the response.
		start := strings.Index(text, prefix)
		if start < 0 {
			return m, nil // no companion marker — leave as-is
		}
		end := strings.Index(text[start:], closingBracket)
		if end < 0 {
			return m, nil // malformed marker
		}
		end += start // absolute index
		quip := strings.TrimSpace(text[start+len(prefix) : end])
		if quip == "" {
			return m, nil
		}
		// Strip the marker from the assistant message.
		cleaned := strings.TrimSpace(text[:start] + text[end+1:])
		if cleaned == "" {
			m.messages = append(m.messages[:i], m.messages[i+1:]...)
		} else {
			m.messages[i].Content = cleaned
		}
		return m, func() tea.Msg { return companionBubbleMsg{text: quip} }
	}
	return m, nil
}

// companionBubbleMsg is sent when the companion should speak.
type companionBubbleMsg struct{ text string }

// stripCompanionMarkerGlobal strips any [Name: text] companion tag from
// a stored message (e.g., when loading history). Uses the live companion
// name from buddy.Load(); falls back to a simple bracket-scan.
func stripCompanionMarkerGlobal(s string) string {
	sc, err := buddy.Load()
	if err != nil || sc == nil || sc.Name == "" {
		return s
	}
	prefix := "[" + sc.Name + ": "
	start := strings.Index(s, prefix)
	if start < 0 {
		return s
	}
	end := strings.Index(s[start:], "]")
	if end < 0 {
		return strings.TrimSpace(s[:start])
	}
	return strings.TrimSpace(s[:start] + s[start+end+1:])
}

// stripCompanionMarker removes any [Name: ...] companion tag from s.
// Used during streaming so the raw marker never reaches the viewport.
// Handles three cases:
//  1. Complete tag present → strip it, return surrounding text
//  2. Tag partially streamed (e.g. "[Na" before closing "]") → hide from "["
//  3. End of s is a partial prefix match (e.g. s ends with "[N") → strip tail
func (m Model) stripCompanionMarker(s string) string {
	if m.companionName == "" {
		return s
	}
	prefix := "[" + m.companionName + ": "

	// Case 1 & 2: full prefix found somewhere in s.
	if start := strings.Index(s, prefix); start >= 0 {
		end := strings.Index(s[start:], "]")
		if end < 0 {
			return strings.TrimSpace(s[:start])
		}
		return strings.TrimSpace(s[:start] + s[start+end+1:])
	}

	// Case 3: s ends with a partial prefix — "[", "[N", "[Na", etc.
	// Walk from longest possible partial down to 1.
	for i := len(prefix) - 1; i >= 1; i-- {
		if strings.HasSuffix(s, prefix[:i]) {
			return strings.TrimSpace(s[:len(s)-i])
		}
	}

	return s
}

// renderCompanionBubble renders a speech bubble with the companion face.
// The face and bubble box are joined horizontally so they align properly.
// Returns "" when no bubble is active or companion not configured.
func (m Model) renderCompanionBubble() string {
	if m.companionBubble == "" {
		return ""
	}
	sc, err := buddy.Load()
	if err != nil || sc == nil {
		return ""
	}
	bones := buddy.GenerateBones(sc.UserID)
	sprite := buddy.RenderSprite(bones, m.buddyFrame)

	const maxW = 28
	// Word-wrap the text to maxW columns.
	var wrapped []string
	words := strings.Fields(m.companionBubble)
	var cur string
	for _, w := range words {
		if cur == "" {
			cur = w
		} else if lipgloss.Width(cur)+1+lipgloss.Width(w) <= maxW {
			cur += " " + w
		} else {
			wrapped = append(wrapped, cur)
			cur = w
		}
	}
	if cur != "" {
		wrapped = append(wrapped, cur)
	}

	// Build the speech bubble text block.
	textStyle := lipgloss.NewStyle().Foreground(colorFg).Italic(true)
	var rows []string
	for _, l := range wrapped {
		rows = append(rows, textStyle.Render(l))
	}
	bubbleStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		PaddingLeft(1).PaddingRight(1).
		Width(maxW + 2)
	bubble := bubbleStyle.Render(strings.Join(rows, "\n"))

	// Join animated sprite + bubble side by side.
	spriteStyle := lipgloss.NewStyle().PaddingRight(1)
	return lipgloss.JoinHorizontal(lipgloss.Center, spriteStyle.Render(sprite), bubble)
}
