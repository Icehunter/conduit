package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
)

// ---- JSON-RPC mock server helpers ------------------------------------------

func writeMsg(w io.Writer, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(body))
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

type mockHandleFunc func(method string, params json.RawMessage) (any, error)

func serveMock(r io.Reader, w io.Writer, handle mockHandleFunc) {
	br := bufio.NewReader(r)
	for {
		msg, err := readMessage(br)
		if err != nil {
			return
		}
		var req struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(msg, &req); err != nil {
			return
		}
		if req.ID == nil {
			continue // notification
		}
		result, handleErr := handle(req.Method, req.Params)
		type errObj struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		type resp struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      int             `json:"id"`
			Result  json.RawMessage `json:"result,omitempty"`
			Error   *errObj         `json:"error,omitempty"`
		}
		r2 := resp{JSONRPC: "2.0", ID: *req.ID}
		if handleErr != nil {
			r2.Error = &errObj{Code: -32603, Message: handleErr.Error()}
		} else {
			b, _ := json.Marshal(result)
			r2.Result = b
		}
		_ = writeMsg(w, r2)
	}
}

func startMock(t *testing.T, handle mockHandleFunc) *Client {
	t.Helper()
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
		serveMock(serverR, serverW, wrapped)
		_ = serverW.Close()
		_ = serverR.Close()
	}()
	cl, err := NewClientFromPipes(context.Background(), clientW, clientR)
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

// ---- readMessage -----------------------------------------------------------

func TestReadMessage_basic(t *testing.T) {
	body := `{"jsonrpc":"2.0","id":1,"result":null}`
	msg := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
	br := bufio.NewReader(bytes.NewBufferString(msg))
	got, err := readMessage(br)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("got %q; want %q", got, body)
	}
}

func TestReadMessage_missingContentLength(t *testing.T) {
	msg := "X-Custom: foo\r\n\r\n{}"
	br := bufio.NewReader(bytes.NewBufferString(msg))
	_, err := readMessage(br)
	if err == nil {
		t.Fatal("expected error for missing Content-Length")
	}
}

func TestReadMessage_eof(t *testing.T) {
	br := bufio.NewReader(bytes.NewBufferString(""))
	_, err := readMessage(br)
	if err == nil {
		t.Fatal("expected EOF error")
	}
}

// ---- Client round-trip -----------------------------------------------------

func TestClient_requestResponse(t *testing.T) {
	cl := startMock(t, func(method string, _ json.RawMessage) (any, error) {
		if method == "test/echo" {
			return map[string]string{"ok": "yes"}, nil
		}
		return nil, nil
	})

	raw, err := cl.Request(context.Background(), "test/echo", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] != "yes" {
		t.Errorf("unexpected response: %v", out)
	}
}

func TestClient_requestError(t *testing.T) {
	cl := startMock(t, func(method string, _ json.RawMessage) (any, error) {
		if method == "test/fail" {
			return nil, fmt.Errorf("boom")
		}
		return nil, nil
	})

	_, err := cl.Request(context.Background(), "test/fail", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected error from server")
	}
}

func TestClient_contextCancel(t *testing.T) {
	// Server that never responds to "test/block" (ignores it).
	cl := startMock(t, func(method string, _ json.RawMessage) (any, error) {
		return nil, nil // returns null; fine
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling

	_, err := cl.Request(ctx, "test/block", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
}

func TestClient_diagnosticsCache(t *testing.T) {
	cl := startMock(t, func(_ string, _ json.RawMessage) (any, error) { return nil, nil })

	uri := "file:///foo.go"
	want := []Diagnostic{
		{Severity: SeverityError, Message: "undefined: x"},
	}
	cl.StoreDiagnostics(uri, want)

	got := cl.Diagnostics(uri)
	if len(got) != 1 || got[0].Message != "undefined: x" {
		t.Errorf("unexpected diagnostics: %v", got)
	}
}

func TestClient_diagnosticsEmpty(t *testing.T) {
	cl := startMock(t, func(_ string, _ json.RawMessage) (any, error) { return nil, nil })
	got := cl.Diagnostics("file:///missing.go")
	if len(got) != 0 {
		t.Errorf("expected empty diagnostics; got %v", got)
	}
}

// ---- DiagnosticSeverity.String ---------------------------------------------

func TestDiagnosticSeverity_String(t *testing.T) {
	tests := []struct {
		s    DiagnosticSeverity
		want string
	}{
		{SeverityError, "error"},
		{SeverityWarning, "warning"},
		{SeverityInformation, "information"},
		{SeverityHint, "hint"},
		{DiagnosticSeverity(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("severity %d: got %q; want %q", tt.s, got, tt.want)
		}
	}
}
