package agent

import (
	"context"
	"errors"
	"strings"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/tool"
)

const (
	// sideQuestionMaxTokens keeps a /btw answer short.
	sideQuestionMaxTokens = 1024
	sideQuestionPrompt    = "The user asked a quick SIDE question with /btw while the main work continues in the original session.\n" +
		"Rules:\n" +
		"- Answer ONLY the side question, using the conversation above as context. Do not continue, redo, or critique the main task.\n" +
		"- Do not call any tools; they are disabled for this side question. Answer directly in text.\n" +
		"- If the conversation does not contain enough information to answer, say so plainly instead of guessing.\n" +
		"- Be concise and direct."
)

// SideQuestion answers question using history as context without touching
// either: it runs a separate one-turn request on a copy of history, with no
// tools and a small output budget, and returns the answer text. The caller
// shows it and does not add it to the conversation. history may end
// mid-turn; the tail is trimmed to the last completed assistant message so
// the request stays valid.
func (l *Loop) SideQuestion(ctx context.Context, history []api.Message, question string) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", errors.New("agent: side question is empty")
	}
	child, model := l.sideQuestionChild()

	msgs := append(trimToCompletedAssistant(history), api.Message{
		Role:    "user",
		Content: []api.ContentBlock{{Type: "text", Text: sideQuestionPrompt + "\n\nSide question: " + question}},
	})
	var usage api.Usage
	out, err := child.Run(ctx, msgs, func(ev LoopEvent) {
		if ev.Type == EventUsage {
			usage.InputTokens += ev.Usage.InputTokens
			usage.OutputTokens += ev.Usage.OutputTokens
			usage.CacheCreationInputTokens += ev.Usage.CacheCreationInputTokens
			usage.CacheReadInputTokens += ev.Usage.CacheReadInputTokens
		}
	})
	l.mu.RLock()
	onUsage := l.cfg.OnSubAgentUsage
	l.mu.RUnlock()
	if onUsage != nil && usage.InputTokens+usage.OutputTokens > 0 {
		onUsage(model, usage)
	}
	if err != nil && !errors.Is(err, ErrMaxTurnsExceeded) {
		return "", err
	}
	// Only the new messages count: the answer is the last assistant text after
	// the question, never one from the copied history.
	if len(out) <= len(msgs) {
		return "", errors.New("agent: side question got no answer")
	}
	ans := strings.TrimSpace(extractLastAssistantText(out[len(msgs):]))
	if ans == "" {
		return "", errors.New("agent: side question got no answer")
	}
	return ans, nil
}

// trimToCompletedAssistant returns a copy of msgs cut back to the last
// assistant message that makes no tool call, dropping a mid-turn tail
// (unanswered tool_use, tool_result, in-flight user message).
func trimToCompletedAssistant(msgs []api.Message) []api.Message {
	end := len(msgs)
	for end > 0 {
		last := msgs[end-1]
		if last.Role == "assistant" && !hasToolUse(last.Content) {
			break
		}
		end--
	}
	return append([]api.Message(nil), msgs[:end]...)
}

// sideQuestionChild builds the one-turn, tool-less child that answers a side
// question, and returns it with its model.
func (l *Loop) sideQuestionChild() (*Loop, string) {
	l.mu.RLock()
	model := l.cfg.Model
	l.mu.RUnlock()
	child, model := l.buildChildLoop(SubAgentSpec{
		Model:     model,
		MaxTokens: sideQuestionMaxTokens,
	})
	// The request carries the parent's tool definitions: the history holds
	// tool_use blocks that need them (Anthropic rejects a tool-using history
	// without tools), and they keep the cached prefix the parent's requests
	// share. The child's own registry is empty, so nothing can run: a tool call
	// is answered "not found" and the one-turn budget ends the run.
	child.toolDefs = buildToolDefsFor(l.reg, model)
	child.reg = tool.NewRegistry()
	child.cfg.ThinkingBudget = 0
	child.cfg.MaxTurns = 1
	child.cfg.TTSRRules = nil
	child.cfg.Hooks = nil // no Stop or tool hooks for a side question
	// No wire identity: the loop sends class "main" with the user-turn hint,
	// because the user typed this.
	return child, model
}
