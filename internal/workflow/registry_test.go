package workflow

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeScript(t *testing.T, path, name, desc string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	src := "export const meta = { name: '" + name + "', description: '" + desc + "' }\nreturn 1\n"
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscover(t *testing.T) {
	conduitDir, claudeDir, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	writeScript(t, filepath.Join(conduitDir, "workflows", "shared.js"), "shared", "from conduit")
	writeScript(t, filepath.Join(claudeDir, "workflows", "shared.js"), "shared", "from claude")
	writeScript(t, filepath.Join(cwd, ".claude", "workflows", "shared.js"), "shared", "from cwd")
	writeScript(t, filepath.Join(claudeDir, "workflows", "only-claude.js"), "only-claude", "c")
	if err := os.WriteFile(filepath.Join(claudeDir, "workflows", "broken.js"), []byte("return 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "workflows", "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Symlinked file, as graph-eng's install.sh creates.
	real := filepath.Join(t.TempDir(), "linked.js")
	writeScript(t, real, "linked", "via symlink")
	if err := os.Symlink(real, filepath.Join(claudeDir, "workflows", "linked.js")); err != nil {
		t.Fatal(err)
	}

	got := Discover(cwd)
	byName := map[string]Saved{}
	for _, s := range got {
		byName[s.Name] = s
	}
	if len(got) != 4 {
		t.Fatalf("Discover returned %d entries: %+v", len(got), got)
	}
	if byName["shared"].Meta.Description != "from cwd" {
		t.Errorf("shared = %+v, want cwd to win", byName["shared"])
	}
	if byName["only-claude"].Meta.Description != "c" {
		t.Errorf("only-claude = %+v", byName["only-claude"])
	}
	if byName["linked"].Meta.Description != "via symlink" {
		t.Errorf("linked = %+v", byName["linked"])
	}
	if b := byName["broken"]; b.Err == "" {
		t.Errorf("broken script should be listed with an error: %+v", b)
	}
	if _, ok := byName["notes"]; ok {
		t.Error("non-.js file was discovered")
	}

	if f := Find(cwd, "shared"); f == nil || f.Meta.Description != "from cwd" {
		t.Errorf("Find(shared) = %+v", f)
	}
	if f := Find(cwd, "broken"); f != nil {
		t.Errorf("Find(broken) = %+v, want nil", f)
	}
	if f := Find(cwd, "missing"); f != nil {
		t.Errorf("Find(missing) = %+v, want nil", f)
	}
}

func TestRegistry(t *testing.T) {
	g := NewRegistry(2)
	mk := func(id string, status RunStatus, done time.Time) *Run {
		r := &Run{ID: id, StartedAt: time.Now()}
		r.status = status
		r.doneAt = done
		g.Add(r)
		return r
	}
	base := time.Now()
	mk("a", RunDone, base.Add(1*time.Second))
	mk("b", RunFailed, base.Add(2*time.Second))
	running := mk("r", RunRunning, time.Time{})
	mk("c", RunDone, base.Add(3*time.Second))

	if g.Get("a") != nil {
		t.Error("oldest completed run should have been evicted")
	}
	if g.Get("r") != running {
		t.Error("running run must never be evicted")
	}
	if g.Running() != 1 {
		t.Errorf("Running = %d", g.Running())
	}
	snap := g.Snapshot()
	if len(snap) != 3 || snap[0].ID != "r" || snap[1].ID != "c" || snap[2].ID != "b" {
		ids := make([]string, len(snap))
		for i, s := range snap {
			ids[i] = s.ID
		}
		t.Errorf("Snapshot order = %v, want [r c b]", ids)
	}

	g.KillAll()
	if running.Status() != RunKilled {
		t.Errorf("KillAll left status %s", running.Status())
	}
}
