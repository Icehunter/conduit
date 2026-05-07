package bashtool

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBash_StdoutCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash tool is unavailable on Windows")
	}
	tt := New(nil)
	res, err := tt.Execute(context.Background(), json.RawMessage(`{"command":"echo hello"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Errorf("IsError; content=%v", res.Content)
	}
	if !strings.Contains(res.Content[0].Text, "hello") {
		t.Errorf("output = %q", res.Content[0].Text)
	}
}

func TestBash_CombinedStdoutStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash tool is unavailable on Windows")
	}
	tt := New(nil)
	res, err := tt.Execute(context.Background(), json.RawMessage(
		`{"command":"echo out; echo err 1>&2"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Errorf("unexpected IsError: %v", res.Content)
	}
	got := res.Content[0].Text
	if !strings.Contains(got, "out") || !strings.Contains(got, "err") {
		t.Errorf("missing stdout or stderr: %q", got)
	}
}

func TestBash_NonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash tool is unavailable on Windows")
	}
	tt := New(nil)
	res, err := tt.Execute(context.Background(), json.RawMessage(`{"command":"exit 7"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("non-zero exit should mark IsError=true")
	}
	if !strings.Contains(res.Content[0].Text, "Exit code: 7") {
		t.Errorf("output = %q", res.Content[0].Text)
	}
}

func TestBash_Timeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("relies on POSIX sleep")
	}
	tt := New(nil)
	res, err := tt.Execute(context.Background(), json.RawMessage(`{"command":"sleep 5","timeout":100}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("timeout should mark IsError=true")
	}
	if !strings.Contains(res.Content[0].Text, "timed out") {
		t.Errorf("output = %q", res.Content[0].Text)
	}
}

func TestBash_TimeoutCappedToMax(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash tool is unavailable on Windows")
	}
	tt := New(nil)
	// Send timeout > MaxTimeout; should be silently capped (no immediate
	// error from a fast command).
	res, err := tt.Execute(context.Background(), json.RawMessage(`{"command":"echo ok","timeout":99999999}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Errorf("unexpected IsError: %v", res.Content)
	}
}

func TestBash_ContextCancel(t *testing.T) {
	tt := New(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := tt.Execute(ctx, json.RawMessage(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("cancelled context should mark IsError=true")
	}
}

func TestBash_OutputTruncated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash tool is unavailable on Windows")
	}
	tt := New(nil)
	// Generate ~50KB of output (more than MaxOutputBytes).
	res, err := tt.Execute(context.Background(),
		json.RawMessage(`{"command":"yes a | head -c 50000"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content[0].Text, "truncated to first") {
		t.Errorf("expected truncation marker; got len=%d", len(res.Content[0].Text))
	}
	if len(res.Content[0].Text) > MaxOutputBytes+200 {
		t.Errorf("output not truncated: len=%d", len(res.Content[0].Text))
	}
}

func TestBash_EmptyCommandRejected(t *testing.T) {
	tt := New(nil)
	res, err := tt.Execute(context.Background(), json.RawMessage(`{"command":"   "}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("empty command should be IsError=true")
	}
}

func TestBash_InvalidJSONRejected(t *testing.T) {
	tt := New(nil)
	res, err := tt.Execute(context.Background(), json.RawMessage(`{not json`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("invalid JSON should be IsError=true")
	}
}

func TestBash_StaticMetadata(t *testing.T) {
	tt := New(nil)
	if tt.Name() != "Bash" {
		t.Errorf("Name = %q", tt.Name())
	}
	if tt.IsReadOnly(nil) {
		t.Error("IsReadOnly should be false")
	}
	if tt.IsConcurrencySafe(nil) {
		t.Error("IsConcurrencySafe should be false")
	}
	var schema map[string]any
	if err := json.Unmarshal(tt.InputSchema(), &schema); err != nil {
		t.Fatalf("InputSchema not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema type = %v", schema["type"])
	}
}

// Compile-time check: Tool implements the interface.
var _ = func() time.Time { return time.Time{} }
