package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/sse"
)

// TestStreamMessage_FixtureReplay: server replays the captured SSE stream
// and our streaming reader yields the same event sequence the parser test
// validates, plus the full request shape mirrors the non-streaming path.
func TestStreamMessage_FixtureReplay(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "testdata", "fixtures", "sse", "simple_text_response.sse")
	body, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var captured *http.Request
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		captured = r.Clone(r.Context())
		// Drain so server doesn't reset.
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write(body)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{
		BaseURL:     srv.URL,
		AuthToken:   "tok",
		BetaHeaders: []string{"oauth-2025-04-20"},
		SessionID:   "s",
	}, srv.Client())

	stream, err := c.StreamMessage(context.Background(), &MessageRequest{
		Model: "m", MaxTokens: 1, Stream: true,
		Messages: []Message{{Role: "user", Content: []ContentBlock{{Type: "text", Text: "hi"}}}},
	})
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var types []string
	for {
		ev, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		types = append(types, ev.Type)
	}
	want := []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}
	if !equalSlices(types, want) {
		t.Errorf("event types\n got=%v\nwant=%v", types, want)
	}

	if captured.Header.Get("Authorization") != "Bearer tok" {
		t.Errorf("Authorization = %q", captured.Header.Get("Authorization"))
	}
}

// TestStreamMessage_ContextCancelStops verifies cancellation propagates
// into the SSE reader.
func TestStreamMessage_ContextCancelStops(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte("event: ping\ndata: {\"type\": \"ping\"}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := c.StreamMessage(ctx, &MessageRequest{Model: "m", MaxTokens: 1, Stream: true})
	if err != nil {
		t.Fatalf("StreamMessage: %v", err)
	}
	defer func() { _ = stream.Close() }()

	cancel()

	// Drain — should hit an error or EOF promptly, not hang.
	for i := 0; i < 100; i++ {
		_, err := stream.Next()
		if err != nil {
			return
		}
	}
	t.Fatal("Next did not stop after cancel")
}

// TestStreamMessage_APIError on non-2xx returns the same error envelope as
// the non-streaming path.
func TestStreamMessage_APIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"oops"}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	_, err := c.StreamMessage(context.Background(), &MessageRequest{Model: "m", MaxTokens: 1, Stream: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "invalid_request_error") {
		t.Errorf("err = %v", err)
	}
}

// TestStreamMessage_SetsStreamTrue: even if the caller forgets, we force
// stream:true on the request body to avoid silent fallback to JSON.
func TestStreamMessage_SetsStreamTrue(t *testing.T) {
	var sawStream bool
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		sawStream = strings.Contains(string(raw), `"stream":true`)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(Config{BaseURL: srv.URL, AuthToken: "t"}, srv.Client())
	stream, err := c.StreamMessage(context.Background(), &MessageRequest{
		Model: "m", MaxTokens: 1, Stream: false, // intentionally false
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	for {
		_, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !sawStream {
		t.Error("StreamMessage did not force stream:true on the request body")
	}
}

// silence unused for now in case parser package not auto-imported.
var _ = sse.NewParser

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
