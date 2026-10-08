// Package iox provides small I/O utilities not in the standard library.
package iox

import (
	"errors"
	"io"
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
