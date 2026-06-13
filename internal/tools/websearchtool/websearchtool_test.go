package websearchtool

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/websearch"
)

func TestWebSearch_StaticMetadata(t *testing.T) {
	tt := New(nil) // nil client — only tests metadata
	if tt.Name() != "WebSearch" {
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
	props, _ := schema["properties"].(map[string]any)
	if _, ok := props["query"]; !ok {
		t.Error("schema missing 'query' property")
	}
}

func TestWebSearch_InputSchema_RequiresQuery(t *testing.T) {
	tt := New(nil)
	var schema map[string]any
	_ = json.Unmarshal(tt.InputSchema(), &schema)
	required, _ := schema["required"].([]any)
	found := false
	for _, r := range required {
		if r == "query" {
			found = true
		}
	}
	if !found {
		t.Error("'query' should be in required")
	}
}

func TestWebSearch_InvalidJSON(t *testing.T) {
	tt := New(nil)
	res, err := tt.Execute(context.TODO(), json.RawMessage(`{bad`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("invalid JSON should IsError=true")
	}
}

func TestWebSearch_EmptyQuery(t *testing.T) {
	tt := New(nil)
	b, _ := json.Marshal(map[string]any{"query": "  "})
	res, err := tt.Execute(context.TODO(), b)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("empty query should IsError=true")
	}
}

// fakeProvider is a test-only websearch.SearchProvider.
type fakeProvider struct {
	name    string
	results []websearch.Result
	err     error
	called  int
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Search(_ context.Context, _ websearch.Query) ([]websearch.Result, error) {
	f.called++
	return f.results, f.err
}

func TestWebSearch_ProviderReturnsResults(t *testing.T) {
	fp := &fakeProvider{
		name: "FakeSearch",
		results: []websearch.Result{
			{Title: "Go Spec", URL: "https://go.dev/spec", Snippet: "The Go specification"},
		},
	}
	tt := NewWithProviders(nil, fp)
	b, _ := json.Marshal(map[string]any{"query": "go spec"})
	res, err := tt.Execute(context.Background(), b)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.IsError {
		t.Fatalf("got IsError=true, content = %v", res.Content)
	}
	if fp.called != 1 {
		t.Errorf("provider called %d times, want 1", fp.called)
	}
	text := res.Content[0].Text
	if !contains(text, "FakeSearch") {
		t.Errorf("result missing provider name; text = %q", text)
	}
	if !contains(text, "https://go.dev/spec") {
		t.Errorf("result missing URL; text = %q", text)
	}
}

func TestWebSearch_ProviderErrorFallsThrough(t *testing.T) {
	failing := &fakeProvider{name: "Failing", err: errors.New("timeout")}
	good := &fakeProvider{
		name:    "Good",
		results: []websearch.Result{{Title: "Result", URL: "https://example.com", Snippet: "ok"}},
	}
	tt := NewWithProviders(nil, failing, good)
	b, _ := json.Marshal(map[string]any{"query": "test query"})
	res, err := tt.Execute(context.Background(), b)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.IsError {
		t.Fatalf("got IsError=true, content = %v", res.Content)
	}
	if failing.called != 1 {
		t.Errorf("failing provider called %d times, want 1", failing.called)
	}
	if good.called != 1 {
		t.Errorf("good provider called %d times, want 1", good.called)
	}
	text := res.Content[0].Text
	if !contains(text, "Good") {
		t.Errorf("result should name 'Good' provider; text = %q", text)
	}
}

func TestWebSearch_ProviderEmptyResultsFallsThrough(t *testing.T) {
	// A provider returning no results should be skipped just like an error.
	empty := &fakeProvider{name: "Empty", results: nil}
	good := &fakeProvider{
		name:    "Good",
		results: []websearch.Result{{Title: "Hit", URL: "https://example.com", Snippet: "found"}},
	}
	tt := NewWithProviders(nil, empty, good)
	b, _ := json.Marshal(map[string]any{"query": "test"})
	res, err := tt.Execute(context.Background(), b)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error result: %v", res.Content)
	}
	if empty.called != 1 {
		t.Errorf("empty provider called %d times, want 1", empty.called)
	}
	if good.called != 1 {
		t.Errorf("good provider called %d times, want 1", good.called)
	}
}

func TestWebSearch_AllProvidersFailFallsToAnthropicNative(t *testing.T) {
	// When all providers fail, Execute must fall through to the Anthropic-native
	// path. We use a nil client (no real API call) — anthropicSearch guards
	// against nil client and returns an IsError result, so the test can assert
	// the fallback was reached without any panic or recover().
	p1 := &fakeProvider{name: "P1", err: errors.New("fail")}
	p2 := &fakeProvider{name: "P2", err: errors.New("fail")}
	tt := NewWithProviders(nil, p1, p2) // nil client — anthropicSearch returns ErrorResult
	b, _ := json.Marshal(map[string]any{"query": "test"})

	res, err := tt.Execute(context.Background(), b)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	// Both providers were exhausted.
	if p1.called != 1 {
		t.Errorf("p1 called %d times, want 1", p1.called)
	}
	if p2.called != 1 {
		t.Errorf("p2 called %d times, want 1", p2.called)
	}
	// Fell through to Anthropic path — nil client returns an error result.
	if !res.IsError {
		t.Error("expected IsError=true: all providers failed, nil API client should report error")
	}
}

func TestWebSearch_DeduplicateByURL(t *testing.T) {
	fp := &fakeProvider{
		name: "Dupe",
		results: []websearch.Result{
			{Title: "A", URL: "https://same.com", Snippet: "first"},
			{Title: "B", URL: "https://same.com", Snippet: "duplicate"},
			{Title: "C", URL: "https://other.com", Snippet: "unique"},
		},
	}
	tt := NewWithProviders(nil, fp)
	b, _ := json.Marshal(map[string]any{"query": "dedup test"})
	res, err := tt.Execute(context.Background(), b)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	text := res.Content[0].Text
	// "same.com" should appear exactly once.
	if count := countOccurrences(text, "https://same.com"); count != 1 {
		t.Errorf("https://same.com appears %d times, want 1; text=%q", count, text)
	}
}

// helpers

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

func countOccurrences(s, sub string) int {
	return strings.Count(s, sub)
}

// sseStream builds an *api.Stream from a raw SSE byte sequence for unit tests.
func sseStream(raw string) *api.Stream {
	return api.NewStreamFromReader(io.NopCloser(strings.NewReader(raw)))
}

// TestDrainSearchStream covers the block-accumulation and output-assembly logic.
func TestDrainSearchStream_TextBlocks(t *testing.T) {
	// Two text_delta events for block 0, then message_stop.
	raw := "" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello \"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"world\"}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"

	result, err := drainSearchStream(context.Background(), sseStream(raw), "test query")
	if err != nil {
		t.Fatalf("drainSearchStream() error = %v", err)
	}
	if !strings.Contains(result, "Hello world") {
		t.Errorf("result missing expected text; got: %q", result)
	}
	if !strings.Contains(result, `"test query"`) {
		t.Errorf("result missing query header; got: %q", result)
	}
}

func TestDrainSearchStream_EmptyStream(t *testing.T) {
	// No content blocks → should return the "no text results" message.
	raw := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	result, err := drainSearchStream(context.Background(), sseStream(raw), "empty query")
	if err != nil {
		t.Fatalf("drainSearchStream() error = %v", err)
	}
	if !strings.Contains(result, "no text results") {
		t.Errorf("empty stream should return no-results message; got: %q", result)
	}
}

func TestDrainSearchStream_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	raw := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"
	_, err := drainSearchStream(ctx, sseStream(raw), "q")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled; got: %v", err)
	}
}
