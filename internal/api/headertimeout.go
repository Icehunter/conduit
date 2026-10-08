package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// defaultResponseHeaderTimeout bounds the time from sending a request to
// receiving the first response headers. It is distinct from streamIdleTimeout,
// which only starts once the stream is open. A var so tests can shorten it.
var defaultResponseHeaderTimeout = 5 * time.Minute

// maxHeaderTimeoutRetries bounds resends after a header timeout: each one
// costs a full timeout, and on a local server it restarts the prefill.
const maxHeaderTimeoutRetries = 1

// ErrResponseHeaderTimeout means the server sent no response headers within
// Config.ResponseHeaderTimeout. Classify treats it as transient. Unlike
// ErrStreamIdleTimeout its chain does not hold context.Canceled.
var ErrResponseHeaderTimeout = errors.New("api: response header timeout")

// responseHeaderTimeout is the effective header timeout: Config's value when
// positive, 0 (disabled) when negative, and when unset the default, except for
// a local or LAN server, which may spend longer than that prefilling a large
// prompt before it sends headers.
func (c *Client) responseHeaderTimeout() time.Duration {
	switch d := c.cfg.ResponseHeaderTimeout; {
	case d < 0:
		return 0
	case d == 0:
		if isLocalBaseURL(c.cfg.BaseURL) {
			return 0
		}
		return defaultResponseHeaderTimeout
	default:
		return d
	}
}

// doWithHeaderTimeout sends req and fails with ErrResponseHeaderTimeout when no
// headers arrive in time. The timer only covers the wait for headers: once Do
// returns it is stopped, and the body is governed by the caller's own limits.
func (c *Client) doWithHeaderTimeout(req *http.Request) (*http.Response, error) {
	timeout := c.responseHeaderTimeout()
	if timeout <= 0 {
		return c.http.Do(req)
	}
	ctx, cancel := context.WithCancel(req.Context())
	var fired atomic.Bool
	timer := time.AfterFunc(timeout, func() {
		fired.Store(true)
		cancel()
	})
	resp, err := c.http.Do(req.WithContext(ctx))
	timer.Stop()
	if err != nil {
		cancel()
		if fired.Load() && req.Context().Err() == nil {
			return nil, ErrResponseHeaderTimeout
		}
		return nil, err
	}
	if fired.Load() {
		// The timer fired just as headers arrived; the body is already
		// cancelled, so report the timeout rather than a broken stream.
		_ = resp.Body.Close()
		cancel()
		return nil, ErrResponseHeaderTimeout
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelOnClose releases the per-request context when the body is closed.
type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnClose) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}
