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

	if !m.userAddressedCompanion(sc.Name) {
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

func (m Model) userAddressedCompanion(name string) bool {
	companionLower := strings.ToLower(name)
	seenAssistant := false
	for i := len(m.messages) - 1; i >= 0; i-- {
		msg := m.messages[i]
		if msg.Role == RoleUser {
			lower := strings.ToLower(msg.Content)
			idx := strings.Index(lower, companionLower)
			for idx >= 0 {
				before := idx == 0 || !isLetter(rune(lower[idx-1]))
				after := idx+len(companionLower) >= len(lower) || !isLetter(rune(lower[idx+len(companionLower)]))
				if before && after {
					return true
				}
				next := strings.Index(lower[idx+1:], companionLower)
				if next < 0 {
					break
				}
				idx = idx + 1 + next
			}
			return false
		}
		if msg.Role == RoleAssistant {
			if seenAssistant {
				break
			}
			seenAssistant = true
		}
	}
	return false
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

// renderCompanionBubble renders the configured companion. When the companion
// has speech text, the face and bubble box are joined horizontally so they
// align properly; otherwise the idle face is still visible.
func (m Model) renderCompanionBubble() string {
	if m.companionName == "" {
		return ""
	}
	sc, err := buddy.Load()
	if err != nil || sc == nil {
		return ""
	}
	bones := buddy.GenerateBones(m.companionUserID, sc.ForcedRarity)
	sprite := buddy.RenderSprite(bones, m.buddyFrame)
	spriteStyle := lipgloss.NewStyle().Background(colorWindowBg)
	if strings.TrimSpace(m.companionBubble) == "" {
		return spriteStyle.Render(sprite)
	}

	const maxW = 34
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

	textStyle := fgOnBg(colorFg).Italic(true)
	var rows []string
	for _, l := range wrapped {
		rows = append(rows, textStyle.Render(l))
	}
	bubble := strings.Join(rows, "\n")

	// Join bubble + animated sprite side by side. The text sits to the left
	// so the companion can live on the lower-right without blocking the input.
	tail := fgOnBg(colorWindowTitle).Render("‹")
	content := lipgloss.JoinHorizontal(lipgloss.Center, bubble+tail, spriteStyle.Render(sprite))
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorWindowBorder).
		BorderBackground(colorWindowBg).
		Background(colorWindowBg).
		PaddingLeft(1).
		PaddingRight(1).
		Render(content)
}
