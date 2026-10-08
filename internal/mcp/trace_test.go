package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const traceResultJSON = `{"content":[{"type":"text","text":"draft"}],"structuredContent":{"local_router_trace":{"version":1,"request":{"messages":[{"role":"user","content":"expanded"}]}}}}`

func TestCallResultPreservesStructuredContentTransports(t *testing.T) {
	for _, transport := range []string{"http", "sse", "websocket", "stdio"} {
		t.Run(transport, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var client Client
			if transport == "stdio" {
				var err error
				client, err = NewStdioClient(ctx, os.Args[0], []string{"-test.run=^TestTraceStdioHelper$"}, map[string]string{"CONDUIT_TRACE_TEST_HELPER": "1"})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var req jsonrpcRequest
					if transport == "websocket" {
						conn, err := websocket.Accept(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer conn.CloseNow()
						_, b, err := conn.Read(ctx)
						if err != nil {
							t.Error(err)
							return
						}
						if err = json.Unmarshal(b, &req); err != nil {
							t.Error(err)
							return
						}
						b, err = json.Marshal(jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(traceResultJSON)})
						if err != nil {
							t.Error(err)
							return
						}
						if err = conn.Write(ctx, websocket.MessageText, b); err != nil {
							t.Error(err)
						}
						// Keep the connection alive until the client's deferred Close.
						_, _, _ = conn.Read(ctx)
						return
					}
					if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
						t.Error(err)
						return
					}
					b, err := json.Marshal(jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(traceResultJSON)})
					if err != nil {
						t.Error(err)
						return
					}
					if transport == "sse" {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write(b)
					}
				}))
				defer srv.Close()
				if transport == "websocket" {
					client = NewWebSocketClient("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
				} else {
					client = NewHTTPClient(srv.URL, nil)
				}
			}
			defer client.Close()
			got, err := client.CallTool(ctx, "local_implement", json.RawMessage(`{}`))
			if err != nil || got.IsError {
				t.Fatalf("result=%+v err=%v", got, err)
			}
			var want CallResult
			if err := json.Unmarshal([]byte(traceResultJSON), &want); err != nil {
				t.Fatal(err)
			}
			if len(got.StructuredContent) == 0 || string(got.StructuredContent) != string(want.StructuredContent) {
				t.Fatalf("trace lost: %s", got.StructuredContent)
			}
		})
	}
}

func TestTraceStdioHelper(t *testing.T) {
	if os.Getenv("CONDUIT_TRACE_TEST_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var req jsonrpcRequest
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(2)
		}
		if err := json.NewEncoder(os.Stdout).Encode(jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(traceResultJSON)}); err != nil {
			os.Exit(2)
		}
	}
	os.Exit(0)
}
