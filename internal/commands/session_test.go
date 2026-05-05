package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFormatSessionFootprint(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{512, "512 B"},
		{1536, "1.5 KB"},
		{130 * 1024 * 1024, "130.0 MB"},
	}

	for _, tt := range tests {
		if got := formatSessionFootprint(tt.bytes); got != tt.want {
			t.Errorf("formatSessionFootprint(%d) = %q; want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestSessionFootprintBytesIncludesSidecarDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session-id.jsonl")
	sidecar := filepath.Join(dir, "session-id", "subagents")
	if err := os.MkdirAll(sidecar, 0o755); err != nil {
		t.Fatalf("mkdir sidecar: %v", err)
	}
	if err := os.WriteFile(path, []byte("12345"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sidecar, "agent.jsonl"), []byte("1234567"), 0o600); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}

	if got := sessionFootprintBytes(path); got != 12 {
		t.Errorf("sessionFootprintBytes() = %d; want transcript + sidecar bytes", got)
	}
}
