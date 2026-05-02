// Package track records RTK filter statistics.
//
// This is a no-op stub — SQLite (modernc.org/sqlite) is not a declared
// dependency of this module. The struct and function signatures match the
// full implementation so callers compile without change if SQLite is added
// later.
package track

import "time"

// Row is one recorded RTK filter application.
type Row struct {
	Command       string
	OriginalBytes int
	FilteredBytes int
	SavedBytes    int
	SavedPct      float64
	RecordedAt    time.Time
}

// DB is a no-op tracker (SQLite not available).
type DB struct{}

// Open returns a no-op DB.
func Open() (*DB, error) { return &DB{}, nil }

// Record is a no-op.
func (d *DB) Record(Row) error { return nil }

// Gain returns zeroed statistics.
func (d *DB) Gain() (totalOrig, totalFiltered, rows int, err error) { return 0, 0, 0, nil }

// Close is a no-op.
func (d *DB) Close() error { return nil }
