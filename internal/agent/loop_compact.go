package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/compact"
	"github.com/icehunter/conduit/internal/microcompact"
	internalmodel "github.com/icehunter/conduit/internal/model"
	"github.com/icehunter/conduit/internal/settings"
)

const (
	// maxTokensFloor is the smallest max_tokens an overflow clamp sets (CC).
	maxTokensFloor = 3000
	// maxTokensHeadroom is left below the window when clamping (CC).
	maxTokensHeadroom = 1000
	// maxClampsPerRequest bounds max_tokens clamps: the second runs only when
	// the follow-up error reports counts the first lacked.
	maxClampsPerRequest = 2
	// minThinkingBudget is the smallest budget the API accepts.
	minThinkingBudget = 1024

	// localSummarizer names the local fallback in EventCompacted.CompactModel.
	localSummarizer = "local"
)

// runState is Run's mutable state that more than one recovery path changes.
// failoverState and overflow recovery point at it.
type runState struct {
	client         *api.Client
	model          string
	window         int
	system         []api.SystemBlock
	thinkingBudget int
	// contextCompacted rides on every attempt of the first request after a
	// compaction, including retries, and clears once a response drains (CC
	// consumes it once per request and re-adds it on each retry).
	contextCompacted string

	// maxTokensOverride, when positive, replaces LoopConfig.MaxTokens for the
	// rest of the Run; set by a max_tokens overflow clamp.
	maxTokensOverride int
	clamps            int
	// microDone and reactiveDone make each overflow step run once per Run.
	microDone, reactiveDone bool
}

// requestMaxTokens is the max_tokens to send: the clamp when one was set.
func (rs *runState) requestMaxTokens(configured int) int {
	if rs.maxTokensOverride > 0 {
		return rs.maxTokensOverride
	}
	return configured
}

// composeSystemLocked returns the base system blocks followed by the overlay.
// The caller holds l.mu.
func (l *Loop) composeSystemLocked() []api.SystemBlock {
	system := l.cfg.System
	if len(l.systemOverlay) > 0 {
		system = append(append([]api.SystemBlock(nil), system...), l.systemOverlay...)
	}
	return system
}

// isChild reports whether this loop serves a parent's sub-agent, teammate or
// background task rather than the user.
func (l *Loop) isChild() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.subAgentLabel != ""
}

// isClaudeID reports whether a model ID names a Claude model.
func isClaudeID(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "claude-")
}

// dashStyleClaudeID reports whether model is a Claude ID in Anthropic's dash
// form ("claude-sonnet-4-6"), not Copilot's dotted form ("claude-sonnet-4.5").
func dashStyleClaudeID(model string) bool {
	return isClaudeID(model) && !strings.Contains(model, ".")
}

// isCopilotHost reports whether a client host is GitHub Copilot's API. Its
// Messages shim names Haiku "claude-haiku-4.5", and some of its Claude IDs
// are dash-style ("claude-sonnet-4"), so the model name cannot tell it apart.
func isCopilotHost(host string) bool {
	return strings.HasSuffix(strings.ToLower(host), "githubcopilot.com")
}

// haikuAllowed reports whether the Haiku background model can be sent through
// active: the client speaks the Anthropic Messages API and either is the
// first-party endpoint or is another gateway serving dash-style Claude IDs.
// Copilot is excluded by endpoint.
func haikuAllowed(active *api.Client, activeModel string) bool {
	if active == nil || !active.SpeaksAnthropicMessages() {
		return false
	}
	return active.FirstPartyAnthropic() || (dashStyleClaudeID(activeModel) && !isCopilotHost(active.BaseHost()))
}

// configuredBackgroundModel is LoopConfig.BackgroundModel's answer, "" when
// unset.
func (l *Loop) configuredBackgroundModel() string {
	l.mu.RLock()
	bg := l.cfg.BackgroundModel
	l.mu.RUnlock()
	if bg == nil {
		return ""
	}
	return strings.TrimSpace(bg())
}

// backgroundTarget picks the client and model for helper work (compaction,
// background review, memory extraction), in order:
//
//  1. The background role from RoleResolver. A role without its own client
//     means "the active provider", which only works when the active client
//     speaks the Anthropic Messages API and the role names a Claude model.
//  2. The configured background model (Haiku), when haikuAllowed.
//  3. The active client and model.
//
// RoleResolver is called without holding any lock.
func (l *Loop) backgroundTarget(active *api.Client, model string) (*api.Client, string) {
	l.mu.RLock()
	resolve := l.cfg.RoleResolver
	l.mu.RUnlock()
	if resolve != nil {
		if roleModel, roleClient, ok := resolve(settings.RoleBackground); ok && strings.TrimSpace(roleModel) != "" {
			if roleClient != nil {
				return roleClient, roleModel
			}
			if active != nil && active.SpeaksAnthropicMessages() && isClaudeID(roleModel) {
				return active, roleModel
			}
		}
	}
	if bg := l.configuredBackgroundModel(); bg != "" {
		// A configured model that is not a Claude ID belongs to the background
		// role's own provider, reached through RoleResolver's client above.
		if isClaudeID(bg) && haikuAllowed(active, model) {
			return active, bg
		}
	}
	return active, model
}

// BackgroundModel returns the helper/background model for new secondary calls
// on the current client. It never returns a Claude ID for a client that cannot
// serve one: the active model stands in.
func (l *Loop) BackgroundModel() string {
	l.mu.RLock()
	active, model := l.client, l.cfg.Model
	l.mu.RUnlock()
	if bg := l.configuredBackgroundModel(); bg != "" && isClaudeID(bg) && haikuAllowed(active, model) {
		return bg
	}
	return model
}

// autoCompactThreshold is the input-token count that triggers auto-compaction.
func (l *Loop) autoCompactThreshold(rs *runState) int {
	window := internalmodel.ResolveContextWindow(rs.window, rs.model)
	limit := internalmodel.DefaultAutoCompactTriggerCap
	l.mu.RLock()
	capFn := l.cfg.AutoCompactTriggerCap
	l.mu.RUnlock()
	if capFn != nil {
		limit = capFn()
	}
	return internalmodel.AutoCompactThreshold(window, limit)
}

// maybeAutoCompact compacts msgs when the last response's prompt size crossed
// the trigger. It returns msgs unchanged when compaction is off, not needed,
// tripped, or failed (a failure is not fatal: the next turn tries again until
// the breaker opens). Sub-agents never auto-compact at end_turn; they keep
// reactive compaction.
func (l *Loop) maybeAutoCompact(ctx context.Context, rs *runState, msgs []api.Message, inputTokens int, handler func(LoopEvent)) []api.Message {
	if !l.cfg.AutoCompact || inputTokens <= 0 || os.Getenv("DISABLE_AUTO_COMPACT") != "" || l.isChild() {
		return msgs
	}
	threshold := l.autoCompactThreshold(rs)
	if inputTokens <= threshold {
		l.compactStuck.Store(0)
		return msgs
	}
	if l.compactFails.Load() >= internalmodel.MaxConsecutiveCompactFail {
		return msgs
	}
	// The last compaction could not get the prompt under the trigger (the
	// system prompt and tools alone are close to it on a small window), and
	// the prompt has not grown meaningfully since: another would change nothing.
	if stuck := l.compactStuck.Load(); stuck > 0 && int64(inputTokens) <= stuck+stuck/4 {
		return msgs
	}
	res, model, err := l.compactHistory(ctx, rs, msgs, compact.KindAuto, false, handler)
	if err != nil {
		return msgs
	}
	preLen := len(msgs)
	l.MarkContextCompacted(compact.KindAuto)
	// What the prompt will weigh: everything that is not conversation (system
	// prompt, tools) is what the last request had beyond the old history.
	overhead := max(0, inputTokens-estimateTokens(msgs))
	if after := overhead + estimateTokens(res.NewHistory); after > threshold {
		l.compactStuck.Store(int64(after))
	} else {
		l.compactStuck.Store(0)
	}
	// Compaction is the natural refresh point: the cached prefix is
	// invalidated anyway, so picking up memories and skills written earlier in
	// this session costs nothing extra.
	l.refreshRunSystem(rs)
	handler(LoopEvent{
		Type:                 EventCompacted,
		CompactKind:          compact.KindAuto,
		CompactSummary:       res.Summary,
		CompactModel:         model,
		CompactTailLen:       res.TailLen,
		CompactPreLen:        preLen,
		CompactedInputTokens: inputTokens,
		CompactedThreshold:   threshold,
		History:              append([]api.Message(nil), res.NewHistory...),
	})
	return res.NewHistory
}

// refreshRunSystem rebuilds the system blocks after a compaction and points
// the Run at them.
func (l *Loop) refreshRunSystem(rs *runState) {
	if l.RefreshSystem() == nil {
		return
	}
	l.mu.RLock()
	rs.system = l.composeSystemLocked()
	l.mu.RUnlock()
}

// RecoveryTools names the registered tools a compacted conversation can use to
// get its history back.
func (l *Loop) RecoveryTools() []string {
	var names []string
	for _, name := range []string{"session_search", "CCRRetrieve"} {
		if _, ok := l.reg.Lookup(name); ok {
			names = append(names, name)
		}
	}
	return names
}

// compactOptions builds the compaction options for a summarizer model.
func (l *Loop) compactOptions(rs *runState, summarizer string, summarizerWindow int, kind string) compact.Options {
	// The request that overflowed must be sent again, so its trailing user
	// message cannot be summarized away.
	l.mu.RLock()
	sessionID := l.cfg.SessionID
	l.mu.RUnlock()
	return compact.Options{
		Model:            summarizer,
		SummarizerWindow: summarizerWindow,
		Kind:             kind,
		SessionID:        sessionID,
		MainWindow:       internalmodel.ResolveContextWindow(rs.window, rs.model),
		RecoveryTools:    l.RecoveryTools(),
		RequireTail:      kind == compact.KindReactive,
	}
}

// summarizerWindow is the context window of the model that will write the
// summary, which bounds the transcript it is sent. The active model's is the
// Run's window; a role's own client reports its provider's; a Claude model on
// the active client is derived from its name (0).
func (l *Loop) summarizerWindow(rs *runState, c *api.Client, model string) int {
	switch {
	case c == rs.client && model == rs.model:
		return internalmodel.ResolveContextWindow(rs.window, rs.model)
	case c != rs.client:
		l.mu.RLock()
		fn := l.cfg.RoleContextWindow
		l.mu.RUnlock()
		if fn != nil {
			return fn(settings.RoleBackground)
		}
	}
	return 0
}

// transientCompactErr reports whether err says nothing about the summarizer
// being broken: the network, throttling or a cancel. Those neither count
// toward the breaker nor justify switching summarizer.
func transientCompactErr(err error) bool {
	switch api.Classify(err).Class {
	case api.ClassTransient, api.ClassRateLimited, api.ClassCancelled:
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// compactHistory compacts msgs on the background target of the Run's current
// client and model, so it follows a failover. A non-transient failure of that
// target is retried once on the active model, with a notice. With
// localFallback a failure that still stands is answered by the local summary,
// since the caller cannot continue without a smaller history. Success resets
// the breaker; a non-transient failure counts toward it. It returns the model
// that wrote the summary ("local" for the fallback).
func (l *Loop) compactHistory(ctx context.Context, rs *runState, msgs []api.Message, kind string, localFallback bool, handler func(LoopEvent)) (*compact.Result, string, error) {
	targetClient, targetModel := l.backgroundTarget(rs.client, rs.model)
	used := targetModel
	opts := l.compactOptions(rs, targetModel, l.summarizerWindow(rs, targetClient, targetModel), kind)
	res, err := compact.CompactWithOptions(l.apiCtx(ctx), targetClient, msgs, opts)

	if err != nil && !compact.IsNothingToCompact(err) && !transientCompactErr(err) &&
		(targetClient != rs.client || targetModel != rs.model) {
		handler(LoopEvent{Type: EventNotice, Notice: fmt.Sprintf("Summary model %s failed — retrying with %s", targetModel, rs.model)})
		used = rs.model
		opts.Model = rs.model
		opts.SummarizerWindow = l.summarizerWindow(rs, rs.client, rs.model)
		res, err = compact.CompactWithOptions(l.apiCtx(ctx), rs.client, msgs, opts)
	}
	failed := err != nil && !compact.IsNothingToCompact(err) && !transientCompactErr(err)

	if err != nil && localFallback && ctx.Err() == nil && !compact.IsNothingToCompact(err) {
		var lerr error
		if res, lerr = compact.CompactLocal(msgs, opts); lerr == nil {
			used, err = localSummarizer, nil
		}
	}
	if err == nil && (res == nil || len(res.NewHistory) == 0) {
		err = errors.New("agent: compaction produced an empty history")
		failed = true
	}
	if failed {
		l.compactFails.Add(1)
	}
	if err != nil {
		return nil, "", fmt.Errorf("agent: compact: %w", err)
	}
	if used != localSummarizer {
		l.compactFails.Store(0)
		l.mu.RLock()
		report := l.cfg.OnSubAgentUsage
		l.mu.RUnlock()
		if report != nil && res.Usage.InputTokens+res.Usage.OutputTokens > 0 {
			report(used, res.Usage)
		}
	}
	// A cancel that landed after the summary came back: the history it would
	// replace is not the one the user is left with, so persist and announce
	// nothing.
	if cerr := ctx.Err(); cerr != nil {
		return nil, "", fmt.Errorf("agent: compact: %w", cerr)
	}
	if res.Fallback {
		handler(LoopEvent{Type: EventNotice, Notice: compact.FallbackNotice})
	}
	return res, used, nil
}

// recoverOverflow recovers from a request that exceeded the model's limits.
// Each step runs at most once per Run, in order:
//
//  1. A max_tokens overflow ("input + max_tokens > limit") lowers max_tokens
//     to max(3000, limit - input - 1000), CC's own rule. A prompt that alone
//     overflows skips this. When the limit is a known output cap (input 0,
//     OpenAI's "at most N completion tokens") the clamp is that cap.
//  2. Microcompaction, when it shrinks the payload.
//  3. A reactive compaction (unless DISABLE_AUTO_COMPACT is set), after which
//     the next request carries context-compacted: reactive.
//
// It returns the history to resend and true when it changed something worth
// resending, or false to surface the error.
func (l *Loop) recoverOverflow(ctx context.Context, c api.Classified, msgs []api.Message, rs *runState, handler func(LoopEvent)) ([]api.Message, bool) {
	if rs == nil {
		return msgs, false
	}
	if c.Overflow != nil && l.clampMaxTokens(rs, *c.Overflow) {
		return msgs, true
	}
	if !rs.microDone {
		rs.microDone = true
		if shrunk, ok := l.microcompactForOverflow(rs, msgs); ok {
			return shrunk, true
		}
	}
	if rs.reactiveDone || os.Getenv("DISABLE_AUTO_COMPACT") != "" {
		return msgs, false
	}
	rs.reactiveDone = true

	notice := "Context window exceeded — compacting and retrying"
	if c.PromptTokens > 0 && c.PromptLimit > 0 {
		notice = fmt.Sprintf("Context window exceeded (%d > %d) — compacting and retrying", c.PromptTokens, c.PromptLimit)
	}
	handler(LoopEvent{Type: EventNotice, Notice: notice})

	res, model, err := l.compactHistory(ctx, rs, msgs, compact.KindReactive, true, handler)
	if err != nil {
		return msgs, false
	}
	rs.contextCompacted = compact.KindReactive
	l.refreshRunSystem(rs)
	handler(LoopEvent{
		Type:           EventCompacted,
		CompactKind:    compact.KindReactive,
		CompactSummary: res.Summary,
		CompactModel:   model,
		CompactTailLen: res.TailLen,
		CompactPreLen:  len(msgs),
		History:        append([]api.Message(nil), res.NewHistory...),
	})
	return res.NewHistory, true
}

// clampMaxTokens applies step 1 of recoverOverflow. It reports whether it
// lowered max_tokens, so the same request is worth resending.
func (l *Loop) clampMaxTokens(rs *runState, o api.MaxTokensOverflow) bool {
	if rs.clamps >= maxClampsPerRequest || o.Limit <= 0 || (o.Input > 0 && o.Input >= o.Limit) {
		return false
	}
	target := o.Limit // a known output cap
	if o.Input > 0 {
		target = max(maxTokensFloor, o.Limit-o.Input-maxTokensHeadroom)
	}
	if target >= rs.requestMaxTokens(l.cfg.MaxTokens) {
		return false // lowering max_tokens would not help
	}
	rs.clamps++
	rs.maxTokensOverride = target
	if rs.thinkingBudget > 0 {
		// The budget must stay below max_tokens, and below the API minimum
		// thinking is off.
		rs.thinkingBudget = min(rs.thinkingBudget, target-1)
		if rs.thinkingBudget < minThinkingBudget {
			rs.thinkingBudget = 0
		}
	}
	return true
}

// microcompactForOverflow shrinks old tool results regardless of the cache
// state, reporting whether that saved anything.
func (l *Loop) microcompactForOverflow(rs *runState, msgs []api.Message) ([]api.Message, bool) {
	keep := l.cfg.MicroCompactKeep
	if keep == 0 {
		keep = microcompact.DefaultKeepRecent
	}
	r := microcompact.ApplyWithContext(msgs, time.Now().Add(-time.Hour), 0, keep,
		internalmodel.ResolveContextWindow(rs.window, rs.model))
	if !r.Triggered || r.TokensSaved <= 0 {
		return msgs, false
	}
	return r.Messages, true
}

// estimateTokens approximates the tokens of msgs as len(JSON)/4, the same
// proxy the pre-flight check uses.
func estimateTokens(msgs []api.Message) int {
	raw, err := json.Marshal(msgs)
	if err != nil {
		return 0
	}
	return len(raw) / 4
}
