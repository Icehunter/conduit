package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/icehunter/conduit/internal/sse"
)

// Stream is an open SSE connection. Call Next() until io.EOF; always Close().
type Stream struct {
	body   io.ReadCloser
	parser *sse.Parser
}

// Next returns the next non-skipped event, or io.EOF when the stream ends.
// Sentinel: a *sse.Error means the API surfaced an error event mid-stream.
func (s *Stream) Next() (sse.Event, error) {
	return s.parser.Next()
}

// Close releases the underlying HTTP connection. Safe to call multiple times.
func (s *Stream) Close() error {
	if s.body == nil {
		return nil
	}
	err := s.body.Close()
	s.body = nil
	return err
}

// StreamMessage opens an event-stream connection to /v1/messages?beta=true.
// Headers and 401 retry mirror CreateMessage. The caller is responsible for
// reading until io.EOF and calling Close.
//
// Reference: decoded/0158.js (create), 0137.js (SSE consumption), 4500.js
// (retry semantics). The wire is HTTP+SSE — no protobuf/binary framing.
func (c *Client) StreamMessage(ctx context.Context, req *MessageRequest) (*Stream, error) {
	// Force stream:true so a forgetful caller doesn't get a non-stream JSON
	// response back from the server (which would fail to parse as SSE and
	// give a confusing error).
	req2 := *req
	req2.Stream = true

	body, err := json.Marshal(&req2)
	if err != nil {
		return nil, fmt.Errorf("api: marshal stream request: %w", err)
	}

	resp, err := c.doStream(ctx, body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized && c.cfg.OnAuth401 != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if err := c.cfg.OnAuth401(ctx); err != nil {
			return nil, fmt.Errorf("api: refresh on 401: %w", err)
		}
		resp, err = c.doStream(ctx, body)
		if err != nil {
			return nil, err
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := c.decodeError(resp)
		_ = resp.Body.Close()
		return nil, err
	}

	if !strings.Contains(resp.Header.Get("Content-Type"), "event-stream") {
		// Server didn't honor stream:true. Treat as error rather than
		// silently swallowing JSON as SSE garbage.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("api: stream: server returned non-SSE Content-Type=%q body=%s",
			resp.Header.Get("Content-Type"), strings.TrimSpace(string(raw)))
	}

	return &Stream{
		body:   resp.Body,
		parser: sse.NewParser(resp.Body),
	}, nil
}

// doStream is the streaming counterpart to do(). It uses the same headers
// but does not buffer the response body.
func (c *Client) doStream(ctx context.Context, body []byte) (*http.Response, error) {
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/v1/messages?beta=true"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("api: build stream request: %w", err)
	}
	c.applyHeaders(httpReq.Header)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("api: send stream: %w", err)
	}
	return resp, nil
}
