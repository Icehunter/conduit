// Package iox provides small I/O utilities not in the standard library.
package iox

import (
	"errors"
	"io"
	"sync/atomic"
)

// ErrLimitReached is returned by LimitWriter once the byte limit is exceeded.
var ErrLimitReached = errors.New("iox: write limit reached")

// LimitWriter wraps an io.Writer and tracks the total bytes written.
// Once the cumulative write count exceeds Limit, all subsequent Write calls
// return (0, ErrLimitReached) without writing any bytes.
//
// LimitWriter is NOT safe for concurrent use; use from a single goroutine.
type LimitWriter struct {
	W       io.Writer
	Limit   int64
	written int64
}

// Write implements io.Writer.
func (l *LimitWriter) Write(p []byte) (int, error) {
	if l.written >= l.Limit {
		return 0, ErrLimitReached
	}
	remaining := l.Limit - l.written
	if int64(len(p)) > remaining {
		// Partial write up to the limit.
		n, err := l.W.Write(p[:remaining])
		l.written += int64(n)
		if err != nil {
			return n, err
		}
		return n, ErrLimitReached
	}
	n, err := l.W.Write(p)
	l.written += int64(n)
	return n, err
}

// Written returns the total bytes written so far.
func (l *LimitWriter) Written() int64 { return l.written }

// AtomicLimitWriter is a concurrent-safe version for use with multiple
// goroutines writing to the same buffer (e.g. cmd.Stdout and cmd.Stderr both
// pointed at the same writer). Once the limit is reached, Overflow is set to
// true atomically. If OnOverflow is non-nil it is called exactly once on the
// first overflow — use it to cancel the subprocess context.
type AtomicLimitWriter struct {
	W          io.Writer
	Limit      int64
	OnOverflow func() // called once on first overflow; may be nil
	written    atomic.Int64
	Overflow   atomic.Bool
}

// Write implements io.Writer.
func (a *AtomicLimitWriter) Write(p []byte) (int, error) {
	if a.Overflow.Load() {
		return 0, ErrLimitReached
	}
	newTotal := a.written.Add(int64(len(p)))
	if newTotal > a.Limit {
		if a.Overflow.CompareAndSwap(false, true) && a.OnOverflow != nil {
			a.OnOverflow()
		}
		return 0, ErrLimitReached
	}
	return a.W.Write(p)
}
