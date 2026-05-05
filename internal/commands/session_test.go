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

func TestToggleUsageCommand(t *testing.T) {
	reg := New()
	enabled := false
	RegisterSessionCommands(reg, &SessionState{
		GetUsageStatusEnabled: func() bool { return enabled },
		SetUsageStatusEnabled: func(on bool) error {
			enabled = on
			return nil
		},
	})

	res, ok := reg.Dispatch("/toggle-usage")
	if !ok {
		t.Fatal("expected /toggle-usage to dispatch")
	}
	if res.Type != "usage-toggle" || res.Text != "on" || !enabled {
		t.Fatalf("first toggle = %#v enabled=%v; want usage-toggle/on enabled", res, enabled)
	}

	res, ok = reg.Dispatch("/toggle-usage")
	if !ok {
		t.Fatal("expected /toggle-usage to dispatch second time")
	}
	if res.Type != "usage-toggle" || res.Text != "off" || enabled {
		t.Fatalf("second toggle = %#v enabled=%v; want usage-toggle/off disabled", res, enabled)
	}
}
