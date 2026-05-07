package memdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func isolateConduitConfig(t *testing.T) {
	t.Helper()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(t.TempDir(), ".conduit"))
}

func TestScanMemories_Empty(t *testing.T) {
	isolateConduitConfig(t)
	cwd := t.TempDir()
	files, err := ScanMemories(cwd)
	if err != nil {
		t.Fatalf("ScanMemories: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected empty; got %d files", len(files))
	}
}

func TestScanMemories_FindsFiles(t *testing.T) {
	isolateConduitConfig(t)
	cwd := t.TempDir()
	dir := Path(cwd)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "user_role.md"), []byte("user is a developer"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "feedback_test.md"), []byte("feedback content"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, EntrypointName), []byte("# index"), 0o644) // should be skipped

	files, err := ScanMemories(cwd)
	if err != nil {
		t.Fatalf("ScanMemories: %v", err)
	}
	if len(files) != 2 {
		t.Errorf("expected 2 files (MEMORY.md skipped); got %d", len(files))
	}
}

func TestInferMemoryType(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"user_role.md", "user"},
		{"feedback_tdd.md", "feedback"},
		{"project_auth.md", "project"},
		{"reference_grafana.md", "reference"},
		{"random.md", "unknown"},
	}
	for _, tt := range tests {
		got := inferMemoryType(tt.name)
		if got != tt.want {
			t.Errorf("inferMemoryType(%q) = %q; want %q", tt.name, got, tt.want)
		}
	}
}

func TestFormatMemoryList_Empty(t *testing.T) {
	out := FormatMemoryList(nil)
	if !strings.Contains(out, "No memory files") {
		t.Errorf("empty list: %q", out)
	}
}

func TestFormatMemoryList_WithFiles(t *testing.T) {
	files := []MemoryFile{
		{Name: "user_role.md", Type: "user", ModTime: time.Now().Add(-2 * time.Hour), Size: 100},
		{Name: "feedback_x.md", Type: "feedback", ModTime: time.Now().Add(-30 * time.Minute), Size: 50},
	}
	out := FormatMemoryList(files)
	if !strings.Contains(out, "user_role.md") || !strings.Contains(out, "feedback_x.md") {
		t.Errorf("file names missing: %q", out)
	}
	if !strings.Contains(out, "[user]") || !strings.Contains(out, "[feedback]") {
		t.Errorf("types missing: %q", out)
	}
}

func TestRelevantMemories_NoKeywords(t *testing.T) {
	isolateConduitConfig(t)
	cwd := t.TempDir()
	dir := Path(cwd)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "user_role.md"), []byte("developer"), 0o644)

	files, err := RelevantMemories(cwd, nil)
	if err != nil {
		t.Fatalf("RelevantMemories: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("expected 1; got %d", len(files))
	}
}

func TestRelevantMemories_MatchKeyword(t *testing.T) {
	isolateConduitConfig(t)
	cwd := t.TempDir()
	dir := Path(cwd)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "project_auth.md"), []byte("auth middleware rewrite"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "user_role.md"), []byte("developer role"), 0o644)

	files, err := RelevantMemories(cwd, []string{"auth"})
	if err != nil {
		t.Fatalf("RelevantMemories: %v", err)
	}
	if len(files) != 1 || files[0].Name != "project_auth.md" {
		t.Errorf("expected project_auth.md; got %+v", files)
	}
}

func TestFormatAge(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "just now"},
		{5 * time.Minute, "5m ago"},
		{3 * time.Hour, "3h ago"},
		{2 * 24 * time.Hour, "2d ago"},
		{10 * 24 * time.Hour, "1w ago"},
	}
	for _, tt := range tests {
		got := formatAge(tt.d)
		if got != tt.want {
			t.Errorf("formatAge(%v) = %q; want %q", tt.d, got, tt.want)
		}
	}
}
