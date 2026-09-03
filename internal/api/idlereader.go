package api

import (
	"context"
	"io"
	"sync/atomic"
	"time"
)

// streamIdleTimeout bounds how long a streaming read may go with zero bytes
// received before the connection is considered dead. Deliberately distinct
// from nonStreamingTimeout: a slow-but-alive stream can legitimately run for
// minutes, but any bytes at all (including the API's periodic SSE `ping`
// keepalives) reset this clock, so only a truly silent connection trips it.
// Mirrors nonStreamingTimeout's value for consistency, not a new arbitrary
// number. A var (not const) so tests can shorten it rather than waiting 5
// real minutes.
var streamIdleTimeout = 5 * time.Minute

// idleTimeoutReadCloser wraps an io.ReadCloser and calls cancel if no Read
// makes progress within timeout. Cancelling the context that the underlying
// HTTP request was built with is the only way to unblock an in-flight,
// already-blocked Read — Go's transport tears down the connection when that
// context is cancelled, regardless of when the read started blocking.
//
// Not safe for concurrent use; one reader per stream (matches sse.Parser's
// own concurrency contract).
type idleTimeoutReadCloser struct {
	r        io.ReadCloser
	timeout  time.Duration
	timer    *time.Timer
	timedOut atomic.Bool
}

// newIdleTimeoutReadCloser starts the idle timer immediately; call Close to
// stop it once the stream is done (Read progress also keeps resetting it).
func newIdleTimeoutReadCloser(r io.ReadCloser, timeout time.Duration, cancel context.CancelFunc) *idleTimeoutReadCloser {
	ir := &idleTimeoutReadCloser{r: r, timeout: timeout}
	ir.timer = time.AfterFunc(timeout, func() {
		ir.timedOut.Store(true)
		cancel()
	})
	return ir
}

func (ir *idleTimeoutReadCloser) Read(p []byte) (int, error) {
	n, err := ir.r.Read(p)
	if n > 0 {
		ir.timer.Reset(ir.timeout)
	}
	return n, err
}

func (ir *idleTimeoutReadCloser) Close() error {
	ir.timer.Stop()
	return ir.r.Close()
}

// TimedOut reports whether the idle timer fired (as opposed to the stream
// context being cancelled for some other reason, e.g. normal turn-end or a
// user interrupt).
func (ir *idleTimeoutReadCloser) TimedOut() bool {
	return ir.timedOut.Load()
}
