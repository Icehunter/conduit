package recorder

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// newTmpPath returns a temp file path that does NOT exist yet (just the path).
func newTmpPath(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.cast")
	if err != nil {
		t.Fatal(err)
	}
	path := f.Name()
	f.Close()
	os.Remove(path)
	return path
}

// readLines reads all non-empty lines from the given file.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open cast file: %v", err)
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l != "" {
			lines = append(lines, l)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	return lines
}

// parseHeader parses line 0 of a .cast file as a castHeader.
func parseHeader(t *testing.T, line string) castHeader {
	t.Helper()
	var h castHeader
	if err := json.Unmarshal([]byte(line), &h); err != nil {
		t.Fatalf("parse header %q: %v", line, err)
	}
	return h
}

// parseEvent parses an event line as [elapsed, type, data].
func parseEvent(t *testing.T, line string) (elapsed float64, evType, data string) {
	t.Helper()
	var raw []json.RawMessage
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		t.Fatalf("parse event %q: %v", line, err)
	}
	if len(raw) != 3 {
		t.Fatalf("event has %d elements, want 3: %s", len(raw), line)
	}
	if err := json.Unmarshal(raw[0], &elapsed); err != nil {
		t.Fatalf("parse elapsed: %v", err)
	}
	if err := json.Unmarshal(raw[1], &evType); err != nil {
		t.Fatalf("parse type: %v", err)
	}
	if err := json.Unmarshal(raw[2], &data); err != nil {
		t.Fatalf("parse data: %v", err)
	}
	return
}

// TestStartCreatesFileWithValidHeader verifies that Start() creates the .cast
// file and writes a valid asciicast v2 header as the first line.
func TestStartCreatesFileWithValidHeader(t *testing.T) {
	r := &Recorder{}
	path := newTmpPath(t)

	if err := r.Start(path); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer r.Stop() //nolint:errcheck

	// File must exist immediately after Start.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cast file not created: %v", err)
	}

	// Read lines while still recording (header is already flushed).
	lines := readLines(t, path)
	if len(lines) == 0 {
		t.Fatal("cast file is empty")
	}

	hdr := parseHeader(t, lines[0])

	if hdr.Version != 2 {
		t.Errorf("header version = %d, want 2", hdr.Version)
	}
	if hdr.Width <= 0 {
		t.Errorf("header width = %d, want > 0", hdr.Width)
	}
	if hdr.Height <= 0 {
		t.Errorf("header height = %d, want > 0", hdr.Height)
	}
	if hdr.Timestamp <= 0 {
		t.Errorf("header timestamp = %d, want > 0", hdr.Timestamp)
	}
	if hdr.Env == nil {
		t.Error("header env is nil")
	}
}

// TestStdoutWritesCapturedAsOutputEvents checks that data written to os.Stdout
// after Start() appears as "o" events in the .cast file.
func TestStdoutWritesCapturedAsOutputEvents(t *testing.T) {
	r := &Recorder{}
	path := newTmpPath(t)

	if err := r.Start(path); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Write to os.Stdout — this now goes through the pipe.
	testMsg := "hello recorder"
	_, _ = os.Stdout.WriteString(testMsg)

	// Give the drain goroutine time to process.
	time.Sleep(50 * time.Millisecond)

	if err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	lines := readLines(t, path)
	if len(lines) < 2 {
		t.Fatalf("want at least header + 1 event, got %d lines", len(lines))
	}

	// lines[0] is the header; check remaining for an "o" event containing testMsg.
	found := false
	for _, l := range lines[1:] {
		_, evType, data := parseEvent(t, l)
		if evType == "o" && strings.Contains(data, testMsg) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("did not find 'o' event containing %q in:\n%s", testMsg, strings.Join(lines[1:], "\n"))
	}
}

// TestStopClosesCleanly verifies that Stop() returns no error and the file is
// readable after stopping.
func TestStopClosesCleanly(t *testing.T) {
	r := &Recorder{}
	path := newTmpPath(t)

	if err := r.Start(path); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.Stop(); err != nil {
		t.Errorf("Stop returned error: %v", err)
	}

	// File must still be a valid cast file after Stop.
	lines := readLines(t, path)
	if len(lines) == 0 {
		t.Error("cast file empty after Stop")
	}
	parseHeader(t, lines[0]) // panics/fails if invalid
}

// TestDoubleStartReturnsError verifies that calling Start twice returns an error.
func TestDoubleStartReturnsError(t *testing.T) {
	r := &Recorder{}
	path := newTmpPath(t)

	if err := r.Start(path); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer r.Stop() //nolint:errcheck

	err := r.Start(newTmpPath(t))
	if err == nil {
		t.Error("second Start should return an error, got nil")
	}
}

// TestIsRecordingState checks that IsRecording() reflects the recording state.
func TestIsRecordingState(t *testing.T) {
	r := &Recorder{}
	path := newTmpPath(t)

	if r.IsRecording() {
		t.Error("IsRecording() should be false before Start")
	}

	if err := r.Start(path); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if !r.IsRecording() {
		t.Error("IsRecording() should be true after Start")
	}

	if err := r.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if r.IsRecording() {
		t.Error("IsRecording() should be false after Stop")
	}
}

// TestRecordingPathFormat verifies RecordingPath returns a path ending in .cast
// inside a "recordings" directory.
func TestRecordingPathFormat(t *testing.T) {
	path, err := RecordingPath()
	if err != nil {
		t.Fatalf("RecordingPath: %v", err)
	}
	if !strings.HasSuffix(path, ".cast") {
		t.Errorf("path does not end in .cast: %s", path)
	}
	dir := strings.ToLower(path)
	if !strings.Contains(dir, "recordings") {
		t.Errorf("path does not contain 'recordings': %s", path)
	}
}
