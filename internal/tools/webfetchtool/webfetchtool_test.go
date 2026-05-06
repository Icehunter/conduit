package webfetchtool

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func input(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, _ := json.Marshal(v)
	return b
}

// newTestTool returns a Tool that uses a plain dialer (no SSRF guard) so
// tests can connect to httptest servers on 127.0.0.1.
func newTestTool() *Tool { return newWithDialer(&net.Dialer{}) }

func TestWebFetch_PlainText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello from server"))
	}))
	defer srv.Close()

	tt := newTestTool()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"url":    srv.URL,
		"prompt": "what does it say?",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	if !strings.Contains(res.Content[0].Text, "hello from server") {
		t.Errorf("content not in result; got: %s", res.Content[0].Text)
	}
}

func TestWebFetch_HTMLStripped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><h1>Title</h1><p>Hello &amp; world</p><script>evil()</script></body></html>`))
	}))
	defer srv.Close()

	tt := newTestTool()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"url":    srv.URL,
		"prompt": "summarize",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	text := res.Content[0].Text
	if strings.Contains(text, "<h1>") || strings.Contains(text, "<p>") {
		t.Errorf("HTML tags not stripped; got: %s", text)
	}
	if strings.Contains(text, "evil()") {
		t.Error("script content should be stripped")
	}
	if !strings.Contains(text, "Title") {
		t.Errorf("title text missing; got: %s", text)
	}
	if !strings.Contains(text, "Hello & world") {
		t.Errorf("entity not decoded; got: %s", text)
	}
}

func TestWebFetch_404ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	tt := newTestTool()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"url":    srv.URL,
		"prompt": "anything",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("expected IsError for 404")
	}
	if !strings.Contains(res.Content[0].Text, "404") {
		t.Errorf("error missing status; got: %s", res.Content[0].Text)
	}
}

func TestWebFetch_InvalidURL(t *testing.T) {
	tt := newTestTool()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"url":    "not-a-url",
		"prompt": "x",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("expected IsError for invalid URL")
	}
}

func TestWebFetch_EmptyURL(t *testing.T) {
	tt := newTestTool()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"url":    "",
		"prompt": "x",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("empty URL should be IsError=true")
	}
}

func TestWebFetch_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tt := newTestTool()
	res, err := tt.Execute(ctx, input(t, map[string]any{
		"url":    srv.URL,
		"prompt": "x",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("cancelled context should IsError=true")
	}
}

func TestWebFetch_LargeBodyTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		// Write more than MaxContentBytes.
		chunk := strings.Repeat("x", 1024)
		for range MaxContentBytes/1024 + 2 {
			_, _ = w.Write([]byte(chunk))
		}
	}))
	defer srv.Close()

	tt := newTestTool()
	res, err := tt.Execute(context.Background(), input(t, map[string]any{
		"url":    srv.URL,
		"prompt": "x",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("IsError: %v", res.Content)
	}
	if !strings.Contains(res.Content[0].Text, "truncated") {
		t.Error("large body should mention truncation")
	}
}

func TestWebFetch_InvalidJSON(t *testing.T) {
	tt := newTestTool()
	res, err := tt.Execute(context.Background(), json.RawMessage(`{bad`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("invalid JSON should IsError=true")
	}
}

func TestWebFetch_StaticMetadata(t *testing.T) {
	tt := newTestTool()
	if tt.Name() != "WebFetch" {
		t.Errorf("Name = %q", tt.Name())
	}
	if !tt.IsReadOnly(nil) {
		t.Error("IsReadOnly should be true")
	}
	if !tt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe should be true")
	}
	var schema map[string]any
	if err := json.Unmarshal(tt.InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema: %v", err)
	}
}
