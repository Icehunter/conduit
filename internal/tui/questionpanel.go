package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// questionAskState drives the interactive AskUserQuestion selection dialog.
// It mirrors the behaviour of QuestionView.tsx / use-select-state.ts from the
// Claude Code source (src/components/CustomSelect/).
type questionAskState struct {
	question string
	options  []questionOption
	multi    bool
	reply    chan<- []string

	// Navigation
	focusedIdx int    // 0..len(options); len(options) == Submit button (multi only)
	selected   []bool // multi-select checked state per option
	textMode   bool   // Tab was pressed — show free-text input instead
	textBuf    string // text typed in textMode

	// guardFirstKey swallows the first key after the dialog opens so a
	// keystroke already in flight (user was typing when the popup appeared)
	// cannot auto-select or auto-submit. Esc/ctrl+c pass through so the
	// user can dismiss immediately.
	guardFirstKey bool
}

// submitIdx returns the virtual index for the "Submit" button in multi-select.
func (q *questionAskState) submitIdx() int { return len(q.options) }

// newQuestionAskState constructs a questionAskState from a questionAskMsg with
// guardFirstKey set so the first keypress after the dialog opens is swallowed.
func newQuestionAskState(msg questionAskMsg) *questionAskState {
	return &questionAskState{
		question:      msg.question,
		options:       msg.options,
		multi:         msg.multi,
		reply:         msg.reply,
		focusedIdx:    0,
		selected:      make([]bool, len(msg.options)),
		textMode:      len(msg.options) == 0,
		guardFirstKey: true,
	}
}

// handleQuestionKey handles all keyboard input while the question dialog is active.
func (m Model) handleQuestionKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	q := m.questionAsk
	key := msg.String()

	// Focus guard: swallow the first key after the dialog opens so a
	// keystroke already in flight cannot auto-select or auto-submit.
	// Esc and ctrl+c pass through so the user can dismiss immediately.
	if q.guardFirstKey {
		q.guardFirstKey = false
		if key != "esc" && key != "ctrl+c" && key != "enter" {
			m.questionAsk = q
			return m, nil
		}
	}

	// sendAnswer closes the dialog, posts the answer to the reply channel as a
	// tea.Cmd (non-blocking), and adds the answer to the chat log.
	sendAnswer := func(answers []string) (Model, tea.Cmd) {
		reply := q.reply
		display := strings.Join(answers, ", ")
		m.questionAsk = nil
		m.messages = append(m.messages, Message{Role: RoleUser, Content: display})
		m.refreshViewport()
		m.vp.GotoBottom()
		return m, func() tea.Msg { reply <- answers; return nil }
	}
	cancel := func() (Model, tea.Cmd) {
		reply := q.reply
		m.questionAsk = nil
		m.refreshViewport()
		return m, func() tea.Msg { reply <- nil; return nil }
	}

	// --- Text mode ---
	if q.textMode {
		switch key {
		case "enter":
			if q.textBuf == "" {
				return m, nil
			}
			return sendAnswer([]string{q.textBuf})
		case "tab", "shift+tab":
			if len(q.options) > 0 {
				q.textMode = false
			}
		case "esc":
			if len(q.options) > 0 {
				q.textMode = false
			} else {
				return cancel()
			}
		case "backspace", "ctrl+h":
			if len(q.textBuf) > 0 {
				q.textBuf = q.textBuf[:len(q.textBuf)-1]
			}
		case "ctrl+c":
			return cancel()
		default:
			if len(key) == 1 {
				q.textBuf += key
			} else if key == "space" {
				q.textBuf += " "
			}
		}
		m.questionAsk = q
		m.refreshViewport()
		return m, nil
	}

	// --- List navigation mode ---
	numOpts := len(q.options)
	maxIdx := numOpts // submit button index for multi
	if !q.multi {
		maxIdx = numOpts - 1
	}

	switch key {
	case "up", "ctrl+p":
		if q.focusedIdx > 0 {
			q.focusedIdx--
		}

	case "down", "ctrl+n":
		if q.focusedIdx < maxIdx {
			q.focusedIdx++
		}

	case "tab":
		q.textMode = true
		q.textBuf = ""

	case "esc", "ctrl+c":
		return cancel()

	case "space":
		if q.multi && q.focusedIdx < numOpts {
			q.selected[q.focusedIdx] = !q.selected[q.focusedIdx]
		}

	case "enter":
		if q.multi {
			if q.focusedIdx == q.submitIdx() {
				answers := collectMultiAnswers(q)
				if len(answers) == 0 {
					m.questionAsk = q
					m.refreshViewport()
					return m, nil
				}
				return sendAnswer(answers)
			}
			// Toggle focused option, then advance focus to Submit.
			q.selected[q.focusedIdx] = !q.selected[q.focusedIdx]
			q.focusedIdx = q.submitIdx()
		} else {
			if q.focusedIdx >= numOpts {
				break
			}
			o := q.options[q.focusedIdx]
			answer := o.Value
			if answer == "" {
				answer = o.Label
			}
			return sendAnswer([]string{answer})
		}

	default:
		// Numeric focus: "1".."9" moves focus (single-select) or toggles+focuses (multi).
		// In single-select, Enter is required to confirm — digits no longer instant-submit
		// so a stray digit in flight cannot auto-select.
		if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
			n := int(key[0] - '1')
			if n < numOpts {
				if q.multi {
					q.selected[n] = !q.selected[n]
					q.focusedIdx = n
				} else {
					q.focusedIdx = n // focus only; Enter confirms
				}
			}
		}
	}

	m.questionAsk = q
	m.refreshViewport()
	return m, nil
}

// collectMultiAnswers returns selected option values in order.
func collectMultiAnswers(q *questionAskState) []string {
	var out []string
	for i, sel := range q.selected {
		if sel {
			v := q.options[i].Value
			if v == "" {
				v = q.options[i].Label
			}
			out = append(out, v)
		}
	}
	return out
}

// renderQuestionDialog renders the AskUserQuestion selection overlay.
func (m Model) renderQuestionDialog() string {
	q := m.questionAsk
	if q == nil {
		return ""
	}

	var sb strings.Builder
	bodyW := floatingInnerWidth(m.width, floatingModalSpec) - floatingBodyPadX*6
	bodyW = max(bodyW, 20)

	// Question text.
	sb.WriteString(styleStatusAccent.Render("◆ Question") + "\n\n")
	// Wrap long question text at panel width.
	qText := wordWrap(q.question, bodyW)
	sb.WriteString(stylePickerItem.Render(qText) + "\n\n")

	if q.textMode {
		// Free-text input mode.
		sb.WriteString(stylePickerDesc.Render("Type your answer:") + "\n")
		cursor := "█"
		display := q.textBuf + cursor
		sb.WriteString(stylePickerItemSelected.Render("  › "+display) + "\n")
		if len(q.options) > 0 {
			sb.WriteString("\n" + stylePickerDesc.Render("Enter to submit · Tab/Esc to go back"))
		} else {
			sb.WriteString("\n" + stylePickerDesc.Render("Enter to submit · Esc to cancel"))
		}
	} else {
		numOpts := len(q.options)
		// Cap visible options to avoid taking over the whole screen.
		const maxVisible = 7
		startIdx := 0
		if q.focusedIdx >= maxVisible {
			startIdx = q.focusedIdx - maxVisible + 1
		}
		endIdx := startIdx + maxVisible
		if endIdx > numOpts {
			endIdx = numOpts
		}

		if startIdx > 0 {
			sb.WriteString(stylePickerDesc.Render("  ↑ more options above") + "\n")
		}
		for i := startIdx; i < endIdx; i++ {
			o := q.options[i]
			focused := i == q.focusedIdx
			label := fmt.Sprintf("%d. %s", i+1, truncatePlainToWidth(o.Label, max(bodyW-8, 8)))
			if q.multi {
				check := "[ ]"
				if q.selected[i] {
					check = "[✔]"
				}
				label = check + " " + label
			}
			var line string
			if focused {
				line = stylePickerItemSelected.Render("  › " + label)
			} else {
				line = stylePickerItem.Render("    " + label)
			}
			sb.WriteString(line + "\n")
			if o.Description != "" && focused {
				desc := indentLines(wordWrap(o.Description, max(bodyW-7, 10)), "       ")
				sb.WriteString(stylePickerDesc.Render(desc) + "\n")
			}
		}
		if endIdx < numOpts {
			sb.WriteString(stylePickerDesc.Render("  ↓ more options below") + "\n")
		}

		// Multi-select submit button.
		if q.multi {
			sb.WriteString("\n")
			submitFocused := q.focusedIdx == q.submitIdx()
			nChecked := 0
			for _, s := range q.selected {
				if s {
					nChecked++
				}
			}
			label := fmt.Sprintf("Submit (%d selected)", nChecked)
			if submitFocused {
				sb.WriteString(stylePickerItemSelected.Render("  › "+label) + "\n")
			} else {
				sb.WriteString(stylePickerItem.Render("    "+label) + "\n")
			}
			sb.WriteString("\n" + stylePickerDesc.Render("↑/↓ navigate · Space toggle · Enter submit · Tab to type"))
		} else {
			sb.WriteString("\n" + stylePickerDesc.Render("↑/↓ navigate · Enter select · 1-9 focus · Tab to type"))
		}
	}

	return sb.String()
}

// wordWrap wraps s to at most maxWidth visible terminal cells per line.
func wordWrap(s string, maxWidth int) string {
	if maxWidth <= 0 || lipgloss.Width(s) <= maxWidth {
		return s
	}
	var out strings.Builder
	words := strings.Fields(s)
	lineLen := 0
	for _, w := range words {
		wordWidth := lipgloss.Width(w)
		if wordWidth > maxWidth {
			if lineLen > 0 {
				out.WriteByte('\n')
				lineLen = 0
			}
			remaining := w
			for lipgloss.Width(remaining) > maxWidth {
				part := truncatePlainToWidth(remaining, maxWidth)
				part = strings.TrimSuffix(part, "…")
				if part == "" {
					break
				}
				out.WriteString(part)
				out.WriteByte('\n')
				remaining = strings.TrimPrefix(remaining, part)
			}
			if remaining != "" {
				out.WriteString(remaining)
				lineLen = lipgloss.Width(remaining)
			}
			continue
		}
		if lineLen == 0 {
			out.WriteString(w)
			lineLen = wordWidth
			continue
		}
		if lineLen+1+wordWidth > maxWidth {
			out.WriteByte('\n')
			out.WriteString(w)
			lineLen = wordWidth
		} else {
			out.WriteByte(' ')
			out.WriteString(w)
			lineLen += 1 + wordWidth
		}
	}
	return out.String()
}
