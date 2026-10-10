package agent

import (
	"context"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/hooks"
	"github.com/icehunter/conduit/internal/permissions"
)

// PromptBlockedError is returned when a UserPromptSubmit hook refused the
// prompt (Stopped is false) or stopped the turn (Stopped is true). No request
// was made and the prompt is not in the returned history. Prompt is what the
// user typed, so a caller can put it back in the input box.
type PromptBlockedError struct {
	Reason  string
	Prompt  string
	Stopped bool
}

func (e *PromptBlockedError) Error() string {
	if e.Stopped {
		if e.Reason == "" {
			return "UserPromptSubmit hook stopped the turn"
		}
		return e.Reason
	}
	return "UserPromptSubmit operation blocked by hook:\n" + e.Reason
}

// UserPrompt names what the user typed, so UserPromptSubmit hooks see it and
// not the content built from it. Only a caller that has such a prompt supplies
// one; every other user-role message (tool results, notifications, nudges,
// steering the caller did not screen) runs without hooks.
type UserPrompt struct {
	// Raw is the typed text before @-mention and file expansion, or the
	// original "/cmd args" for a slash command. It is the hook's input.
	Raw string
	// Text is the text of the content block an updatedPrompt replaces. It
	// defaults to Raw; a slash command's block holds the expanded prompt.
	Text string
	// Block is 1 plus the index of the content block holding the typed text,
	// when the caller built the content and knows it. 0 means find the last
	// text block equal to Text.
	Block int
	// Typed marks a prompt the user submitted even when Raw is empty (an
	// images-only submit). Hooks run for it with an empty prompt. A zero
	// UserPrompt, which is no prompt at all, has neither.
	Typed bool
}

// active reports whether the user submitted this prompt, so hooks screen it.
func (p UserPrompt) active() bool { return p.Typed || p.Raw != "" }

func (p UserPrompt) target() string {
	if p.Text != "" {
		return p.Text
	}
	return p.Raw
}

// HasPromptHooks reports whether UserPromptSubmit hooks would run for a
// prompt on this loop.
func (l *Loop) HasPromptHooks() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return !l.child && l.cfg.Hooks != nil && len(l.cfg.Hooks.UserPromptSubmit) > 0
}

// hookPermissionMode maps a permission mode to the name Claude Code's hook
// payload uses; conduit-only modes report their nearest equivalent.
func hookPermissionMode(m permissions.Mode) string {
	switch m {
	case permissions.ModeAcceptEditsLive:
		return string(permissions.ModeAcceptEdits)
	case permissions.ModeCouncil:
		return string(permissions.ModePlan)
	default:
		return string(m)
	}
}

// ScreenPrompt runs the UserPromptSubmit hooks over prompt and returns the
// content to send: the typed-text block rewritten by updatedPrompt and hook
// context appended as a system-reminder block. When a hook blocks or stops it
// returns a *PromptBlockedError. Events (notices, a session title) go to
// notify. Child loops, loops without hooks and a zero prompt return content
// unchanged. content is never modified in place.
func (l *Loop) ScreenPrompt(ctx context.Context, prompt UserPrompt, content []api.ContentBlock, notify func(LoopEvent)) ([]api.ContentBlock, error) {
	if !prompt.active() || !l.HasPromptHooks() {
		return content, nil
	}
	l.mu.RLock()
	matchers := l.cfg.Hooks.UserPromptSubmit
	gate, sessionID, cwd, transcript := l.cfg.Gate, l.cfg.SessionID, l.cfg.Cwd, l.cfg.TranscriptPath
	l.mu.RUnlock()

	input := hooks.HookInput{SessionID: sessionID, Prompt: prompt.Raw, Cwd: cwd}
	if gate != nil {
		input.PermissionMode = hookPermissionMode(gate.Mode())
	}
	if transcript != nil {
		input.TranscriptPath = transcript()
	}
	res := hooks.RunUserPromptSubmit(ctx, matchers, input)
	for _, w := range res.Warnings {
		notify(LoopEvent{Type: EventNotice, Notice: "UserPromptSubmit hook failed, prompt sent anyway: " + w})
	}
	if res.SystemMessage != "" {
		notify(LoopEvent{Type: EventNotice, Notice: res.SystemMessage})
	}
	if res.SessionTitle != "" {
		notify(LoopEvent{Type: EventSessionTitle, Notice: res.SessionTitle})
	}
	if res.Blocked || res.Stopped {
		reason := res.Reason
		if res.Stopped {
			reason = res.StopReason
		}
		return content, &PromptBlockedError{Reason: reason, Prompt: prompt.Raw, Stopped: res.Stopped}
	}

	out := make([]api.ContentBlock, len(content), len(content)+1)
	copy(out, content)
	if res.UpdatedPrompt != nil {
		replaced := false
		if i := prompt.Block - 1; i >= 0 && i < len(out) && out[i].Type == "text" {
			out[i] = api.ContentBlock{Type: "text", Text: *res.UpdatedPrompt}
			replaced = true
		}
		for i := len(out) - 1; i >= 0 && !replaced; i-- {
			if out[i].Type == "text" && out[i].Text == prompt.target() {
				out[i] = api.ContentBlock{Type: "text", Text: *res.UpdatedPrompt}
				replaced = true
			}
		}
		if !replaced {
			notify(LoopEvent{Type: EventNotice, Notice: "UserPromptSubmit updatedPrompt ignored: the prompt text was not found in the message"})
		}
	}
	if res.AdditionalContext != "" {
		out = append(out, api.ContentBlock{
			Type: "text",
			Text: "<system-reminder>\nUserPromptSubmit hook additional context: " + res.AdditionalContext + "\n</system-reminder>",
		})
	}
	return out, nil
}

// screenLastMessage runs ScreenPrompt over the trailing user message of msgs.
// On a block the message is removed from the returned history.
func (l *Loop) screenLastMessage(ctx context.Context, prompt UserPrompt, msgs []api.Message, notify func(LoopEvent)) ([]api.Message, error) {
	last := len(msgs) - 1
	if !prompt.active() || last < 0 || msgs[last].Role != "user" {
		return msgs, nil
	}
	content, err := l.ScreenPrompt(ctx, prompt, msgs[last].Content, notify)
	if err != nil {
		return msgs[:last:last], err
	}
	out := make([]api.Message, len(msgs))
	copy(out, msgs)
	out[last].Content = content
	return out, nil
}

// ClearSteer discards the steering message waiting for the next tool round and
// returns it, nil when there is none. A caller whose turn ended before the
// loop delivered it takes the message back instead of leaving it to leak into
// an unrelated later turn.
func (l *Loop) ClearSteer() []api.ContentBlock {
	if l == nil {
		return nil
	}
	if p := l.steerMsg.Swap(nil); p != nil {
		return *p
	}
	return nil
}
