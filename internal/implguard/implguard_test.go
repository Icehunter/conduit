package implguard

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestNilGuard(t *testing.T) {
	var g *Guard
	if on, _ := g.Active(); on {
		t.Error("nil guard active")
	}
	g.Record("/x")
	if g.Allowed("/x") {
		t.Error("nil guard allowed a path")
	}
	if g.Touched() != nil {
		t.Error("nil guard has touched paths")
	}
}

func TestActive(t *testing.T) {
	tests := []struct {
		name      string
		fn        func() (bool, string)
		wantOn    bool
		wantLabel string
	}{
		{"nil func", nil, false, ""},
		{"inactive", func() (bool, string) { return false, "x" }, false, "x"},
		{"active", func() (bool, string) { return true, "mcp.local" }, true, "mcp.local"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			on, label := New(tt.fn).Active()
			if on != tt.wantOn || label != tt.wantLabel {
				t.Errorf("Active() = %v, %q", on, label)
			}
		})
	}
}

func TestRecordAllowed(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	existing := filepath.Join(real, "a.go")
	if err := os.WriteFile(existing, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	g := New(nil)
	g.Record(existing, filepath.Join(real, "new.go"), "")

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"exact", existing, true},
		{"through symlink", filepath.Join(link, "a.go"), true},
		{"unclean", filepath.Join(real, "sub", "..", "a.go"), true},
		{"not yet existing, via symlink", filepath.Join(link, "new.go"), true},
		{"other file", filepath.Join(real, "b.go"), false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := g.Allowed(tt.path); got != tt.want {
				t.Errorf("Allowed(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
	if got := len(g.Touched()); got != 2 {
		t.Errorf("Touched() has %d entries, want 2", got)
	}
}

func TestTouchedSorted(t *testing.T) {
	g := New(nil)
	dir := t.TempDir()
	g.Record(filepath.Join(dir, "b"), filepath.Join(dir, "a"))
	got := g.Touched()
	if len(got) != 2 || got[0] > got[1] {
		t.Errorf("Touched() = %v, want sorted", got)
	}
	got[0] = "mutated"
	if g.Touched()[0] == "mutated" {
		t.Error("Touched() returned internal storage")
	}
}

func TestConcurrent(t *testing.T) {
	g := New(nil)
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		p := filepath.Join(dir, fmt.Sprintf("f%d", i))
		go func() { defer wg.Done(); g.Record(p) }()
		go func() { defer wg.Done(); _ = g.Allowed(p); _ = g.Touched() }()
	}
	wg.Wait()
	if got := len(g.Touched()); got != 8 {
		t.Errorf("Touched() has %d entries, want 8", got)
	}
}

func TestFallbackNoticeOncePerOutage(t *testing.T) {
	var down bool
	g := NewStatus(func() Status { return Status{Active: !down, Label: "x", Unreachable: down} })
	steps := []struct {
		down bool
		want string
	}{
		{false, ""},
		{true, FallbackNotice},
		{true, ""},
		{false, ""},
		{true, FallbackNotice},
	}
	for i, st := range steps {
		down = st.down
		g.Active()
		if got := g.TakeFallbackNotice(); got != st.want {
			t.Fatalf("step %d: notice = %q, want %q", i, got, st.want)
		}
	}
	var nilGuard *Guard
	if nilGuard.TakeFallbackNotice() != "" {
		t.Error("nil guard produced a notice")
	}
}
