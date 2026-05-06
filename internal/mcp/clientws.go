package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

// wsClient speaks JSON-RPC over a WebSocket connection.
// Mirrors utils/mcpWebSocketTransport.ts — full-duplex, request/response
// matched by JSON-RPC ID, read loop runs in a goroutine.
type wsClient struct {
	url     string
	headers map[string]string

	mu      sync.Mutex
	conn    *websocket.Conn
	nextID  atomic.Int64
	pending map[int64]chan *jsonrpcResponse
	done    chan struct{}
}

// NewWebSocketClient creates a Client that communicates via WebSocket.
func NewWebSocketClient(url string, headers map[string]string) Client {
	return &wsClient{
		url:     url,
		headers: headers,
		pending: make(map[int64]chan *jsonrpcResponse),
		done:    make(chan struct{}),
	}
}

func (c *wsClient) dial(ctx context.Context) error {
	opts := &websocket.DialOptions{HTTPHeader: make(http.Header)}
	for k, v := range c.headers {
		opts.HTTPHeader.Set(k, v)
	}
	conn, _, err := websocket.Dial(ctx, c.url, opts) //nolint:bodyclose
	if err != nil {
		return fmt.Errorf("mcp ws dial %s: %w", c.url, err)
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	go c.readLoop()
	return nil
}

func (c *wsClient) readLoop() {
	defer func() {
		close(c.done)
		c.mu.Lock()
		for _, ch := range c.pending {
			close(ch)
		}
		c.pending = make(map[int64]chan *jsonrpcResponse)
		c.mu.Unlock()
	}()
	for {
		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()
		if conn == nil {
			return
		}
		_, data, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		var resp jsonrpcResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			continue
		}
		c.mu.Lock()
		if ch, ok := c.pending[int64(resp.ID)]; ok {
			ch <- &resp
			delete(c.pending, int64(resp.ID))
		}
		c.mu.Unlock()
	}
}

func (c *wsClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.conn == nil {
		c.mu.Unlock()
		if err := c.dial(ctx); err != nil {
			return nil, err
		}
		c.mu.Lock()
	}
	id := c.nextID.Add(1)
	ch := make(chan *jsonrpcResponse, 1)
	c.pending[id] = ch
	conn := c.conn
	c.mu.Unlock()

	var rawParams json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		rawParams = b
	}
	req := jsonrpcRequest{JSONRPC: "2.0", ID: int(id), Method: method, Params: rawParams}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := conn.Write(ctx, websocket.MessageText, body); err != nil {
		return nil, fmt.Errorf("mcp ws write: %w", err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("mcp ws: connection closed")
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *wsClient) Initialize(ctx context.Context) (string, error) {
	raw, err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "conduit", "version": "1.0"},
	})
	if err != nil {
		return "", err
	}
	var result struct {
		Instructions string `json:"instructions"`
	}
	_ = json.Unmarshal(raw, &result)
	return result.Instructions, nil
}

func (c *wsClient) ListTools(ctx context.Context) ([]ToolDef, error) {
	raw, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Tools []ToolDef `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp ws tools/list: %w", err)
	}
	return result.Tools, nil
}

func (c *wsClient) CallTool(ctx context.Context, name string, input json.RawMessage) (CallResult, error) {
	var inputMap map[string]any
	if len(input) > 0 {
		_ = json.Unmarshal(input, &inputMap)
	}
	raw, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": inputMap})
	if err != nil {
		return CallResult{IsError: true, Content: []ContentBlock{{Type: "text", Text: err.Error()}}}, nil
	}
	var result CallResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return CallResult{IsError: true, Content: []ContentBlock{{Type: "text", Text: fmt.Sprintf("mcp ws tools/call: %v", err)}}}, nil
	}
	return result, nil
}

func (c *wsClient) ListResources(ctx context.Context) ([]ResourceDef, error) {
	raw, err := c.call(ctx, "resources/list", nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Resources []ResourceDef `json:"resources"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp ws resources/list: %w", err)
	}
	return result.Resources, nil
}

func (c *wsClient) ReadResource(ctx context.Context, uri string) ([]ResourceContent, error) {
	raw, err := c.call(ctx, "resources/read", map[string]any{"uri": uri})
	if err != nil {
		return nil, err
	}
	var result struct {
		Contents []ResourceContent `json:"contents"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("mcp ws resources/read: %w", err)
	}
	return result.Contents, nil
}

func (c *wsClient) Close() error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn != nil {
		return conn.Close(websocket.StatusNormalClosure, "done")
	}
	return nil
}
