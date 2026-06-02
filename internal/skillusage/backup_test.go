package skillusage

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// seedSkillsDir creates a minimal skills directory structure.
func seedSkillsDir(t *testing.T, dir string) {
	t.Helper()
	skillDir := filepath.Join(dir, "skills", "test-skill")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatalf("seed skills dir: %v", err)
	}
	content := []byte("# Test Skill\nThis is a test skill.\n")
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), content, 0o600); err != nil {
		t.Fatalf("seed skill file: %v", err)
	}
}

// listTarEntries opens a tar.gz and returns all entry names.
func listTarEntries(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec
	if err != nil {
		t.Fatalf("open archive %s: %v", path, err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		names = append(names, hdr.Name)
	}
	return names
}

func TestSnapshotPrune(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", dir)
	// Point CLAUDE_CONFIG_DIR to a subdir that does NOT have a skills/ dir so
	// Snapshot only archives the conduit-skills source.
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	seedSkillsDir(t, dir)

	// First snapshot.
	id1, err := Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error: %v", err)
	}
	if id1 == "" {
		t.Fatal("Snapshot() returned empty id")
	}

	archivePath := filepath.Join(dir, "skill-backups", id1, "skills.tar.gz")
	if _, statErr := os.Stat(archivePath); statErr != nil {
		t.Fatalf("archive not created at %s: %v", archivePath, statErr)
	}

	entries := listTarEntries(t, archivePath)
	if len(entries) == 0 {
		t.Fatal("archive is empty")
	}

	// Verify that entries contain the conduit-skills prefix.
	found := false
	for _, e := range entries {
		if len(e) > len("conduit-skills/") && e[:len("conduit-skills/")] == "conduit-skills/" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no conduit-skills/ entries found; entries: %v", entries)
	}

	// Second snapshot.
	id2, err := Snapshot()
	if err != nil {
		t.Fatalf("second Snapshot() error: %v", err)
	}
	if id2 == id1 {
		// IDs are timestamp-based; they should differ unless the two Snapshot
		// calls land in the same second (very unlikely but handle gracefully).
		t.Log("warning: two snapshots produced the same id (clock resolution?)")
	}

	backups, err := ListBackups()
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(backups) < 1 {
		t.Fatalf("expected at least 1 backup; got %d", len(backups))
	}

	// Prune to keep only 1.
	PruneBackups(1)

	backupsAfter, err := ListBackups()
	if err != nil {
		t.Fatalf("ListBackups after prune: %v", err)
	}
	if len(backupsAfter) != 1 {
		t.Errorf("after PruneBackups(1) got %d backups; want 1", len(backupsAfter))
	}
}

func TestRollback(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", dir)
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	seedSkillsDir(t, dir)

	originalPath := filepath.Join(dir, "skills", "test-skill", "SKILL.md")
	originalContent, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}

	// Take the snapshot we will roll back to.
	id, err := Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Mutate the file.
	mutated := []byte("mutated content\n")
	if err := os.WriteFile(originalPath, mutated, 0o600); err != nil {
		t.Fatalf("write mutated: %v", err)
	}

	// Rollback to the original snapshot.
	if err := Rollback(id); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	restored, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}

	if string(restored) != string(originalContent) {
		t.Errorf("restored content = %q; want %q", string(restored), string(originalContent))
	}
}

func TestRollbackNotFound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	err := Rollback("nonexistent-id")
	if err == nil {
		t.Fatal("expected error for nonexistent rollback id")
	}
}

func TestListBackupsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", dir)

	backups, err := ListBackups()
	if err != nil {
		t.Fatalf("ListBackups on empty dir: %v", err)
	}
	if len(backups) != 0 {
		t.Errorf("expected 0 backups; got %d", len(backups))
	}
}

func TestPruneBackupsNoop(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", dir)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	seedSkillsDir(t, dir)
	if _, err := Snapshot(); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Pruning with keep=5 when only 1 exists: no-op.
	PruneBackups(5)

	backups, err := ListBackups()
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(backups) != 1 {
		t.Errorf("expected 1 backup after no-op prune; got %d", len(backups))
	}
}
