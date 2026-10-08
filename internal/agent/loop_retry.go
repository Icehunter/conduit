package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/providerrotation"
	"github.com/icehunter/conduit/internal/settings"
)

const (
	// maxToolUseRecoveries bounds history repairs for a tool_use/tool_result
	// pairing rejection.
	maxToolUseRecoveries = 2
	// maxStreamRetries bounds loop-level retries of a response that failed
	// after the 200 (truncation, idle timeout, mid-stream error).
	maxStreamRetries = 3
	// maxLoopBackoffHint caps a server retry hint the loop sleeps on between
	// stream retries.
	maxLoopBackoffHint = 60 * time.Second
	// maxRateLimitWait is the longest rate-limit delay worth waiting out. A
	// longer one means the quota is spent, so the loop rotates or gives up.
	// Matches the API layer's cap.
	maxRateLimitWait = 2 * time.Minute
	// defaultRotateCooldown is how long a provider that failed over stays out
	// of rotation when the server gave no hint.
	defaultRotateCooldown = 5 * time.Minute
)

// requestRecovery counts the recoveries spent on the request being sent. It
// resets only after a response drains cleanly, so a stream that connects but
// keeps failing runs out of retries.
type requestRecovery struct {
	stream, toolFlow, prefixLock int
	// rotationAbort is set when the API-layer retry handler gave up on a rate
	// limit expecting the loop to rotate provider. Run clears it before each
	// send.
	rotationAbort bool
}

func (r *requestRecovery) reset() { *r = requestRecovery{} }

// apiCtx returns ctx carrying the loop's injected sleep, so the API layer's
// backoff uses it too.
func (l *Loop) apiCtx(ctx context.Context) context.Context {
	l.mu.RLock()
	sleep := l.cfg.Sleep
	l.mu.RUnlock()
	return api.WithSleep(ctx, sleep)
}

// sleep waits d, returning early with ctx.Err() when ctx is done.
func (l *Loop) sleep(ctx context.Context, d time.Duration) error {
	l.mu.RLock()
	sleep := l.cfg.Sleep
	l.mu.RUnlock()
	if sleep != nil {
		return sleep(ctx, d)
	}
	return api.SleepContext(ctx, d)
}

// shouldFailover reports whether a provider failure warrants switching to the
// next provider in the chain: throttling, overload and server or network
// trouble, or a model the provider does not have.
func shouldFailover(c api.Classified) bool {
	switch c.Class {
	case api.ClassRateLimited, api.ClassTransient:
		return true
	}
	return c.Reason == api.ReasonModelNotFound
}

// failoverState bundles the mutable provider-rotation variables so tryRotate
// can update them in place without needing to return multiple values.
type failoverState struct {
	chain        []settings.ActiveProviderSettings
	rotation     *providerrotation.State
	newClientFor func(settings.ActiveProviderSettings) (*api.Client, error)
	attempts     int

	// Pointers into Run()'s locals — updated on each successful rotation.
	client        **api.Client
	providerKey   *string
	model         *string
	contextWindow *int
	// run is Run's state, which overflow recovery changes. nil in tests that
	// exercise the retry paths alone.
	run *runState
}

// canRotate reports whether another provider could be switched to right now:
// one that is not cooling down from an earlier failure.
func (fs *failoverState) canRotate() bool {
	if fs.rotation == nil || len(fs.chain) <= 1 || fs.attempts >= len(fs.chain) {
		return false
	}
	_, ok := fs.rotation.NextAvailable(fs.chain, *fs.providerKey, time.Now())
	return ok
}

// rotateCooldown is how long the failing provider sits out: the server's
// retry-after, else the limit's reset time, else five minutes.
func rotateCooldown(c api.Classified, now time.Time) time.Time {
	switch {
	case c.RetryAfter > 0:
		return now.Add(c.RetryAfter)
	case c.ResetAt.After(now):
		return c.ResetAt
	}
	return now.Add(defaultRotateCooldown)
}

// tryRotate attempts to rotate to the next available provider when c is a
// failure worth failing over on. Returns true if rotation succeeded (caller
// should continue the loop); false if no rotation is possible or the per-Run
// cap is exhausted.
func (fs *failoverState) tryRotate(c api.Classified, err error, handler func(LoopEvent)) bool {
	if !shouldFailover(c) || !fs.canRotate() {
		return false
	}
	now := time.Now()
	next, ok := fs.rotation.NextAvailable(fs.chain, *fs.providerKey, now)
	if !ok {
		return false
	}
	nc, cerr := fs.newClientFor(next)
	if cerr != nil {
		return false
	}
	if *fs.providerKey != "" {
		fs.rotation.Penalize(*fs.providerKey, rotateCooldown(c, now))
	}
	*fs.client = nc
	*fs.providerKey = settings.ProviderKey(next)
	fs.attempts++
	if next.Model != "" {
		*fs.model = next.Model
	}
	if next.ContextWindow > 0 {
		*fs.contextWindow = next.ContextWindow
	}
	handler(LoopEvent{Type: EventProviderFailover, FailoverErr: err, FailoverProvider: *fs.providerKey})
	return true
}

// retryEvent builds the EventAPIRetry the loop emits for its own retries and
// repairs (the API layer emits its own from the retry handler).
func retryEvent(c api.Classified, err error, attempt, maxAttempts int, delay time.Duration, stream bool) LoopEvent {
	return LoopEvent{
		Type:         EventAPIRetry,
		RetryAttempt: attempt,
		RetryMax:     maxAttempts,
		RetryDelay:   delay,
		RetryAfter:   c.RetryAfter,
		RetryErr:     err,
		RetryReason:  c.Reason,
		RetryStatus:  c.Status,
		RetryStream:  stream,
	}
}

// recoverConnect handles a StreamMessage error: a failure before any response.
// The API layer has already retried what it can, so this only repairs the
// request or switches provider. It returns the history to resend and true when
// the request should be sent again; false means surface err.
func (l *Loop) recoverConnect(ctx context.Context, err error, msgs []api.Message, rec *requestRecovery, fs *failoverState, handler func(LoopEvent)) ([]api.Message, bool) {
	c := api.Classify(err)
	switch c.Class {
	case api.ClassToolUseFlow:
		if rec.toolFlow < maxToolUseRecoveries {
			if recovered, ok := recoverToolUseHistory(msgs); ok {
				rec.toolFlow++
				handler(retryEvent(c, err, rec.toolFlow, maxToolUseRecoveries, 0, false))
				return recovered, true
			}
		}
	case api.ClassPrefixLock:
		// Strict prefix lock: a replayed thinking signature is bound to
		// another conversation (compaction, resume, model swap). Strip from
		// the block the server named and retry; fall back to stripping all
		// thinking. Nothing to strip means the error is surfaced as-is.
		if se := prefixLockRejection(err); se != nil && rec.prefixLock < maxPrefixLockRecoveries {
			rec.prefixLock++
			if healed, scope, ok := healPrefixLock(msgs, se.ThinkingMismatch, rec.prefixLock); ok {
				handler(retryEvent(c, fmt.Errorf("agent: prefix-lock rejection: stripped thinking (%s), retrying: %w", scope, err),
					rec.prefixLock, maxPrefixLockRecoveries, 0, false))
				return healed, true
			}
		}
	case api.ClassContextOverflow:
		if recovered, ok := l.recoverOverflow(ctx, c, msgs, fs.run, handler); ok {
			return recovered, true
		}
	}
	if fs.tryRotate(c, err, handler) {
		rec.stream = 0
		return msgs, true
	}
	// A short rate limit the API layer gave up on only because it expected a
	// rotation is worth one backoff resend when no provider could take over.
	// Otherwise the API layer has spent its own retry budget already.
	if rec.rotationAbort && c.Class == api.ClassRateLimited && c.RetryAfter <= maxRateLimitWait && rec.stream < 1 {
		rec.stream++
		d := api.BackoffDelay(0, c.RetryAfter)
		handler(retryEvent(c, err, rec.stream, 1, d, false))
		if l.sleep(ctx, d) == nil {
			return msgs, true
		}
	}
	return msgs, false
}

// drainOutcome is what to do after a stream failed mid-response.
type drainOutcome int

const (
	drainRetry  drainOutcome = iota // send the request again
	drainFail                       // return the error
	drainCancel                     // the caller's ctx is done; return its error
)

// recoverDrain handles an error from drainStream, a failure after the 200. It
// closes the UI rows of tool calls that never ran, then retries transient
// failures with backoff (the request is resent byte-identical, so the prompt
// cache stays warm), rotates provider, or gives up. msgs gains the response's
// completed text on give-up and on cancel. The returned error is non-nil for
// drainFail and drainCancel.
func (l *Loop) recoverDrain(ctx context.Context, err error, res streamResult, msgs []api.Message, rec *requestRecovery, fs *failoverState, handler func(LoopEvent)) ([]api.Message, drainOutcome, error) {
	closeDanglingTools(handler, res.StartedTools, danglingToolMsg)

	// A user cancel is decided by the caller's context. The idle-timeout error
	// also wraps context.Canceled, so the error alone cannot tell them apart.
	if ctx.Err() != nil {
		if len(res.Blocks) > 0 {
			handler(LoopEvent{Type: EventPartial, PartialBlocks: res.Blocks, PartialErr: err})
		}
		return appendPartial(msgs, res.Blocks), drainCancel, ctx.Err()
	}

	c := api.Classify(err)
	if c.Class == api.ClassContextOverflow {
		if recovered, ok := l.recoverOverflow(ctx, c, msgs, fs.run, handler); ok {
			return recovered, drainRetry, nil
		}
		return appendPartial(msgs, res.Blocks), drainFail, fmt.Errorf("agent: drain: %w", err)
	}

	// Unknown counts as retryable: the server already answered 200, so an
	// unrecognised break in the body is most likely the connection.
	retryable := c.Class == api.ClassTransient || c.Class == api.ClassRateLimited || c.Class == api.ClassUnknown
	spent := c.Class == api.ClassRateLimited && c.RetryAfter > maxRateLimitWait
	if retryable && !spent && rec.stream < maxStreamRetries {
		rec.stream++
		d := api.BackoffDelay(rec.stream-1, min(c.RetryAfter, maxLoopBackoffHint))
		handler(retryEvent(c, err, rec.stream, maxStreamRetries, d, true))
		if serr := l.sleep(ctx, d); serr != nil {
			return msgs, drainCancel, serr
		}
		return msgs, drainRetry, nil
	}
	if fs.tryRotate(c, err, handler) {
		rec.stream = 0
		return msgs, drainRetry, nil
	}
	return appendPartial(msgs, res.Blocks), drainFail, fmt.Errorf("agent: drain: %w", err)
}

// danglingToolMsg is the result shown on a tool row whose call was interrupted
// before it could run.
const danglingToolMsg = "The response was interrupted before this tool call finished."

// DanglingToolMessage is danglingToolMsg for callers that tell such a closing
// result from a real one.
const DanglingToolMessage = danglingToolMsg

// closeDanglingTools emits an error EventToolResult for each tool row the UI
// opened that will never run, so the row does not stay "running…".
func closeDanglingTools(handler func(LoopEvent), started []startedTool, msg string) {
	for _, t := range started {
		handler(LoopEvent{
			Type:       EventToolResult,
			ToolID:     t.ID,
			ToolName:   t.Name,
			ResultText: msg,
			IsError:    true,
		})
	}
}

// appendPartial returns msgs plus an assistant message holding the text
// blocks of an interrupted response, or msgs unchanged when it had none. Only
// text is kept: a tool_use that never ran would be an orphan, and a thinking
// block cut off mid-stream has no signature to replay.
func appendPartial(msgs []api.Message, blocks []api.ContentBlock) []api.Message {
	var text []api.ContentBlock
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			text = append(text, b)
		}
	}
	if len(text) == 0 {
		return msgs
	}
	return append(msgs, api.Message{Role: "assistant", Content: text})
}
