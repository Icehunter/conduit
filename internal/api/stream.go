package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/icehunter/conduit/internal/sse"
)

// Stream is an open SSE connection. Call Next() until io.EOF; always Close().
type Stream struct {
	body   io.ReadCloser
	parser *sse.Parser
	// ResponseHeader holds the HTTP response headers from the initial connection.
	// Use this to read rate-limit headers (anthropic-ratelimit-*).
	ResponseHeader http.Header
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

// StreamMessage opens an event-stream connection using the configured provider
// wire dialect. Headers and 401 retry mirror CreateMessage. The caller is
// responsible for reading until io.EOF and calling Close.
//
// Reference: decoded/0158.js (create), 0137.js (SSE consumption), 4500.js
// (retry semantics). The wire is HTTP+SSE — no protobuf/binary framing.
func (c *Client) StreamMessage(ctx context.Context, req *MessageRequest) (*Stream, error) {
	return c.transport.StreamMessage(ctx, c, req)
}

// StreamMessage opens an Anthropic Messages API SSE stream at
// /v1/messages?beta=true.
func (anthropicMessagesTransport) StreamMessage(ctx context.Context, c *Client, req *MessageRequest) (*Stream, error) {
	// Force stream:true so a forgetful caller doesn't get a non-stream JSON
	// response back from the server (which would fail to parse as SSE and
	// give a confusing error).
	req2 := *sanitizeAnthropicRequest(req, c.cfg)
	req2.Stream = true

	body, err := json.Marshal(&req2)
	if err != nil {
		return nil, fmt.Errorf("api: marshal stream request: %w", err)
	}

	resp, err := c.doWithRetryAndAuth(ctx, func() (*http.Response, error) {
		return c.doStream(ctx, body, req2.Model)
	})
	if err != nil {
		return nil, err
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
		body:           resp.Body,
		parser:         sse.NewParser(resp.Body),
		ResponseHeader: resp.Header,
	}, nil
}

// doStream is the streaming counterpart to do(). It uses the same headers
// but does not buffer the response body.
func (c *Client) doStream(ctx context.Context, body []byte, model string) (*http.Response, error) {
	url := strings.TrimRight(c.cfg.BaseURL, "/") + "/v1/messages?beta=true"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("api: build stream request: %w", err)
	}
	c.applyHeaders(httpReq.Header, model)

	if os.Getenv("CLAUDE_GO_DEBUG_HTTP") == "1" {
		fmt.Fprintf(os.Stderr, "\n[CLAUDE_GO_DEBUG_HTTP] >>> POST %s\n", url)
		keys := make([]string, 0, len(httpReq.Header))
		for k := range httpReq.Header {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := httpReq.Header.Get(k)
			if isCredentialHeader(k) {
				v = "(redacted)"
			}
			fmt.Fprintf(os.Stderr, "  %s: %s\n", k, v)
		}
		fmt.Fprintf(os.Stderr, "  body bytes: %d\n", len(body))
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("api: send stream: %w", err)
	}
	return resp, nil
}
