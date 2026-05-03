package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

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
// communicating over stdio.
func NewStdioClient(command string, args []string, env map[string]string) (Client, error) {
	cmd := exec.Command(command, args...)
	if len(env) > 0 {
		pairs := make([]string, 0, len(env))
		for k, v := range env {
			pairs = append(pairs, k+"="+v)
		}
		cmd.Env = append(cmd.Env, pairs...)
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

	c := &stdioClient{
		cmd:     cmd,
		stdin:   stdin,
		stdout:  bufio.NewScanner(stdoutPipe),
		pending: make(map[int64]chan *jsonrpcResponse),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
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
	return c.cmd.Wait()
}

// ---- HTTP/SSE transport ---------------------------------------------------

// httpClient speaks JSON-RPC over HTTP POST (Streamable HTTP transport, MCP 2024-11-05).
type httpClient struct {
	url     string
	headers map[string]string
	http    *http.Client
	nextID  atomic.Int64
}

// NewHTTPClient creates a Client that sends JSON-RPC requests to url via HTTP POST.
func NewHTTPClient(url string, headers map[string]string) Client {
	return &httpClient{
		url:     url,
		headers: headers,
		http:    &http.Client{},
	}
}

func (c *httpClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
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
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("mcp http: %w", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		return c.readSSEResponse(resp.Body)
	}

	var rpcResp jsonrpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("mcp http decode: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}
	return rpcResp.Result, nil
}

// readSSEResponse reads a single JSON-RPC response from an SSE stream.
func (c *httpClient) readSSEResponse(body io.Reader) (json.RawMessage, error) {
	scanner := bufio.NewScanner(body)
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
	raw, err := c.call(ctx, "initialize", params)
	if err != nil {
		return "", fmt.Errorf("mcp initialize: %w", err)
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

