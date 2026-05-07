package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Client wraps a language server subprocess and speaks JSON-RPC 2.0 over
// its stdin/stdout using the Content-Length framing defined by the LSP spec.
type Client struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	mu      sync.Mutex // guards writes to stdin
	nextID  atomic.Int32
	pending sync.Map      // map[int]chan rpcResponse
	diags   sync.Map      // map[string][]Diagnostic (URI → diagnostics cache)
	done    chan struct{} // closed when the read loop exits
}

// NewClient spawns the language server at cmd+args, performs the LSP
// initialize/initialized handshake, and returns a ready Client.
func NewClient(ctx context.Context, cmd string, args ...string) (*Client, error) {
	c := exec.CommandContext(ctx, cmd, args...)

	stdin, err := c.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: stdin pipe: %w", err)
	}
	stdout, err := c.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: stdout pipe: %w", err)
	}
	c.Stderr = os.Stderr

	if err := c.Start(); err != nil {
		return nil, fmt.Errorf("lsp: start %q: %w", cmd, err)
	}

	cl := &Client{
		cmd:   c,
		stdin: stdin,
		done:  make(chan struct{}),
	}

	go cl.readLoop(stdout)

	if err := cl.initialize(ctx); err != nil {
		_ = c.Process.Kill()
		return nil, err
	}
	return cl, nil
}

// NewClientFromPipes creates a Client using caller-supplied stdin/stdout pipes.
// This is useful for testing with an in-process mock server.
// It performs the same initialize/initialized handshake as NewClient.
func NewClientFromPipes(ctx context.Context, stdin io.WriteCloser, stdout io.ReadCloser) (*Client, error) {
	cl := &Client{
		stdin: stdin,
		done:  make(chan struct{}),
	}
	go cl.readLoop(stdout)

	if err := cl.initialize(ctx); err != nil {
		return nil, err
	}
	return cl, nil
}

// initialize sends the LSP initialize/initialized handshake.
func (c *Client) initialize(ctx context.Context) error {
	cwd, _ := os.Getwd()
	rootURI := "file://" + cwd

	initParams := InitializeParams{
		ProcessID: pid(),
		RootURI:   rootURI,
		Capabilities: map[string]any{
			"textDocument": map[string]any{
				"hover": map[string]any{
					"contentFormat": []string{"markdown", "plaintext"},
				},
				"publishDiagnostics": map[string]any{},
			},
		},
	}

	raw, err := json.Marshal(initParams)
	if err != nil {
		return fmt.Errorf("lsp: marshal initialize: %w", err)
	}

	if _, err := c.Request(ctx, "initialize", raw); err != nil {
		return fmt.Errorf("lsp: initialize: %w", err)
	}

	// Send initialized notification (fire-and-forget).
	if err := c.Notify("initialized", json.RawMessage("{}")); err != nil {
		return fmt.Errorf("lsp: initialized notify: %w", err)
	}
	return nil
}

// Request sends a JSON-RPC request and blocks until the server responds.
func (c *Client) Request(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	id := int(c.nextID.Add(1))
	ch := make(chan rpcResponse, 1)
	c.pending.Store(id, ch)
	defer c.pending.Delete(id)

	if err := c.send(id, method, params); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, fmt.Errorf("lsp: server exited")
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	}
}

// Notify sends a JSON-RPC notification (no ID, no response expected).
func (c *Client) Notify(method string, params json.RawMessage) error {
	return c.send(0, method, params)
}

// Diagnostics returns the most recently cached diagnostics for uri.
func (c *Client) Diagnostics(uri string) []Diagnostic {
	val, ok := c.diags.Load(uri)
	if !ok {
		return nil
	}
	return val.([]Diagnostic)
}

// StoreDiagnostics manually sets the diagnostics cache for a URI.
// This is primarily useful in tests to simulate server push notifications.
func (c *Client) StoreDiagnostics(uri string, diags []Diagnostic) {
	c.diags.Store(uri, diags)
}

// Close sends the LSP shutdown + exit sequence and waits for the process.
func (c *Client) Close() error {
	ctx := context.Background()
	_, _ = c.Request(ctx, "shutdown", json.RawMessage("null"))
	_ = c.Notify("exit", json.RawMessage("null"))
	_ = c.stdin.Close()
	if c.cmd != nil {
		return c.cmd.Wait()
	}
	return nil
}

// send writes a single JSON-RPC message (request or notification) to stdin.
// id == 0 → notification (no "id" field emitted).
func (c *Client) send(id int, method string, params json.RawMessage) error {
	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	if id != 0 {
		req.ID = &id
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("lsp: marshal %s: %w", method, err)
	}

	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))

	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := io.WriteString(c.stdin, header); err != nil {
		return fmt.Errorf("lsp: write header: %w", err)
	}
	if _, err := c.stdin.Write(body); err != nil {
		return fmt.Errorf("lsp: write body: %w", err)
	}
	return nil
}

// readLoop reads JSON-RPC messages from the server's stdout and dispatches
// responses to pending channels or handles server-push notifications.
func (c *Client) readLoop(r io.Reader) {
	defer close(c.done)
	br := bufio.NewReader(r)
	for {
		msg, err := readMessage(br)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				lspLogf("read error: %v", err)
			}
			return
		}
		c.dispatch(msg)
	}
}

// dispatch routes an incoming raw JSON-RPC message.
func (c *Client) dispatch(raw []byte) {
	// Peek: does it have an "id" field? → response. Otherwise → notification.
	var peek struct {
		ID     *int            `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		lspLogf("dispatch unmarshal: %v", err)
		return
	}

	if peek.ID != nil {
		// Response to one of our requests.
		var resp rpcResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			lspLogf("response unmarshal: %v", err)
			return
		}
		if v, ok := c.pending.Load(resp.ID); ok {
			ch := v.(chan rpcResponse)
			ch <- resp
		}
		return
	}

	// Server-push notification.
	switch peek.Method {
	case "textDocument/publishDiagnostics":
		var p PublishDiagnosticsParams
		if err := json.Unmarshal(peek.Params, &p); err == nil {
			c.diags.Store(p.URI, p.Diagnostics)
		}
	default:
		// Unknown notifications are intentionally ignored (log at trace level
		// only to avoid noisy output during normal operation).
	}
}

func lspLogf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "lsp: "+format+"\n", args...)
}

// readMessage reads one Content-Length-framed message from br.
func readMessage(br *bufio.Reader) ([]byte, error) {
	contentLength := -1
	// Read headers until blank line.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			val := strings.TrimSpace(strings.TrimPrefix(line, "Content-Length:"))
			n, err := strconv.Atoi(val)
			if err != nil {
				return nil, fmt.Errorf("lsp: bad Content-Length %q: %w", val, err)
			}
			contentLength = n
		}
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("lsp: no Content-Length header")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, fmt.Errorf("lsp: read body: %w", err)
	}
	return body, nil
}

// pid returns the current process ID as a pointer (LSP processId may be null).
func pid() *int {
	p := os.Getpid()
	return &p
}
