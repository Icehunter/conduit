package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	lspclient "github.com/icehunter/conduit/internal/lsp"
)

// ---- JSON-RPC framing helpers used by the mock server ----------------------

func writeMsg(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func readMsg(br *bufio.Reader) ([]byte, error) {
	contentLength := -1
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
			_, err := fmt.Sscanf(val, "%d", &contentLength)
			if err != nil {
				return nil, err
			}
		}
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("no Content-Length")
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(br, body); err != nil {
		return nil, err
	}
	return body, nil
}

// ---- mock LSP server --------------------------------------------------------

// handleFunc is called for each JSON-RPC method. Return (result, nil) for
// success or (nil, err) to send an error response. Return (nil, nil) to send
// a null result.
type handleFunc func(method string, params json.RawMessage) (any, error)

// serveMockLSP reads requests from r and writes responses to w, calling handle
// for each request. It runs until r reaches EOF.
func serveMockLSP(r io.Reader, w io.Writer, handle handleFunc) {
	br := bufio.NewReader(r)
	for {
		msg, err := readMsg(br)
		if err != nil {
			return
		}

		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *int            `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(msg, &req); err != nil {
			return
		}

		// Notifications: no response needed.
		if req.ID == nil {
			continue
		}

		result, handleErr := handle(req.Method, req.Params)

		type rpcErrObj struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		type resp struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      int             `json:"id"`
			Result  json.RawMessage `json:"result,omitempty"`
			Error   *rpcErrObj      `json:"error,omitempty"`
		}

		r := resp{JSONRPC: "2.0", ID: *req.ID}
		if handleErr != nil {
			r.Error = &rpcErrObj{Code: -32603, Message: handleErr.Error()}
		} else {
			b, _ := json.Marshal(result)
			r.Result = b
		}
		_ = writeMsg(w, r)
	}
}

// startMockServer starts a mock LSP server in a background goroutine using
// in-process pipes. It returns a *lspclient.Client wired to the mock.
//
// handle is invoked for each JSON-RPC request. The initialize request is
// handled automatically (returns minimal capabilities).
func startMockServer(t *testing.T, handle handleFunc) *lspclient.Client {
	t.Helper()

	// Pipes: clientStdin → serverReads; serverWrites → clientStdout
	serverR, clientW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	clientR, serverW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	wrapped := func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "initialize":
			return map[string]any{"capabilities": map[string]any{}}, nil
		case "shutdown":
			return nil, nil
		default:
			return handle(method, params)
		}
	}

	go func() {
		serveMockLSP(serverR, serverW, wrapped)
		_ = serverW.Close()
		_ = serverR.Close()
	}()

	cl, err := lspclient.NewClientFromPipes(context.Background(), clientW, clientR)
	if err != nil {
		_ = clientW.Close()
		t.Fatalf("NewClientFromPipes: %v", err)
	}

	t.Cleanup(func() {
		_ = cl.Close()
		_ = clientW.Close()
	})

	return cl
}

// ---- helper to marshal tool input ------------------------------------------

func mkInput(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ---- tests ------------------------------------------------------------------

func TestLSPTool_Hover(t *testing.T) {
	cl := startMockServer(t, func(method string, params json.RawMessage) (any, error) {
		if method != "textDocument/hover" {
			return nil, nil
		}
		return lspclient.Hover{
			Contents: lspclient.MarkupContent{Kind: "markdown", Value: "**func Foo()** string"},
		}, nil
	})

	mgr := &stubManager{cl: cl}
	tt := New(mgr)

	// Write a temp Go file so didOpen can read it.
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "hover",
		"file":      goFile,
		"line":      0,
		"character": 0,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	if !strings.Contains(res.Content[0].Text, "Foo") {
		t.Errorf("expected hover text to contain 'Foo'; got: %s", res.Content[0].Text)
	}
}

func TestLSPTool_Definition(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cl := startMockServer(t, func(method string, params json.RawMessage) (any, error) {
		if method != "textDocument/definition" {
			return nil, nil
		}
		return []lspclient.Location{
			{URI: "file://" + filepath.ToSlash(goFile), Range: lspclient.Range{
				Start: lspclient.Position{Line: 4, Character: 0},
				End:   lspclient.Position{Line: 4, Character: 8},
			}},
		}, nil
	})

	mgr := &stubManager{cl: cl}
	tt := New(mgr)

	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "definition",
		"file":      goFile,
		"line":      0,
		"character": 5,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	text := res.Content[0].Text
	// Should report line 5 (1-based) inside the temp dir.
	if !strings.Contains(text, ":5:") {
		t.Errorf("expected ':5:' in result; got: %s", text)
	}
}

func TestLSPTool_References(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cl := startMockServer(t, func(method string, params json.RawMessage) (any, error) {
		if method != "textDocument/references" {
			return nil, nil
		}
		return []lspclient.Location{
			{URI: "file://" + filepath.ToSlash(goFile), Range: lspclient.Range{Start: lspclient.Position{Line: 0, Character: 0}}},
			{URI: "file://" + filepath.ToSlash(goFile), Range: lspclient.Range{Start: lspclient.Position{Line: 9, Character: 4}}},
		}, nil
	})

	mgr := &stubManager{cl: cl}
	tt := New(mgr)

	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "references",
		"file":      goFile,
		"line":      0,
		"character": 0,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "References (2)") {
		t.Errorf("expected 'References (2)'; got: %s", text)
	}
}

func TestLSPTool_Diagnostics(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cl := startMockServer(t, func(method string, _ json.RawMessage) (any, error) {
		return nil, nil
	})

	// Inject diagnostics directly into the client cache.
	fileURI := "file://" + filepath.ToSlash(goFile)
	cl.StoreDiagnostics(fileURI, []lspclient.Diagnostic{
		{
			Range:    lspclient.Range{Start: lspclient.Position{Line: 2, Character: 0}},
			Severity: lspclient.SeverityError,
			Message:  "undefined: foo",
			Source:   "gopls",
		},
	})

	mgr := &stubManager{cl: cl}
	tt := New(mgr)

	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "diagnostics",
		"file":      goFile,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "undefined: foo") {
		t.Errorf("expected diagnostic message; got: %s", text)
	}
	if !strings.Contains(text, "error") {
		t.Errorf("expected severity 'error'; got: %s", text)
	}
}

func TestLSPTool_UnknownServerGraceful(t *testing.T) {
	mgr := lspclient.NewManager()
	tt := New(mgr)

	// Use a file extension that has no known server.
	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "hover",
		"file":      "/some/file.unknown_ext_xyz",
		"line":      0,
		"character": 0,
	}))
	if err != nil {
		t.Fatal("Execute must not return a Go error; got:", err)
	}
	if !res.IsError {
		t.Fatal("expected IsError=true for unknown extension")
	}
}

func TestLSPTool_GoplsIntegration(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not on PATH; skipping integration test")
	}

	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	src := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(goFile, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	mgr := lspclient.NewManager()
	t.Cleanup(mgr.Close)
	tt := New(mgr)

	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "hover",
		"file":      goFile,
		"line":      2,
		"character": 5,
	}))
	if err != nil {
		t.Fatal(err)
	}
	// A real gopls hover may return "No hover information" for a simple main;
	// that's fine — we just verify no panic and no Go error.
	_ = res
}

// ---- stubManager implements a minimal Manager for tests --------------------

// stubManager always returns the same pre-built client.
type stubManager struct {
	cl *lspclient.Client
}

func (s *stubManager) ServerFor(_ context.Context, _ string) (*lspclient.Client, error) {
	return s.cl, nil
}

// managerInterface is what Tool.Execute uses — defined in lsp.go.
// We verify stubManager satisfies it.
var _ managerIface = (*stubManager)(nil)
var _ managerIface = (*lspclient.Manager)(nil)
