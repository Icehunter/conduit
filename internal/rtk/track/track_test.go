package track

import (
	"os"
	"testing"
	"time"
)

func TestOpen_InMemory(t *testing.T) {
	db, err := OpenPath(":memory:")
	if err != nil {
		t.Fatalf("OpenPath: %v", err)
	}
	defer db.Close()
}

func TestRecord_And_Gain(t *testing.T) {
	db, err := OpenPath(":memory:")
	if err != nil {
		t.Fatalf("OpenPath: %v", err)
	}
	defer db.Close()

	if err := db.Record(Row{
		Command:       "git log",
		OriginalBytes: 1000,
		FilteredBytes: 200,
		SavedBytes:    800,
		SavedPct:      80.0,
		RecordedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if err := db.Record(Row{
		Command:       "go test ./...",
		OriginalBytes: 500,
		FilteredBytes: 50,
		SavedBytes:    450,
		SavedPct:      90.0,
		RecordedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	totalOrig, totalFiltered, rows, err := db.Gain()
	if err != nil {
		t.Fatalf("Gain: %v", err)
	}
	if rows != 2 {
		t.Errorf("expected 2 rows; got %d", rows)
	}
	if totalOrig != 1500 {
		t.Errorf("expected totalOrig=1500; got %d", totalOrig)
	}
	if totalFiltered != 250 {
		t.Errorf("expected totalFiltered=250; got %d", totalFiltered)
	}
}

func TestOpen_DefaultPath(t *testing.T) {
	// Open() must not fail; uses a temp-like path.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	db, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.Close()
	// Verify file was created.
	if _, err := os.Stat(tmp + "/.local/share/rtk/history.db"); os.IsNotExist(err) {
		t.Error("expected history.db to be created")
	}
}

func TestGain_Empty(t *testing.T) {
	db, err := OpenPath(":memory:")
	if err != nil {
		t.Fatalf("OpenPath: %v", err)
	}
	defer db.Close()

	totalOrig, totalFiltered, rows, err := db.Gain()
	if err != nil {
		t.Fatalf("Gain: %v", err)
	}
	if rows != 0 || totalOrig != 0 || totalFiltered != 0 {
		t.Errorf("empty db should return zeros; got orig=%d filtered=%d rows=%d", totalOrig, totalFiltered, rows)
	}
}
