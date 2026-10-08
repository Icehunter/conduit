package skillusage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// skillPath returns <root>/skills/<name>/<rel> under a fresh temp tree and
// isolates the conduit dir.
func skillPath(t *testing.T, rel string) string {
	t.Helper()
	t.Setenv("CONDUIT_CONFIG_DIR", filepath.Join(t.TempDir(), ".conduit"))
	p := filepath.Join(t.TempDir(), "skills", "s", filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func track(t *testing.T, actor, op, p, content string) {
	t.Helper()
	if err := TrackEdit(actor, op, "s", p, func() error { return os.WriteFile(p, []byte(content), 0o644) }); err != nil {
		t.Fatal(err)
	}
}

func TestActorContext(t *testing.T) {
	if got := ActorFrom(context.Background(), "agent"); got != "agent" {
		t.Errorf("fallback = %q", got)
	}
	if got := ActorFrom(WithActor(context.Background(), ActorCurator), "agent"); got != ActorCurator {
		t.Errorf("ctx actor = %q", got)
	}
	if got := ActorFrom(WithActor(context.Background(), ""), "user"); got != "user" {
		t.Errorf("empty ctx actor should fall back, got %q", got)
	}
}

func TestTrackEditAndList(t *testing.T) {
	p := skillPath(t, "SKILL.md")
	track(t, ActorAgent, OpCreate, p, "v1")
	track(t, ActorCurator, OpUpdate, p, "v2")
	track(t, ActorAgent, OpUpdate, p, "v2") // no-op: identical content, not recorded

	got := LedgerList("s", 0)
	if len(got) != 2 {
		t.Fatalf("entries = %d, want 2: %+v", len(got), got)
	}
	if got[0].Actor != ActorCurator || got[0].Op != OpUpdate || got[1].Op != OpCreate {
		t.Errorf("order/fields wrong (want newest first): %+v", got)
	}
	if got[1].Before != "" || got[0].Before != got[1].After || got[0].After == got[0].Before {
		t.Errorf("hash chain broken: %+v", got)
	}
	if !got[0].Reversible {
		t.Error("SKILL.md edit should be reversible")
	}
	if n := len(LedgerList("other", 0)); n != 0 {
		t.Errorf("filter by skill returned %d", n)
	}
	if n := len(LedgerList("", 1)); n != 1 {
		t.Errorf("limit returned %d", n)
	}
}

func TestTrackEdit_FailedMutateRecordsNothing(t *testing.T) {
	p := skillPath(t, "SKILL.md")
	err := TrackEdit(ActorAgent, OpCreate, "s", p, func() error { return os.ErrPermission })
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("err = %v", err)
	}
	if n := len(LedgerList("", 0)); n != 0 {
		t.Errorf("entries = %d", n)
	}
}

func TestLedgerRollback(t *testing.T) {
	p := skillPath(t, "SKILL.md")
	track(t, ActorAgent, OpCreate, p, "v1")
	track(t, ActorAgent, OpUpdate, p, "v2")
	list := LedgerList("s", 0)
	update, create := list[0], list[1]

	// Rolling back an older edit while a newer one exists is refused.
	if _, err := LedgerRollback(create.ID, ActorUser); err == nil || !strings.Contains(err.Error(), "changed after") {
		t.Fatalf("expected stale refusal, got %v", err)
	}
	if got := read(t, p); got != "v2" {
		t.Fatalf("refused rollback modified file: %q", got)
	}

	if _, err := LedgerRollback(update.ID, ActorUser); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "v1" {
		t.Fatalf("content = %q, want v1", got)
	}
	// The rollback is recorded and itself reversible.
	top := LedgerList("s", 1)[0]
	if top.Op != OpRollback || top.Actor != ActorUser {
		t.Fatalf("rollback entry = %+v", top)
	}
	if _, err := LedgerRollback(top.ID, ActorUser); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "v2" {
		t.Fatalf("undo of rollback = %q, want v2", got)
	}
}

func TestLedgerRollback_CreateRemovesFile(t *testing.T) {
	p := skillPath(t, "SKILL.md")
	track(t, ActorAgent, OpCreate, p, "v1")
	if _, err := LedgerRollback(LedgerList("s", 1)[0].ID, ActorUser); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("file should be gone: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(p)); !os.IsNotExist(err) {
		t.Errorf("empty skill dir should be removed: %v", err)
	}
}

func TestLedgerRollback_RemoveFileRestores(t *testing.T) {
	p := skillPath(t, "references/a.md")
	track(t, ActorAgent, OpWriteFile, p, "ref")
	if err := TrackEdit(ActorAgent, OpRemoveFile, "s", p, func() error { return os.Remove(p) }); err != nil {
		t.Fatal(err)
	}
	if _, err := LedgerRollback(LedgerList("s", 1)[0].ID, ActorUser); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != "ref" {
		t.Errorf("restored = %q", got)
	}
}

func TestLedgerRollback_FailsClosed(t *testing.T) {
	t.Run("unknown id", func(t *testing.T) {
		skillPath(t, "SKILL.md")
		if _, err := LedgerRollback("nope", ActorUser); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("informational entry", func(t *testing.T) {
		skillPath(t, "SKILL.md")
		RecordInfo(ActorCurator, OpArchive, "s", "", "moved")
		e := LedgerList("s", 1)[0]
		if _, err := LedgerRollback(e.ID, ActorUser); err == nil || !strings.Contains(err.Error(), "not reversible") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("missing blob", func(t *testing.T) {
		p := skillPath(t, "SKILL.md")
		track(t, ActorAgent, OpCreate, p, "v1")
		track(t, ActorAgent, OpUpdate, p, "v2")
		e := LedgerList("s", 1)[0]
		if err := os.Remove(filepath.Join(blobsDir(), e.Before)); err != nil {
			t.Fatal(err)
		}
		if _, err := LedgerRollback(e.ID, ActorUser); err == nil {
			t.Fatal("expected error")
		}
		if got := read(t, p); got != "v2" {
			t.Errorf("file modified despite failure: %q", got)
		}
	})
	t.Run("corrupt blob", func(t *testing.T) {
		p := skillPath(t, "SKILL.md")
		track(t, ActorAgent, OpCreate, p, "v1")
		track(t, ActorAgent, OpUpdate, p, "v2")
		e := LedgerList("s", 1)[0]
		write(t, filepath.Join(blobsDir(), e.Before), "tampered")
		if _, err := LedgerRollback(e.ID, ActorUser); err == nil || !strings.Contains(err.Error(), "corrupt") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unsafe path in tampered entry", func(t *testing.T) {
		skillPath(t, "SKILL.md")
		victim := filepath.Join(t.TempDir(), "victim.txt")
		write(t, victim, "keep")
		h, err := storeBlob([]byte("evil"))
		if err != nil {
			t.Fatal(err)
		}
		cur, _ := captureFile(victim)
		if err := appendEntry(LedgerEntry{ID: "x1", Time: time.Now(), Actor: "agent", Op: OpUpdate, Skill: "s",
			Path: victim, Before: h, After: cur, Reversible: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := LedgerRollback("x1", ActorUser); err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Fatalf("err = %v", err)
		}
		if got := read(t, victim); got != "keep" {
			t.Errorf("victim modified: %q", got)
		}
	})
}

func TestTrimBoundsLedgerAndGCsBlobs(t *testing.T) {
	p := skillPath(t, "SKILL.md")
	for _, c := range []string{"v1", "v2", "v3", "v4"} {
		track(t, ActorAgent, OpUpdate, p, c)
	}
	entries := readEntries()
	fi, err := os.Stat(ledgerFile())
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := lockLedger()
	if err != nil {
		t.Fatal(err)
	}
	// Trim to roughly two entries, evaluating GC far in the future so the
	// grace period has elapsed.
	trimLocked(fi.Size()/2, blobMaxBytes, time.Now().Add(2*blobGCGrace))
	unlock()

	left := readEntries()
	if len(left) >= len(entries) || len(left) == 0 {
		t.Fatalf("entries %d -> %d, want fewer but not zero", len(entries), len(left))
	}
	if left[len(left)-1].ID != entries[len(entries)-1].ID {
		t.Error("newest entry must survive a trim")
	}
	live := map[string]bool{}
	for _, e := range left {
		live[e.Before], live[e.After] = true, true
	}
	des, _ := os.ReadDir(blobsDir())
	for _, de := range des {
		if !live[de.Name()] {
			t.Errorf("unreferenced blob %s survived GC", de.Name())
		}
	}
	for h := range live {
		if h == "" {
			continue
		}
		if _, err := readBlob(h); err != nil {
			t.Errorf("live blob lost: %v", err)
		}
	}
}

func TestTrimSparesRecentBlobs(t *testing.T) {
	p := skillPath(t, "SKILL.md")
	for _, c := range []string{"v1", "v2", "v3"} {
		track(t, ActorAgent, OpUpdate, p, c)
	}
	before, _ := os.ReadDir(blobsDir())
	fi, _ := os.Stat(ledgerFile())
	unlock, err := lockLedger()
	if err != nil {
		t.Fatal(err)
	}
	trimLocked(fi.Size()/3, blobMaxBytes, time.Now())
	unlock()
	after, _ := os.ReadDir(blobsDir())
	if len(after) != len(before) {
		t.Errorf("recent blobs collected: %d -> %d", len(before), len(after))
	}
}

func TestSkillFilePathRE(t *testing.T) {
	tests := []struct {
		p  string
		ok bool
	}{
		{"/a/skills/s/SKILL.md", true},
		{"/a/skills/s/references/x.md", true},
		{"/a/skills/s/references/sub/x.md", true},
		{"/a/skills/s/other.md", false},
		{"/a/skills/SKILL.md", false},
		{"/etc/passwd", false},
		{"/a/skills/s/references/", false},
	}
	for _, tt := range tests {
		if got := skillFilePathRE.MatchString(tt.p); got != tt.ok {
			t.Errorf("%s = %v, want %v", tt.p, got, tt.ok)
		}
	}
}

func TestTrimBoundsBlobStorage(t *testing.T) {
	p := skillPath(t, "SKILL.md")
	for _, c := range []string{strings.Repeat("a", 400), strings.Repeat("b", 400), strings.Repeat("c", 400), strings.Repeat("d", 400)} {
		track(t, ActorAgent, OpUpdate, p, c)
	}
	unlock, err := lockLedger()
	if err != nil {
		t.Fatal(err)
	}
	// Ledger size is fine; only the blob budget (~2 blobs) is exceeded.
	trimLocked(ledgerMaxBytes, 1000, time.Now().Add(2*blobGCGrace))
	unlock()
	left := readEntries()
	if len(left) == 0 || len(left) >= 4 {
		t.Fatalf("entries after blob-driven trim = %d, want 1..3", len(left))
	}
	if got := blobsSize(); got > 1000 {
		t.Errorf("blobs = %d bytes, want <= 1000", got)
	}
	if left[len(left)-1].After == "" {
		t.Error("newest entry lost")
	}
}

func TestStoreBlobRefreshesMtime(t *testing.T) {
	skillPath(t, "SKILL.md")
	h, err := storeBlob([]byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(blobsDir(), h), old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := storeBlob([]byte("x")); err != nil {
		t.Fatal(err)
	}
	fi, _ := os.Stat(filepath.Join(blobsDir(), h))
	if time.Since(fi.ModTime()) > time.Minute {
		t.Errorf("mtime not refreshed: %v", fi.ModTime())
	}
}

func TestConcurrentRollbackAppliesOnce(t *testing.T) {
	p := skillPath(t, "SKILL.md")
	track(t, ActorAgent, OpCreate, p, "v1")
	track(t, ActorAgent, OpUpdate, p, "v2")
	id := LedgerList("s", 1)[0].ID
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = LedgerRollback(id, ActorUser)
		}()
	}
	wg.Wait()
	ok := 0
	for _, e := range errs {
		if e == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Errorf("successful rollbacks = %d, want exactly 1 (errs=%v)", ok, errs)
	}
	if got := read(t, p); got != "v1" {
		t.Errorf("content = %q", got)
	}
}
