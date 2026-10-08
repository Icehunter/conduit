package memdir

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/icehunter/conduit/internal/skillusage"
)

// curatorEnv isolates the conduit dir and returns it plus a project dir.
func curatorEnv(t *testing.T) (conduitDir, projectDir string) {
	t.Helper()
	conduitDir = filepath.Join(t.TempDir(), ".conduit")
	t.Setenv("CONDUIT_CONFIG_DIR", conduitDir)
	return conduitDir, t.TempDir()
}

func TestCuratorLock_Exclusive(t *testing.T) {
	_, proj := curatorEnv(t)
	rel, err := tryCuratorLock(proj)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tryCuratorLock(proj); !errors.Is(err, ErrCuratorBusy) {
		t.Fatalf("second lock err = %v, want ErrCuratorBusy", err)
	}
	if shouldRunCurator(proj, CuratorConfig{}, time.Now()) {
		t.Error("shouldRunCurator must be false while the lock is held")
	}
	rel()
	rel2, err := tryCuratorLock(proj)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	rel2()
}

func TestCuratorLock_StaleFileDoesNotBlock(t *testing.T) {
	_, proj := curatorEnv(t)
	// A lock file left by a crashed holder (no live kernel lock behind it).
	lock := filepath.Join(proj, curatorLockFileName)
	if err := os.WriteFile(lock, []byte("2020-01-01T00:00:00Z"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := saveCuratorState(proj, curatorState{LastRun: now.Add(-200 * time.Hour).UTC().Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if !shouldRunCurator(proj, CuratorConfig{}, now) {
		t.Error("stale lock file blocked the curator")
	}
	rel, err := tryCuratorLock(proj)
	if err != nil {
		t.Fatalf("stale lock file not recovered: %v", err)
	}
	rel()
}

func TestRunCurator_BusyReturnsErrAndSkipsAgent(t *testing.T) {
	_, proj := curatorEnv(t)
	rel, err := tryCuratorLock(proj)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	called := false
	err = runCurator(context.Background(), t.TempDir(), proj, CuratorConfig{}, time.Now(),
		func(context.Context, string) (string, error) { called = true; return "", nil })
	if !errors.Is(err, ErrCuratorBusy) || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestRunCurator_LifecycleDefaults14_30_Purge(t *testing.T) {
	conduit, proj := curatorEnv(t)
	now := time.Now()
	day := 24 * time.Hour

	// Write the usage store directly so LastUsedAt/ArchivedAt are controlled.
	writeStore := func(recs map[string]skillusage.Record) {
		t.Helper()
		data := "{"
		first := true
		for n, r := range recs {
			if !first {
				data += ","
			}
			first = false
			b, _ := json.Marshal(r)
			data += `"` + n + `":` + string(b)
		}
		data += "}"
		if err := os.MkdirAll(conduit, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(conduit, "skill-usage.json"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(name string) string {
		dir := filepath.Join(conduit, "skills", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	staleDir := mk("idle-15d") // past 14 days: stale, stays in place
	freshDir := mk("idle-10d") // under 14 days: untouched
	archDir := mk("idle-31d")  // past 30 days: archived
	archived := filepath.Join(conduit, "skills", ".archive")
	if err := os.MkdirAll(filepath.Join(archived, "old-archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Only directories the ledger recorded as curator archives may be purged.
	skillusage.RecordInfo(skillusage.ActorCurator, skillusage.OpArchive, "old-archive", filepath.Join(archived, "old-archive"), "seed")
	if err := os.MkdirAll(filepath.Join(archived, "recent-archive"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeStore(map[string]skillusage.Record{
		"idle-15d":       {Name: "idle-15d", CreatedBy: "agent", Scope: skillusage.ScopeConduitGlobal, State: "active", LastUsedAt: now.Add(-15 * day)},
		"idle-10d":       {Name: "idle-10d", CreatedBy: "agent", Scope: skillusage.ScopeConduitGlobal, State: "active", LastUsedAt: now.Add(-10 * day)},
		"idle-31d":       {Name: "idle-31d", CreatedBy: "agent", Scope: skillusage.ScopeConduitGlobal, State: "active", LastUsedAt: now.Add(-31 * day)},
		"old-archive":    {Name: "old-archive", CreatedBy: "agent", Scope: skillusage.ScopeConduitGlobal, State: "archived", ArchivedAt: now.Add(-91 * day)},
		"recent-archive": {Name: "recent-archive", CreatedBy: "agent", Scope: skillusage.ScopeConduitGlobal, State: "archived", ArchivedAt: now.Add(-10 * day)},
	})

	var gotActor string
	err := runCurator(context.Background(), t.TempDir(), proj, defaultCuratorConfig, now,
		func(ctx context.Context, _ string) (string, error) {
			gotActor = skillusage.ActorFrom(ctx, "none")
			return "", nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if gotActor != skillusage.ActorCurator {
		t.Errorf("agent ctx actor = %q, want curator", gotActor)
	}

	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if !exists(staleDir) || !exists(freshDir) {
		t.Error("stale/fresh skills must not be moved")
	}
	if exists(archDir) || !exists(filepath.Join(archived, "idle-31d")) {
		t.Error("31-day-idle skill should have moved into .archive")
	}
	if exists(filepath.Join(archived, "old-archive")) {
		t.Error("archive older than retention should be purged")
	}
	if !exists(filepath.Join(archived, "recent-archive")) {
		t.Error("archive within retention must be kept")
	}
	states := map[string]string{}
	for _, r := range skillusage.All() {
		states[r.Name] = r.State
	}
	want := map[string]string{"idle-15d": "stale", "idle-10d": "active", "idle-31d": "archived", "recent-archive": "archived"}
	for n, s := range want {
		if states[n] != s {
			t.Errorf("state[%s] = %q, want %q", n, states[n], s)
		}
	}
	if _, ok := states["old-archive"]; ok {
		t.Error("purged record should be removed from the store")
	}
	// Ledger audit entries for archive and purge.
	ops := map[string]string{}
	for _, e := range skillusage.LedgerList("", 0) {
		if _, seen := ops[e.Skill]; !seen { // newest first
			ops[e.Skill] = e.Op
		}
	}
	if ops["idle-31d"] != skillusage.OpArchive || ops["old-archive"] != skillusage.OpPurge {
		t.Errorf("ledger ops = %v", ops)
	}
}

func TestRunCurator_ReleasesLock(t *testing.T) {
	_, proj := curatorEnv(t)
	if err := runCurator(context.Background(), t.TempDir(), proj, defaultCuratorConfig, time.Now(),
		func(context.Context, string) (string, error) { return "", nil }); err != nil {
		t.Fatal(err)
	}
	rel, err := tryCuratorLock(proj)
	if err != nil {
		t.Fatalf("lock not released: %v", err)
	}
	rel()
}

func TestPurgeArchivedSkill_RejectsUnsafeNames(t *testing.T) {
	conduit, _ := curatorEnv(t)
	victim := filepath.Join(conduit, "skills", "keep")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"", ".", "..", "../keep", "a/b"} {
		purgeArchivedSkill(n)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("unsafe name removed live skill: %v", err)
	}
}

var noopAgent = func(context.Context, string) (string, error) { return "", nil }

func writeUsage(t *testing.T, conduit string, recs ...skillusage.Record) {
	t.Helper()
	m := map[string]skillusage.Record{}
	for _, r := range recs {
		m[r.Name] = r
	}
	b, _ := json.Marshal(m)
	if err := os.MkdirAll(conduit, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(conduit, "skill-usage.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func mkSkill(t *testing.T, root, name string) string {
	t.Helper()
	dir := filepath.Join(root, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A hand-written skill that only ever ran through usage telemetry must survive
// archive (31d) and purge (122d) untouched.
func TestRunCurator_UserSkillNeverArchivedOrPurged(t *testing.T) {
	conduit, proj := curatorEnv(t)
	dir := mkSkill(t, conduit, "mine")
	skillusage.BumpUse("mine")
	now := time.Now()
	for _, off := range []time.Duration{31 * 24 * time.Hour, 122 * 24 * time.Hour, 400 * 24 * time.Hour} {
		if err := runCurator(context.Background(), "", proj, defaultCuratorConfig, now.Add(off), noopAgent); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
			t.Fatalf("user skill gone after +%v: %v", off, err)
		}
	}
}

func TestRunCurator_PurgeOnlyLedgerRecordedArchives(t *testing.T) {
	conduit, proj := curatorEnv(t)
	now := time.Now()
	old := now.Add(-200 * 24 * time.Hour)
	archived := filepath.Join(conduit, "skills", ".archive")
	for _, n := range []string{"recorded", "unrecorded"} {
		if err := os.MkdirAll(filepath.Join(archived, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	skillusage.RecordInfo(skillusage.ActorCurator, skillusage.OpArchive, "recorded", filepath.Join(archived, "recorded"), "seed")
	// A forged/stale entry whose path is outside .archive must be ignored.
	outside := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	skillusage.RecordInfo(skillusage.ActorCurator, skillusage.OpArchive, "unrecorded", outside, "seed")
	writeUsage(t, conduit,
		skillusage.Record{Name: "recorded", CreatedBy: "agent", Scope: skillusage.ScopeConduitGlobal, State: "archived", ArchivedAt: old},
		skillusage.Record{Name: "unrecorded", CreatedBy: "agent", Scope: skillusage.ScopeConduitGlobal, State: "archived", ArchivedAt: old},
	)
	if err := runCurator(context.Background(), "", proj, defaultCuratorConfig, now, noopAgent); err != nil {
		t.Fatal(err)
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	if exists(filepath.Join(archived, "recorded")) {
		t.Error("ledger-recorded archive should be purged")
	}
	if !exists(filepath.Join(archived, "unrecorded")) || !exists(outside) {
		t.Error("unrecorded / out-of-tree directories must survive purge")
	}
}

func TestRunCurator_SnapshotFailureSkipsArchiveAndPurge(t *testing.T) {
	conduit, proj := curatorEnv(t)
	now := time.Now()
	dir := mkSkill(t, conduit, "idle")
	archived := filepath.Join(conduit, "skills", ".archive", "gone")
	if err := os.MkdirAll(archived, 0o755); err != nil {
		t.Fatal(err)
	}
	skillusage.RecordInfo(skillusage.ActorCurator, skillusage.OpArchive, "gone", archived, "seed")
	writeUsage(t, conduit,
		skillusage.Record{Name: "idle", CreatedBy: "agent", Scope: skillusage.ScopeConduitGlobal, State: "active", LastUsedAt: now.Add(-60 * 24 * time.Hour)},
		skillusage.Record{Name: "gone", CreatedBy: "agent", Scope: skillusage.ScopeConduitGlobal, State: "archived", ArchivedAt: now.Add(-200 * 24 * time.Hour)},
	)
	// A file where the backups directory must go makes Snapshot fail.
	if err := os.WriteFile(filepath.Join(conduit, "skill-backups"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ran := false
	if err := runCurator(context.Background(), "", proj, defaultCuratorConfig, now,
		func(context.Context, string) (string, error) { ran = true; return "", nil }); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Error("LLM pass should still run")
	}
	for _, p := range []string{dir, archived} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s removed despite failed snapshot: %v", p, err)
		}
	}
	for _, r := range skillusage.All() {
		if r.Name == "idle" && r.State != "active" {
			t.Errorf("state advanced without a snapshot: %s", r.State)
		}
	}
}

func TestArchiveAgentSkill_KeepsPriorArchive(t *testing.T) {
	conduit, _ := curatorEnv(t)
	prior := filepath.Join(conduit, "skills", ".archive", "dup")
	if err := os.MkdirAll(prior, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prior, "SKILL.md"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	mkSkill(t, conduit, "dup")
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	archiveAgentSkill("dup", now)
	if b, err := os.ReadFile(filepath.Join(prior, "SKILL.md")); err != nil || string(b) != "old" {
		t.Fatalf("prior archive clobbered: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(conduit, "skills", ".archive", "dup.20260304T050607", "SKILL.md")); err != nil {
		t.Errorf("new archive not versioned: %v", err)
	}
	// Same-second collision gets a counter instead of failing.
	mkSkill(t, conduit, "dup")
	archiveAgentSkill("dup", now)
	if _, err := os.Stat(filepath.Join(conduit, "skills", ".archive", "dup.20260304T050607-2", "SKILL.md")); err != nil {
		t.Errorf("collision not handled: %v", err)
	}
}

func TestArchiveAgentSkill_LeavesSymlinkedSkill(t *testing.T) {
	conduit, _ := curatorEnv(t)
	target := t.TempDir()
	if err := os.MkdirAll(filepath.Join(conduit, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(conduit, "skills", "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	archiveAgentSkill("linked", time.Now())
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("symlinked skill was moved: %v", err)
	}
}

func TestRunCurator_SymlinkedClaudeSkillDoesNotBreakSnapshot(t *testing.T) {
	conduit, proj := curatorEnv(t)
	claude := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	if err := os.MkdirAll(filepath.Join(claude, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(claude, "skills", "cua-driver")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	mkSkill(t, conduit, "idle")
	now := time.Now()
	writeUsage(t, conduit, skillusage.Record{Name: "idle", CreatedBy: "agent", Scope: skillusage.ScopeConduitGlobal, State: "active", LastUsedAt: now.Add(-60 * 24 * time.Hour)})
	if err := runCurator(context.Background(), "", proj, defaultCuratorConfig, now, noopAgent); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(conduit, "skills", ".archive", "idle", "SKILL.md")); err != nil {
		t.Errorf("archive should proceed once the snapshot succeeds: %v", err)
	}
}
