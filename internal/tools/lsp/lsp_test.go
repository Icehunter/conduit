package lsp

import (
	"bufio"
	"bytes"
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
		"filePath":  goFile,
		"line":      1,
		"character": 1,
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
		"operation": "goToDefinition",
		"filePath":  goFile,
		"line":      1,
		"character": 6,
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
		"operation": "findReferences",
		"filePath":  goFile,
		"line":      1,
		"character": 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "Found 2 references across 1 files:") {
		t.Errorf("expected 'Found 2 references across 1 files:'; got: %s", text)
	}
}

func TestLSPTool_UnknownServerGraceful(t *testing.T) {
	mgr := lspclient.NewManager()
	tt := New(mgr)

	// Use a file extension that has no known server.
	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "hover",
		"filePath":  "/some/file.unknown_ext_xyz",
		"line":      1,
		"character": 1,
	}))
	if err != nil {
		t.Fatal("Execute must not return a Go error; got:", err)
	}
	if res.IsError {
		t.Fatalf("Claude Code never returns a tool-level error for a missing server; got IsError: %s", res.Content)
	}
	want := "No LSP server available for file type: .unknown_ext_xyz"
	if res.Content[0].Text != want {
		t.Errorf("expected exact message %q; got: %s", want, res.Content[0].Text)
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
		"filePath":  goFile,
		"line":      3,
		"character": 6,
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

func (s *stubManager) HasEverConnected() bool { return true }

// managerInterface is what Tool.Execute uses — defined in lsp.go.
// We verify stubManager satisfies it.
var _ managerIface = (*stubManager)(nil)
var _ managerIface = (*lspclient.Manager)(nil)

func TestLSPTool_DocumentSymbol(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cl := startMockServer(t, func(method string, _ json.RawMessage) (any, error) {
		if method != "textDocument/documentSymbol" {
			return nil, nil
		}
		return []lspclient.DocumentSymbol{
			{
				Name:           "MyFunc",
				Kind:           lspclient.SymbolFunction,
				Range:          lspclient.Range{Start: lspclient.Position{Line: 2, Character: 0}},
				SelectionRange: lspclient.Range{Start: lspclient.Position{Line: 2, Character: 5}},
			},
		}, nil
	})

	mgr := &stubManager{cl: cl}
	tt := New(mgr)

	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "documentSymbol",
		"filePath":  goFile,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	if !strings.Contains(res.Content[0].Text, "MyFunc") {
		t.Errorf("expected 'MyFunc' in result; got: %s", res.Content[0].Text)
	}
}

func TestLSPTool_WorkspaceSymbol(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cl := startMockServer(t, func(method string, _ json.RawMessage) (any, error) {
		if method != "workspace/symbol" {
			return nil, nil
		}
		return []lspclient.SymbolInformation{
			{
				Name: "Manager",
				Kind: lspclient.SymbolClass,
				Location: lspclient.Location{
					URI:   "file://" + filepath.ToSlash(goFile),
					Range: lspclient.Range{Start: lspclient.Position{Line: 10, Character: 0}},
				},
			},
		}, nil
	})

	mgr := &stubManager{cl: cl}
	tt := New(mgr)

	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "workspaceSymbol",
		"filePath":  goFile,
		"query":     "Manager",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	if !strings.Contains(res.Content[0].Text, "Manager") {
		t.Errorf("expected 'Manager' in result; got: %s", res.Content[0].Text)
	}
}

func TestLSPTool_Implementation(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cl := startMockServer(t, func(method string, _ json.RawMessage) (any, error) {
		if method != "textDocument/implementation" {
			return nil, nil
		}
		return []lspclient.Location{
			{URI: "file://" + filepath.ToSlash(goFile), Range: lspclient.Range{Start: lspclient.Position{Line: 7, Character: 0}}},
		}, nil
	})

	mgr := &stubManager{cl: cl}
	tt := New(mgr)

	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "goToImplementation",
		"filePath":  goFile,
		"line":      1,
		"character": 6,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	// goToImplementation reuses the goToDefinition (Et) formatter.
	if !strings.Contains(res.Content[0].Text, "Defined in ") {
		t.Errorf("expected 'Defined in ' in result; got: %s", res.Content[0].Text)
	}
}

func TestLSPTool_CallHierarchyIncoming(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	fileURI := "file://" + filepath.ToSlash(goFile)

	cl := startMockServer(t, func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "textDocument/prepareCallHierarchy":
			return []lspclient.CallHierarchyItem{
				{Name: "Foo", Kind: lspclient.SymbolFunction, URI: fileURI,
					Range:          lspclient.Range{Start: lspclient.Position{Line: 2}},
					SelectionRange: lspclient.Range{Start: lspclient.Position{Line: 2}}},
			}, nil
		case "callHierarchy/incomingCalls":
			return []lspclient.CallHierarchyIncomingCall{
				{From: lspclient.CallHierarchyItem{Name: "main", URI: fileURI,
					Range:          lspclient.Range{Start: lspclient.Position{Line: 5}},
					SelectionRange: lspclient.Range{Start: lspclient.Position{Line: 5}}}},
			}, nil
		default:
			return nil, nil
		}
	})

	mgr := &stubManager{cl: cl}
	tt := New(mgr)

	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "incomingCalls",
		"filePath":  goFile,
		"line":      3,
		"character": 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	if !strings.Contains(res.Content[0].Text, "main") {
		t.Errorf("expected caller 'main'; got: %s", res.Content[0].Text)
	}
}

func TestLSPTool_CallHierarchyOutgoing(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	fileURI := "file://" + filepath.ToSlash(goFile)

	cl := startMockServer(t, func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "textDocument/prepareCallHierarchy":
			return []lspclient.CallHierarchyItem{
				{Name: "main", Kind: lspclient.SymbolFunction, URI: fileURI,
					Range:          lspclient.Range{Start: lspclient.Position{Line: 4}},
					SelectionRange: lspclient.Range{Start: lspclient.Position{Line: 4}}},
			}, nil
		case "callHierarchy/outgoingCalls":
			return []lspclient.CallHierarchyOutgoingCall{
				{To: lspclient.CallHierarchyItem{Name: "Bar", URI: fileURI,
					Range:          lspclient.Range{Start: lspclient.Position{Line: 10}},
					SelectionRange: lspclient.Range{Start: lspclient.Position{Line: 10}}}},
			}, nil
		default:
			return nil, nil
		}
	})

	mgr := &stubManager{cl: cl}
	tt := New(mgr)

	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "outgoingCalls",
		"filePath":  goFile,
		"line":      5,
		"character": 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	if !strings.Contains(res.Content[0].Text, "Bar") {
		t.Errorf("expected callee 'Bar'; got: %s", res.Content[0].Text)
	}
}

// TestLSPTool_HoverNullNoInfo locks the exact Claude Code message when the LSP
// server returns no hover (null result).
func TestLSPTool_HoverNullNoInfo(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cl := startMockServer(t, func(method string, _ json.RawMessage) (any, error) {
		if method != "textDocument/hover" {
			return nil, nil
		}
		return nil, nil // null result
	})

	mgr := &stubManager{cl: cl}
	tt := New(mgr)

	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "hover",
		"filePath":  goFile,
		"line":      1,
		"character": 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	want := "No hover information available. This may occur if the cursor is not on a symbol, or if the LSP server has not fully indexed the file."
	if res.Content[0].Text != want {
		t.Errorf("expected exact hover message; got: %s", res.Content[0].Text)
	}
}

// TestLSPTool_CallHierarchyNoItem locks the exact Claude Code message when no
// call-hierarchy item exists at the position.
func TestLSPTool_CallHierarchyNoItem(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cl := startMockServer(t, func(method string, _ json.RawMessage) (any, error) {
		if method != "textDocument/prepareCallHierarchy" {
			return nil, nil
		}
		return nil, nil // no items
	})

	mgr := &stubManager{cl: cl}
	tt := New(mgr)

	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "incomingCalls",
		"filePath":  goFile,
		"line":      1,
		"character": 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	want := "No call hierarchy item found at this position"
	if res.Content[0].Text != want {
		t.Errorf("expected exact message; got: %s", res.Content[0].Text)
	}
}

// TestLSPTool_FileTooLarge locks the Claude Code 10MB file-size gate.
func TestLSPTool_FileTooLarge(t *testing.T) {
	dir := t.TempDir()
	goFile := filepath.Join(dir, "main.go")
	big := bytes.Repeat([]byte("x"), 11*1024*1024) // 11MB > 10MB limit
	if err := os.WriteFile(goFile, big, 0644); err != nil {
		t.Fatal(err)
	}

	// A mock server is provided so ServerFor succeeds, but the size gate must
	// fire before any LSP request is made.
	cl := startMockServer(t, func(method string, _ json.RawMessage) (any, error) {
		t.Errorf("mock server should not be called for a too-large file")
		return nil, nil
	})
	mgr := &stubManager{cl: cl}
	tt := New(mgr)

	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "hover",
		"filePath":  goFile,
		"line":      1,
		"character": 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	if !strings.Contains(res.Content[0].Text, "File too large for LSP analysis") {
		t.Errorf("expected file-too-large message; got: %s", res.Content[0].Text)
	}
}

// TestLSPTool_ReferencesExact locks the exact grouped output for references
// spanning two files.
func TestLSPTool_ReferencesExact(t *testing.T) {
	dir1 := t.TempDir()
	file1 := filepath.Join(dir1, "a.go")
	if err := os.WriteFile(file1, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dir2 := t.TempDir()
	file2 := filepath.Join(dir2, "b.go")
	if err := os.WriteFile(file2, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cl := startMockServer(t, func(method string, _ json.RawMessage) (any, error) {
		if method != "textDocument/references" {
			return nil, nil
		}
		return []lspclient.Location{
			{URI: "file://" + filepath.ToSlash(file1), Range: lspclient.Range{Start: lspclient.Position{Line: 0, Character: 0}}},
			{URI: "file://" + filepath.ToSlash(file2), Range: lspclient.Range{Start: lspclient.Position{Line: 3, Character: 4}}},
		}, nil
	})

	mgr := &stubManager{cl: cl}
	tt := New(mgr)

	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "findReferences",
		"filePath":  file1,
		"line":      1,
		"character": 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "Found 2 references across 2 files:") {
		t.Errorf("expected two-file grouping header; got: %s", text)
	}
	if !strings.Contains(text, filepath.ToSlash(file1)) {
		t.Errorf("expected first file path in result; got: %s", text)
	}
	if !strings.Contains(text, filepath.ToSlash(file2)) {
		t.Errorf("expected second file path in result; got: %s", text)
	}
	if !strings.Contains(text, "  Line 1:1") {
		t.Errorf("expected 1-based line formatting; got: %s", text)
	}
	if !strings.Contains(text, "  Line 4:5") {
		t.Errorf("expected 1-based line formatting for second ref; got: %s", text)
	}
}

// TestLSPTool_WorkspaceSymbolPlural locks the pluralization and grouping for a
// workspace search spanning two files.
func TestLSPTool_WorkspaceSymbolPlural(t *testing.T) {
	dir1 := t.TempDir()
	file1 := filepath.Join(dir1, "a.go")
	if err := os.WriteFile(file1, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dir2 := t.TempDir()
	file2 := filepath.Join(dir2, "b.go")
	if err := os.WriteFile(file2, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cl := startMockServer(t, func(method string, _ json.RawMessage) (any, error) {
		if method != "workspace/symbol" {
			return nil, nil
		}
		return []lspclient.SymbolInformation{
			{Name: "Manager", Kind: lspclient.SymbolFunction, ContainerName: "pkg",
				Location: lspclient.Location{URI: "file://" + filepath.ToSlash(file1), Range: lspclient.Range{Start: lspclient.Position{Line: 2, Character: 0}}}},
			{Name: "Manager", Kind: lspclient.SymbolFunction, ContainerName: "pkg",
				Location: lspclient.Location{URI: "file://" + filepath.ToSlash(file2), Range: lspclient.Range{Start: lspclient.Position{Line: 5, Character: 0}}}},
		}, nil
	})

	mgr := &stubManager{cl: cl}
	tt := New(mgr)

	res, err := tt.Execute(context.Background(), mkInput(t, map[string]any{
		"operation": "workspaceSymbol",
		"query":     "Manager",
		"filePath":  file1,
		"line":      1,
		"character": 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	text := res.Content[0].Text
	if !strings.Contains(text, "Found 2 symbols in workspace:") {
		t.Errorf("expected plural 'symbols'; got: %s", text)
	}
	if !strings.Contains(text, " in pkg") {
		t.Errorf("expected container name suffix; got: %s", text)
	}
}
