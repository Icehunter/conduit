package skillusage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// setupTempDir creates a temporary conduit dir and returns the path.
func setupTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CONDUIT_CONFIG_DIR", dir)
	return dir
}

func TestApplyTransitions(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	staleDays := 30
	archiveDays := 90

	tests := []struct {
		name     string
		record   Record
		wantFrom string
		wantTo   string
		wantLen  int
	}{
		{
			name: "active idle 31 days → stale",
			record: Record{
				Name:       "skill-a",
				CreatedBy:  "agent",
				State:      "active",
				CreatedAt:  now.Add(-32 * 24 * time.Hour),
				LastUsedAt: now.Add(-31 * 24 * time.Hour),
			},
			wantFrom: "active",
			wantTo:   "stale",
			wantLen:  1,
		},
		{
			name: "active idle 91 days → archived directly",
			record: Record{
				Name:       "skill-b",
				CreatedBy:  "agent",
				State:      "active",
				CreatedAt:  now.Add(-92 * 24 * time.Hour),
				LastUsedAt: now.Add(-91 * 24 * time.Hour),
			},
			wantFrom: "active",
			wantTo:   "archived",
			wantLen:  1,
		},
		{
			name: "stale idle 91 days → archived",
			record: Record{
				Name:       "skill-c",
				CreatedBy:  "agent",
				State:      "stale",
				CreatedAt:  now.Add(-92 * 24 * time.Hour),
				LastUsedAt: now.Add(-91 * 24 * time.Hour),
			},
			wantFrom: "stale",
			wantTo:   "archived",
			wantLen:  1,
		},
		{
			name: "pinned idle 100 days → no transition",
			record: Record{
				Name:       "skill-d",
				CreatedBy:  "agent",
				State:      "active",
				Pinned:     true,
				CreatedAt:  now.Add(-101 * 24 * time.Hour),
				LastUsedAt: now.Add(-100 * 24 * time.Hour),
			},
			wantLen: 0,
		},
		{
			name: "user-created idle 100 days → no transition",
			record: Record{
				Name:       "skill-e",
				CreatedBy:  "user",
				State:      "active",
				CreatedAt:  now.Add(-101 * 24 * time.Hour),
				LastUsedAt: now.Add(-100 * 24 * time.Hour),
			},
			wantLen: 0,
		},
		{
			name: "active used yesterday → no transition",
			record: Record{
				Name:       "skill-f",
				CreatedBy:  "agent",
				State:      "active",
				CreatedAt:  now.Add(-200 * 24 * time.Hour),
				LastUsedAt: now.Add(-1 * 24 * time.Hour),
			},
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupTempDir(t)

			store := map[string]Record{tt.record.Name: tt.record}
			if err := saveStore(store); err != nil {
				t.Fatalf("saveStore: %v", err)
			}

			transitions := ApplyTransitions(now, staleDays, archiveDays)

			if len(transitions) != tt.wantLen {
				t.Fatalf("got %d transitions; want %d", len(transitions), tt.wantLen)
			}
			if tt.wantLen == 0 {
				return
			}
			tr := transitions[0]
			if tr.Name != tt.record.Name {
				t.Errorf("transition.Name = %q; want %q", tr.Name, tt.record.Name)
			}
			if tr.From != tt.wantFrom {
				t.Errorf("transition.From = %q; want %q", tr.From, tt.wantFrom)
			}
			if tr.To != tt.wantTo {
				t.Errorf("transition.To = %q; want %q", tr.To, tt.wantTo)
			}
		})
	}
}

func TestRecordCreate(t *testing.T) {
	setupTempDir(t)

	RecordCreate("my-skill", "conduit-global", true)

	store, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	r, ok := store["my-skill"]
	if !ok {
		t.Fatal("record not created")
	}
	if r.State != "active" {
		t.Errorf("State = %q; want active", r.State)
	}
	if r.CreatedBy != "agent" {
		t.Errorf("CreatedBy = %q; want agent", r.CreatedBy)
	}
	origCreatedAt := r.CreatedAt

	// Second call is a no-op — record must not be overwritten.
	RecordCreate("my-skill", "project", false)
	store2, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore2: %v", err)
	}
	r2 := store2["my-skill"]
	if r2.CreatedBy != "agent" {
		t.Errorf("CreatedBy after second create = %q; want agent (no-op)", r2.CreatedBy)
	}
	if !r2.CreatedAt.Equal(origCreatedAt) {
		t.Errorf("CreatedAt changed after no-op second create")
	}
}

func TestBumpUse(t *testing.T) {
	setupTempDir(t)

	t.Run("creates record when missing", func(t *testing.T) {
		setupTempDir(t)

		BumpUse("new-skill")
		store, err := loadStore()
		if err != nil {
			t.Fatalf("loadStore: %v", err)
		}
		r, ok := store["new-skill"]
		if !ok {
			t.Fatal("record not created by BumpUse")
		}
		if r.UseCount != 1 {
			t.Errorf("UseCount = %d; want 1", r.UseCount)
		}
		if r.State != "active" {
			t.Errorf("State = %q; want active", r.State)
		}
		if r.LastUsedAt.IsZero() {
			t.Error("LastUsedAt should be set")
		}
	})

	t.Run("increments existing record", func(t *testing.T) {
		setupTempDir(t)

		BumpUse("skill-x")
		BumpUse("skill-x")
		BumpUse("skill-x")
		store, err := loadStore()
		if err != nil {
			t.Fatalf("loadStore: %v", err)
		}
		if store["skill-x"].UseCount != 3 {
			t.Errorf("UseCount = %d; want 3", store["skill-x"].UseCount)
		}
	})
}

func TestAll(t *testing.T) {
	setupTempDir(t)

	names := []string{"zebra", "apple", "mango"}
	for _, n := range names {
		RecordCreate(n, "project", false)
	}

	records := All()
	if len(records) != 3 {
		t.Fatalf("All() returned %d records; want 3", len(records))
	}
	want := []string{"apple", "mango", "zebra"}
	for i, r := range records {
		if r.Name != want[i] {
			t.Errorf("records[%d].Name = %q; want %q", i, r.Name, want[i])
		}
	}
}

func TestPin(t *testing.T) {
	setupTempDir(t)

	RecordCreate("pinnable", "project", true)

	Pin("pinnable")
	store, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if !store["pinnable"].Pinned {
		t.Error("Pinned should be true after Pin()")
	}

	Unpin("pinnable")
	store, err = loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if store["pinnable"].Pinned {
		t.Error("Pinned should be false after Unpin()")
	}
}

func TestAgentCreatedNames(t *testing.T) {
	setupTempDir(t)

	RecordCreate("agent-skill-1", "project", true)
	RecordCreate("user-skill", "project", false)
	RecordCreate("agent-skill-2", "conduit-global", true)

	names := AgentCreatedNames()
	if len(names) != 2 {
		t.Fatalf("AgentCreatedNames() = %v; want 2 entries", names)
	}
	if names[0] != "agent-skill-1" || names[1] != "agent-skill-2" {
		t.Errorf("AgentCreatedNames() = %v; want [agent-skill-1 agent-skill-2]", names)
	}
}

func TestIsAgentCreated(t *testing.T) {
	setupTempDir(t)

	RecordCreate("by-agent", "project", true)
	RecordCreate("by-user", "project", false)

	if !IsAgentCreated("by-agent") {
		t.Error("IsAgentCreated(by-agent) = false; want true")
	}
	if IsAgentCreated("by-user") {
		t.Error("IsAgentCreated(by-user) = true; want false")
	}
	if IsAgentCreated("nonexistent") {
		t.Error("IsAgentCreated(nonexistent) = true; want false")
	}
}

func TestUpdateScope(t *testing.T) {
	setupTempDir(t)

	RecordCreate("scoped", "project", true)
	UpdateScope("scoped", "conduit-global")

	store, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if store["scoped"].Scope != "conduit-global" {
		t.Errorf("Scope = %q; want conduit-global", store["scoped"].Scope)
	}
}

func TestApplyTransitions_ZeroLastUsedAt(t *testing.T) {
	setupTempDir(t)

	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	// Record with no LastUsedAt — age computed from CreatedAt.
	r := Record{
		Name:      "no-last-used",
		CreatedBy: "agent",
		State:     "active",
		CreatedAt: now.Add(-31 * 24 * time.Hour),
		// LastUsedAt zero
	}
	if err := saveStore(map[string]Record{r.Name: r}); err != nil {
		t.Fatalf("saveStore: %v", err)
	}

	transitions := ApplyTransitions(now, 30, 90)
	if len(transitions) != 1 {
		t.Fatalf("expected 1 transition; got %d", len(transitions))
	}
	if transitions[0].To != "stale" {
		t.Errorf("To = %q; want stale", transitions[0].To)
	}
}

// TestBumpPatch verifies PatchCount increments and State is set to active.
func TestBumpPatch(t *testing.T) {
	setupTempDir(t)

	BumpPatch("patchable")
	store, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	r := store["patchable"]
	if r.PatchCount != 1 {
		t.Errorf("PatchCount = %d; want 1", r.PatchCount)
	}
	if r.State != "active" {
		t.Errorf("State = %q; want active", r.State)
	}
}

// TestBumpView verifies ViewCount increments.
func TestBumpView(t *testing.T) {
	setupTempDir(t)

	BumpView("viewable")
	BumpView("viewable")
	store, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if store["viewable"].ViewCount != 2 {
		t.Errorf("ViewCount = %d; want 2", store["viewable"].ViewCount)
	}
}

// TestLockFileStale verifies that a stale lock file is stolen and operations
// still succeed.
func TestLockFileStale(t *testing.T) {
	dir := setupTempDir(t)

	// Write a lock file with a very old timestamp.
	lp := filepath.Join(dir, ".skill-usage.lock")
	staleNano := time.Now().Add(-60 * time.Second).UnixNano()
	content := "99999 " + itoa64(staleNano)
	if err := os.WriteFile(lp, []byte(content), 0o600); err != nil {
		t.Fatalf("write stale lock: %v", err)
	}

	// BumpUse should still succeed.
	BumpUse("after-stale-lock")
	store, err := loadStore()
	if err != nil {
		t.Fatalf("loadStore: %v", err)
	}
	if store["after-stale-lock"].UseCount != 1 {
		t.Errorf("UseCount = %d; want 1 (operation should succeed despite stale lock)", store["after-stale-lock"].UseCount)
	}
}

func itoa64(n int64) string {
	return int64ToString(n)
}

func int64ToString(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 20)
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
