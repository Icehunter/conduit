// Package sse parses Anthropic's /v1/messages event-stream.
//
// Wire format follows the Server-Sent Events spec (https://html.spec.whatwg.org/multipage/server-sent-events.html):
// each event is a sequence of lines like `field: value\n`, terminated by a
// blank line. We care about exactly two fields: `event` (the event type)
// and `data` (the JSON payload).
//
// Captured fixture: testdata/fixtures/sse/simple_text_response.sse —
// real Claude Code 2.1.126 output for a 3-word reply. Notable quirks the
// parser must tolerate: trailing whitespace before `}` in data payloads,
// `ping` events whose data is the literal string `{"type": "ping"}` and
// must be skipped without breaking the stream.
package sse

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestParse_FixtureBasic walks the captured SSE and asserts every event
// type was seen, in order, with payloads parseable.
func TestParse_FixtureBasic(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "fixtures", "sse", "simple_text_response.sse")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	p := NewParser(f)
	var got []string
	var deltas []string
	for {
		ev, err := p.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		got = append(got, ev.Type)
		if ev.Type == "content_block_delta" {
			d, derr := ev.AsContentBlockDelta()
			if derr != nil {
				t.Fatalf("AsContentBlockDelta: %v", derr)
			}
			if d.Delta.Type == "text_delta" {
				deltas = append(deltas, d.Delta.Text)
			}
		}
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
	if !equalStrings(got, want) {
		t.Errorf("event sequence:\n got=%v\nwant=%v", got, want)
	}

	concat := strings.Join(deltas, "")
	if concat != "Hi there friend" {
		t.Errorf("concatenated deltas = %q; want %q", concat, "Hi there friend")
	}
}

// TestParse_PingSkipped verifies ping events are silently absorbed by
// default. Some callers may want them surfaced; we expose IncludePings.
func TestParse_PingNotSurfacedByDefault(t *testing.T) {
	body := `event: ping
data: {"type": "ping"}

event: message_stop
data: {"type":"message_stop"}

`
	p := NewParser(strings.NewReader(body))
	ev, err := p.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.Type != "message_stop" {
		t.Errorf("first event = %q; want message_stop (ping should have been skipped)", ev.Type)
	}
	if _, err := p.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("expected EOF, got %v", err)
	}
}

// TestParse_PingIncludedWhenRequested verifies opt-in surfacing.
func TestParse_PingIncludedWhenRequested(t *testing.T) {
	body := `event: ping
data: {"type": "ping"}

`
	p := NewParser(strings.NewReader(body))
	p.IncludePings = true
	ev, err := p.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.Type != "ping" {
		t.Errorf("event = %q; want ping", ev.Type)
	}
}

// TestParse_ErrorEvent maps the `error` event type to a typed error.
func TestParse_ErrorEvent(t *testing.T) {
	body := `event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"server is overloaded"}}

`
	p := NewParser(strings.NewReader(body))
	_, err := p.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	var ae *Error
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v; want *sse.Error", err)
	}
	if ae.Type != "overloaded_error" {
		t.Errorf("Type = %q", ae.Type)
	}
	if ae.Message != "server is overloaded" {
		t.Errorf("Message = %q", ae.Message)
	}
}

// TestParse_TolerateMultilineData: SSE allows multiple `data:` lines per
// event; concat with newline. Anthropic doesn't currently emit these, but
// the spec allows them and our parser must not regress.
func TestParse_TolerateMultilineData(t *testing.T) {
	// Note: multiline data lines are concated. Use syntactically valid JSON.
	body := `event: ping
data: {"type":
data: "ping"}

`
	p := NewParser(strings.NewReader(body))
	p.IncludePings = true
	ev, err := p.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.Type != "ping" {
		t.Errorf("event = %q", ev.Type)
	}
	if !strings.Contains(string(ev.RawData), `"ping"`) {
		t.Errorf("RawData = %s", ev.RawData)
	}
}

// TestParse_EmptyStream returns EOF immediately.
func TestParse_EmptyStream(t *testing.T) {
	p := NewParser(strings.NewReader(""))
	_, err := p.Next()
	if !errors.Is(err, io.EOF) {
		t.Errorf("err = %v; want EOF", err)
	}
}

// TestParse_TolerateTrailingWhitespace mirrors a real-world quirk: the
// captured fixture has spaces between `}` and `\n` on most data lines.
func TestParse_TolerateTrailingWhitespace(t *testing.T) {
	body := "event: message_stop\ndata: {\"type\":\"message_stop\"}             \n\n"
	p := NewParser(strings.NewReader(body))
	ev, err := p.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.Type != "message_stop" {
		t.Errorf("event = %q", ev.Type)
	}
}

// TestAsContentBlockDelta_TextDelta decodes the most common delta variant.
func TestAsContentBlockDelta_TextDelta(t *testing.T) {
	body := `event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}

`
	p := NewParser(strings.NewReader(body))
	ev, err := p.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	d, err := ev.AsContentBlockDelta()
	if err != nil {
		t.Fatalf("AsContentBlockDelta: %v", err)
	}
	if d.Index != 0 || d.Delta.Type != "text_delta" || d.Delta.Text != "hello" {
		t.Errorf("delta = %+v", d)
	}
}

// FuzzParse exercises the framing parser with arbitrary input. Crash
// triage: any panic, infinite loop, or read past EOF is a bug.
func FuzzParse(f *testing.F) {
	f.Add("event: ping\ndata: {\"type\":\"ping\"}\n\n")
	f.Add("event: message_stop\ndata: {}\n\n")
	f.Add("")
	f.Add("\n\n\n")
	f.Add(": comment\nevent: x\ndata: {}\n\n")
	f.Fuzz(func(t *testing.T, input string) {
		p := NewParser(strings.NewReader(input))
		p.IncludePings = true
		// Drain up to a bounded number of events so a pathological input
		// can't loop forever.
		for range 1000 {
			_, err := p.Next()
			if err != nil {
				return
			}
		}
	})
}

// TestParse_TruncatedMidEvent ensures EOF mid-event returns ErrTruncated, not
// a synthesized complete event. The agent loop must surface this as an error.
func TestParse_TruncatedMidEvent(t *testing.T) {
	// Stream ends with no blank-line terminator after the data line.
	body := "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_de"
	p := NewParser(strings.NewReader(body))
	_, err := p.Next()
	if !errors.Is(err, ErrTruncated) {
		t.Errorf("err = %v; want ErrTruncated", err)
	}
}

// TestParse_CleanEOFBetweenEvents returns io.EOF (not ErrTruncated) when
// the stream ends cleanly on an event boundary.
func TestParse_CleanEOFBetweenEvents(t *testing.T) {
	body := "event: message_stop\ndata: {}\n\n"
	p := NewParser(strings.NewReader(body))
	_, err := p.Next()
	if err != nil {
		t.Fatalf("first event: %v", err)
	}
	_, err = p.Next()
	if !errors.Is(err, io.EOF) {
		t.Errorf("second Next() err = %v; want io.EOF", err)
	}
}

// TestParse_LineTooLong ensures a single oversized data line returns ErrLineTooLong
// rather than OOMing the process.
func TestParse_LineTooLong(t *testing.T) {
	// Write a "data: " prefix followed by 9 MiB of 'x' with no newline terminator.
	prefix := "data: "
	overflow := strings.Repeat("x", 9<<20)
	body := prefix + overflow
	p := NewParser(strings.NewReader(body))
	_, err := p.Next()
	if !errors.Is(err, ErrLineTooLong) {
		t.Errorf("err = %v; want ErrLineTooLong", err)
	}
}

func equalStrings(a, b []string) bool {
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
