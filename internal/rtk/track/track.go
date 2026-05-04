// Package track records RTK filter statistics in a SQLite database.
//
// Schema mirrors RTK's Rust implementation at rtk/src/core/tracking.rs.
// Database lives at ~/.local/share/rtk/history.db.
package track

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS rtk_history (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	command        TEXT    NOT NULL,
	original_bytes INTEGER NOT NULL,
	filtered_bytes INTEGER NOT NULL,
	saved_bytes    INTEGER NOT NULL,
	saved_pct      REAL    NOT NULL,
	recorded_at    INTEGER NOT NULL
);
`

// Row is one recorded RTK filter application.
type Row struct {
	Command       string
	OriginalBytes int
	FilteredBytes int
	SavedBytes    int
	SavedPct      float64
	RecordedAt    time.Time
}

// DB holds an open connection to the history database.
type DB struct {
	conn *sql.DB
}

// Open opens (creating if necessary) the default history database at
// ~/.local/share/rtk/history.db.
func Open() (*DB, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("track: home dir: %w", err)
	}
	dir := filepath.Join(home, ".local", "share", "rtk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("track: mkdir %s: %w", dir, err)
	}
	return OpenPath(filepath.Join(dir, "history.db"))
}

// OpenPath opens the database at path (use ":memory:" for tests).
func OpenPath(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("track: open %s: %w", path, err)
	}
	conn.SetMaxOpenConns(1) // SQLite is single-writer
	if _, err := conn.ExecContext(context.Background(), schema); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("track: schema: %w", err)
	}
	return &DB{conn: conn}, nil
}

// Record persists one filter application to the database.
func (d *DB) Record(r Row) error {
	ts := r.RecordedAt.UnixMilli()
	if ts == 0 {
		ts = time.Now().UnixMilli()
	}
	_, err := d.conn.ExecContext(context.Background(),
		`INSERT INTO rtk_history (command, original_bytes, filtered_bytes, saved_bytes, saved_pct, recorded_at) VALUES (?, ?, ?, ?, ?, ?)`,
		r.Command, r.OriginalBytes, r.FilteredBytes, r.SavedBytes, r.SavedPct, ts,
	)
	if err != nil {
		return fmt.Errorf("track: record: %w", err)
	}
	return nil
}

// Gain returns aggregate savings statistics across all recorded filter applications.
func (d *DB) Gain() (totalOrig, totalFiltered, rows int, err error) {
	row := d.conn.QueryRowContext(context.Background(),
		`SELECT COALESCE(SUM(original_bytes),0), COALESCE(SUM(filtered_bytes),0), COUNT(*) FROM rtk_history`,
	)
	if err = row.Scan(&totalOrig, &totalFiltered, &rows); err != nil {
		return 0, 0, 0, fmt.Errorf("track: gain: %w", err)
	}
	return totalOrig, totalFiltered, rows, nil
}

// Close closes the database connection.
func (d *DB) Close() error {
	return d.conn.Close()
}
