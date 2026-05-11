package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrUnauthorized is returned by HTTP/SSE client calls when the server
// responds with HTTP 401. Callers — typically Manager — branch on this
// to mark the server as StatusNeedsAuth and surface McpAuthTool.
var ErrUnauthorized = errors.New("mcp: unauthorized (401)")

// Client is the interface both stdio and HTTP/SSE clients implement.
type Client interface {
	// Initialize sends the MCP initialize handshake and returns the server's
	// instructions string (empty if none) for injection into the system prompt.
	Initialize(ctx context.Context) (string, error)
	// ListTools fetches the server's tool list.
	ListTools(ctx context.Context) ([]ToolDef, error)
	// CallTool invokes a tool and returns its result.
	CallTool(ctx context.Context, name string, input json.RawMessage) (CallResult, error)
	// ListResources fetches the server's resource list (MCP resources/list).
	ListResources(ctx context.Context) ([]ResourceDef, error)
	// ReadResource reads the contents of one resource (MCP resources/read).
	ReadResource(ctx context.Context, uri string) ([]ResourceContent, error)
	// Close shuts down the transport.
	Close() error
}

// ---- stdio transport -------------------------------------------------------

// stdioClient speaks JSON-RPC over stdin/stdout of a subprocess.
type stdioClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner

	mu      sync.Mutex
	nextID  atomic.Int64
	pending map[int64]chan *jsonrpcResponse
	done    chan struct{}
}

// NewStdioClient creates a Client that runs cmd with the given args and env,
// communicating over stdio. The provided ctx controls the subprocess lifetime —
// cancelling it kills the child process.
func NewStdioClient(ctx context.Context, command string, args []string, env map[string]string) (Client, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	if len(env) > 0 {
		cmd.Env = mergedStdioEnv(env)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: stdin pipe: %w", err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mcp stdio: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp stdio: start %q: %w", command, err)
	}

	sc := bufio.NewScanner(stdoutPipe)
	// Default scanner cap is 64 KB — real MCP servers (Playwright, Atlassian)
	// emit tool schemas > 64 KB. Grow limit to 8 MiB, matching the SSE parser.
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
	c := &stdioClient{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  sc,
		pending: make(map[int64]chan *jsonrpcResponse),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

func mergedStdioEnv(env map[string]string) []string {
	pairs := make([]string, 0, len(os.Environ())+len(env))
	pairs = append(pairs, os.Environ()...)
	for k, v := range env {
		pairs = append(pairs, k+"="+v)
	}
	return pairs
}

func (c *stdioClient) readLoop() {
	defer close(c.done)
	for c.stdout.Scan() {
		line := c.stdout.Bytes()
		var resp jsonrpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}
		c.mu.Lock()
		ch, ok := c.pending[int64(resp.ID)]
		if ok {
			delete(c.pending, int64(resp.ID))
		}
		c.mu.Unlock()
		if ok {
			ch <- &resp
		}
	}
}

func (c *stdioClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)

	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		rawParams = b
	}

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      int(id),
		Method:  method,
		Params:  rawParams,
	}
	line, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	line = append(line, '\n')

	ch := make(chan *jsonrpcResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	if _, err := c.stdin.Write(line); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp stdio: write: %w", err)
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-c.done:
		return nil, fmt.Errorf("mcp stdio: server exited")
	}
}

func (c *stdioClient) Initialize(ctx context.Context) (string, error) {
	params := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "conduit",
			"version": "1.0",
		},
	}
	raw, err := c.call(ctx, "initialize", params)
	if err != nil {
		return "", fmt.Errorf("mcp initialize: %w", err)
	}
	// Send initialized notification (no response expected).
	notif := map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}
	b, _ := json.Marshal(notif)
	b = append(b, '\n')
	_, _ = c.stdin.Write(b)
	// Extract instructions from the initialize result if present.
	var result struct {
		Instructions string `json:"instructions"`
	}
	_ = json.Unmarshal(raw, &result)
	return result.Instructions, nil
}

func (c *stdioClient) ListTools(ctx context.Context) ([]ToolDef, error) {
	raw, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp tools/list decode: %w", err)
	}
	return result.Tools, nil
}

func (c *stdioClient) CallTool(ctx context.Context, name string, input json.RawMessage) (CallResult, error) {
	var inputMap map[string]any
	if len(input) > 0 {
		_ = json.Unmarshal(input, &inputMap)
	}
	params := map[string]any{
		"name":      name,
		"arguments": inputMap,
	}
	raw, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return CallResult{IsError: true, Content: []ContentBlock{{Type: "text", Text: err.Error()}}}, nil
	}
	var result CallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return CallResult{IsError: true, Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("mcp tools/call decode: %v", err)}}}, nil
	}
	return result, nil
}

func (c *stdioClient) ListResources(ctx context.Context) ([]ResourceDef, error) {
	raw, err := c.call(ctx, "resources/list", nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Resources []ResourceDef `json:"resources"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp resources/list decode: %w", err)
	}
	return result.Resources, nil
}

func (c *stdioClient) ReadResource(ctx context.Context, uri string) ([]ResourceContent, error) {
	raw, err := c.call(ctx, "resources/read", map[string]any{"uri": uri})
	if err != nil {
		return nil, err
	}
	var result struct {
		Contents []ResourceContent `json:"contents"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp resources/read decode: %w", err)
	}
	return result.Contents, nil
}

func (c *stdioClient) Close() error {
	_ = c.stdin.Close()
	waitDone := make(chan error, 1)
	go func() { waitDone <- c.cmd.Wait() }()
	select {
	case err := <-waitDone:
		return err
	case <-time.After(2 * time.Second):
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		return <-waitDone
	}
}

// ---- HTTP/SSE transport ---------------------------------------------------

// httpClient speaks JSON-RPC over HTTP POST (Streamable HTTP transport, MCP 2024-11-05).
type httpClient struct {
	url     string
	headers map[string]string
	http    *http.Client
	nextID  atomic.Int64

	sessionMu sync.Mutex
	sessionID string // Mcp-Session-Id from Initialize response; sent on all subsequent requests
}

// NewHTTPClient creates a Client that sends JSON-RPC requests to url via HTTP POST.
func NewHTTPClient(url string, headers map[string]string) Client {
	return &httpClient{
		url:     url,
		headers: headers,
		// 60-second timeout matches internal/mcp/oauth.go and prevents a hung
		// or unresponsive MCP server from blocking the agent indefinitely.
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

// call is the standard entry point for all calls after Initialize.
// It injects the session ID (if any) and discards the response headers.
func (c *httpClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	raw, _, err := c.callWithHeaders(ctx, method, params)
	return raw, err
}

// callWithHeaders performs one JSON-RPC request and returns the result along
// with the Mcp-Session-Id response header value (empty string if absent).
// Used by Initialize so it can capture the session ID on first contact.
func (c *httpClient) callWithHeaders(ctx context.Context, method string, params any) (json.RawMessage, string, error) {
	id := c.nextID.Add(1)

	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, "", err
		}
		rawParams = b
	}

	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      int(id),
		Method:  method,
		Params:  rawParams,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}
	// Include the session ID on all calls after Initialize.
	c.sessionMu.Lock()
	sid := c.sessionID
	c.sessionMu.Unlock()
	if sid != "" {
		httpReq.Header.Set("Mcp-Session-Id", sid)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, "", fmt.Errorf("mcp http: %w", err)
	}
	defer resp.Body.Close()

	respSessionID := resp.Header.Get("Mcp-Session-Id")

	// Auth-required: 401 is the canonical signal but several MCPs return
	// 403 for missing/invalid bearer (especially when the server is
	// behind a proxy or CDN that intercepts the auth check). Treat both
	// as ErrUnauthorized so the caller can drive OAuth.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, "", ErrUnauthorized
	}

	// SSE detection: some MCPs ship `data: {...}` framing under content
	// types like "text/event-stream; charset=utf-8" or "application/
	// vnd.mcp+sse" — match by substring rather than HasPrefix("text/
	// event-stream") so non-canonical content types still flow through
	// the SSE reader.
	ct := resp.Header.Get("Content-Type")
	if strings.Contains(ct, "event-stream") {
		raw, err := c.readSSEResponse(resp.Body)
		return raw, respSessionID, err
	}

	// Non-2xx with a non-JSON body: surface the status + a body excerpt
	// so the user can see what the server actually said. Without this,
	// every "the server returned HTML" case ends up as the cryptic
	// "invalid character '<' looking for beginning of value".
	if resp.StatusCode/100 != 2 {
		excerpt := readBodyExcerpt(resp.Body, 200)
		return nil, "", fmt.Errorf("mcp http: server returned %d %s%s",
			resp.StatusCode, http.StatusText(resp.StatusCode), excerpt)
	}

	// Buffer up to 64KB so on decode failure we can include a body
	// excerpt — JSON decoder errors that just say "invalid character 'b'"
	// are unhelpful when the response is HTML or plain text.
	buf, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var rpcResp jsonrpcResponse
	if err := json.Unmarshal(buf, &rpcResp); err != nil {
		return nil, "", fmt.Errorf("mcp http decode: %w%s", err, formatBodyExcerpt(buf, 200))
	}
	if rpcResp.Error != nil {
		return nil, "", rpcResp.Error
	}
	return rpcResp.Result, respSessionID, nil
}

// readBodyExcerpt drains up to max bytes from r and returns a "; body: …"
// suffix suitable for appending to an error message, or "" if the body is
// empty or unreadable. Trims whitespace and collapses internal newlines.
func readBodyExcerpt(r io.Reader, max int) string {
	buf, _ := io.ReadAll(io.LimitReader(r, int64(max)+1))
	return formatBodyExcerpt(buf, max)
}

// formatBodyExcerpt formats a buffered body for inclusion in an error
// message. Returns "" when the body is empty.
func formatBodyExcerpt(buf []byte, max int) string {
	s := strings.TrimSpace(string(buf))
	if s == "" {
		return ""
	}
	// Collapse newlines so the error stays one line in panel output.
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", "")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return "; body: " + s
}

// readSSEResponse reads a single JSON-RPC response from an SSE stream.
func (c *httpClient) readSSEResponse(body io.Reader) (json.RawMessage, error) {
	scanner := bufio.NewScanner(body)
	// Default scanner cap is 64 KB — MCP servers can emit tool schemas > 64 KB.
	// Grow limit to 8 MiB, matching the stdio transport and internal/sse/parser.go.
	scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var rpcResp jsonrpcResponse
		if err := json.Unmarshal([]byte(data), &rpcResp); err != nil {
			continue
		}
		if rpcResp.Error != nil {
			return nil, rpcResp.Error
		}
		return rpcResp.Result, nil
	}
	return nil, fmt.Errorf("mcp sse: no response data")
}

func (c *httpClient) Initialize(ctx context.Context) (string, error) {
	params := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "conduit",
			"version": "1.0",
		},
	}
	raw, sessionID, err := c.callWithHeaders(ctx, "initialize", params)
	if err != nil {
		return "", fmt.Errorf("mcp initialize: %w", err)
	}
	// Persist the session ID so all subsequent calls include it.
	if sessionID != "" {
		c.sessionMu.Lock()
		c.sessionID = sessionID
		c.sessionMu.Unlock()
	}
	var result struct {
		Instructions string `json:"instructions"`
	}
	_ = json.Unmarshal(raw, &result)
	return result.Instructions, nil
}

func (c *httpClient) ListTools(ctx context.Context) ([]ToolDef, error) {
	raw, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp tools/list decode: %w", err)
	}
	return result.Tools, nil
}

func (c *httpClient) CallTool(ctx context.Context, name string, input json.RawMessage) (CallResult, error) {
	var inputMap map[string]any
	if len(input) > 0 {
		_ = json.Unmarshal(input, &inputMap)
	}
	params := map[string]any{
		"name":      name,
		"arguments": inputMap,
	}
	raw, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return CallResult{IsError: true, Content: []ContentBlock{{Type: "text", Text: err.Error()}}}, nil
	}
	var result CallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return CallResult{IsError: true, Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("mcp tools/call decode: %v", err)}}}, nil
	}
	return result, nil
}

func (c *httpClient) ListResources(ctx context.Context) ([]ResourceDef, error) {
	raw, err := c.call(ctx, "resources/list", nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Resources []ResourceDef `json:"resources"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp resources/list decode: %w", err)
	}
	return result.Resources, nil
}

func (c *httpClient) ReadResource(ctx context.Context, uri string) ([]ResourceContent, error) {
	raw, err := c.call(ctx, "resources/read", map[string]any{"uri": uri})
	if err != nil {
		return nil, err
	}
	var result struct {
		Contents []ResourceContent `json:"contents"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp resources/read decode: %w", err)
	}
	return result.Contents, nil
}

func (c *httpClient) Close() error { return nil }
