package bashtool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/tool"
	"github.com/icehunter/conduit/internal/truncate"
)

// TestMain points CONDUIT_CONFIG_DIR at a scratch directory for the whole
// binary: spill files and the CCR store live under it, and the tests must not
// write to the real ~/.conduit.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "bashtool-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.Setenv("CONDUIT_CONFIG_DIR", dir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

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
	// ~50KB in 25000 short lines: over both the byte and the line budget,
	// but small enough to stay in memory.
	text := execText(t, `{"command":"yes a | head -c 50000"}`, false)
	if !strings.Contains(text, " lines / ") || !strings.Contains(text, " bytes omitted; full output: ") {
		t.Fatalf("expected head/tail marker; got len=%d", len(text))
	}
	if len(text) > MaxOutputBytes+500 {
		t.Errorf("output not bounded: len=%d", len(text))
	}
	if !strings.HasPrefix(text, "a\na\n") || !strings.HasSuffix(text, "a\na") {
		t.Errorf("head or tail missing: %q ... %q", text[:20], text[len(text)-20:])
	}
	want := strings.TrimRight(strings.Repeat("a\n", 25000), "\n")
	if got := readSpill(t, text); got != want {
		t.Errorf("spill file holds %d bytes, want the full %d", len(got), len(want))
	}
}

// TestBash_LargeOutputNotKilled: output past the in-memory threshold streams
// to a spill file instead of killing the command, and the model sees both
// ends of it.
func TestBash_LargeOutputNotKilled(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash tool is unavailable on Windows")
	}
	// seq 1 200000 writes 1.3 MB.
	text := execText(t, `{"command":"seq 1 200000"}`, false)
	if !strings.HasPrefix(text, "1\n2\n3\n") {
		t.Errorf("head missing: %q", text[:min(len(text), 40)])
	}
	if !strings.HasSuffix(text, "199999\n200000") {
		t.Errorf("tail missing: %q", text[max(len(text)-40, 0):])
	}
	if len(text) > MaxOutputBytes+500 {
		t.Errorf("output not bounded: len=%d", len(text))
	}
	if got, want := readSpill(t, text), seqOutput(200000); got != want {
		t.Errorf("spill file holds %d bytes, want the full %d", len(got), len(want))
	}
}

// TestBash_SpillFileIsPrivate: spill files hold raw command output, which
// can include secrets, so they are 0600 in a 0700 directory.
func TestBash_SpillFileIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash tool is unavailable on Windows")
	}
	for _, command := range []string{
		`{"command":"seq 1 30000"}`,  // spilled after the run
		`{"command":"seq 1 200000"}`, // spilled while running
	} {
		path := spillPath(t, execText(t, command, false))
		for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
			info, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != want {
				t.Errorf("%s: mode of %s = %v, want %v", command, p, got, want)
			}
		}
		if filepath.Dir(path) != truncate.Dir() {
			t.Errorf("spill file %s is outside %s", path, truncate.Dir())
		}
	}
}

// TestBash_NonZeroExitStillSpills: a failing command's output is bounded and
// saved the same way; its tail is usually where the failure is.
func TestBash_NonZeroExitStillSpills(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash tool is unavailable on Windows")
	}
	text := execText(t, `{"command":"seq 1 30000; exit 3"}`, true)
	if !strings.HasSuffix(text, "29999\n30000\nExit code: 3") {
		t.Errorf("tail or exit code missing: %q", text[max(len(text)-60, 0):])
	}
	if got, want := readSpill(t, text), strings.TrimRight(seqOutput(30000), "\n"); got != want {
		t.Errorf("spill file holds %d bytes, want the full %d", len(got), len(want))
	}
}

// TestBash_TimeoutStillSpills: a timed-out command's output is bounded and
// saved too, instead of being cut to its head.
func TestBash_TimeoutStillSpills(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash tool is unavailable on Windows")
	}
	text := execText(t, `{"command":"seq 1 30000; sleep 30","timeout":1000}`, true)
	if !strings.HasSuffix(text, "29999\n30000\nCommand timed out after 1s.") {
		t.Errorf("tail or timeout message missing: %q", text[max(len(text)-60, 0):])
	}
	if got, want := readSpill(t, text), strings.TrimRight(seqOutput(30000), "\n"); got != want {
		t.Errorf("spill file holds %d bytes, want the full %d", len(got), len(want))
	}
}

// TestBash_DiskCapKillsRunaway: the disk safety cap is the only output limit
// that kills a command, and what was written before it is kept as the
// partial output.
func TestBash_DiskCapKillsRunaway(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash tool is unavailable on Windows")
	}
	setCaptureLimits(t, 64<<10, 1<<20)
	text := execText(t, `{"command":"yes"}`, true)
	if !strings.Contains(text, "[killed: output exceeded 1 MB; partial output: ") {
		t.Fatalf("missing kill report: %q", text[max(len(text)-200, 0):])
	}
	if !strings.HasPrefix(text, "y\ny\n") {
		t.Errorf("head missing: %q", text[:min(len(text), 20)])
	}
	info, err := os.Stat(spillPath(t, text))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 1<<20 {
		t.Errorf("partial output is %d bytes, want the 1 MB cap", info.Size())
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

func TestBash_RunsInContextCwd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash tool is unavailable on Windows")
	}
	dir := t.TempDir()
	ctx := tool.WithCwd(context.Background(), dir)
	res, err := New(nil).Execute(ctx, json.RawMessage(`{"command":"pwd"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := strings.TrimSpace(res.Content[0].Text)
	want, _ := filepath.EvalSymlinks(dir)
	if gotReal, _ := filepath.EvalSymlinks(got); gotReal != want {
		t.Errorf("pwd = %q, want %q", got, want)
	}
}

var spillPathRe = regexp.MustCompile(`(?:full|partial) output: ([^\]]+)\]`)

// execText runs the Bash tool and returns its text, failing unless IsError
// matches wantErr.
func execText(t *testing.T, input string, wantErr bool) string {
	t.Helper()
	res, err := New(nil).Execute(context.Background(), json.RawMessage(input))
	if err != nil {
		t.Fatalf("Execute(%s): %v", input, err)
	}
	text := res.Content[0].Text
	if res.IsError != wantErr {
		t.Fatalf("Execute(%s) IsError = %v, want %v; output tail %q", input, res.IsError, wantErr, text[max(len(text)-200, 0):])
	}
	return text
}

// spillPath returns the spill file named in a tool result.
func spillPath(t *testing.T, text string) string {
	t.Helper()
	m := spillPathRe.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("no spill path in output (len=%d): %q", len(text), text[max(len(text)-300, 0):])
	}
	return m[1]
}

func readSpill(t *testing.T, text string) string {
	t.Helper()
	data, err := os.ReadFile(spillPath(t, text))
	if err != nil {
		t.Fatalf("read spill file: %v", err)
	}
	return string(data)
}

// seqOutput is what `seq 1 n` prints.
func seqOutput(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "%d\n", i)
	}
	return sb.String()
}

// setCaptureLimits lowers the spill threshold and disk cap for one test.
func setCaptureLimits(t *testing.T, threshold, diskCap int64) {
	t.Helper()
	oldThreshold, oldCap := spillThreshold, outputDiskCap
	spillThreshold, outputDiskCap = threshold, diskCap
	t.Cleanup(func() { spillThreshold, outputDiskCap = oldThreshold, oldCap })
}
