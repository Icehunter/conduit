// Package sessionsearch provides cross-session full-text search over
// Conduit's JSONL transcript archives via a SQLite FTS5 index.
//
// The database lives at settings.ConduitDir()+"/session-search.db".
// Indexing is incremental: files whose mtime is unchanged since the last
// index pass are skipped. No LLM calls are made; results are raw message
// windows for the caller to format.
package sessionsearch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id           TEXT    PRIMARY KEY,
    path         TEXT    NOT NULL,
    indexed_at   INTEGER NOT NULL,
    message_count INTEGER NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS messages USING fts5(
    session_id UNINDEXED,
    msg_index  UNINDEXED,
    role       UNINDEXED,
    content,
    tokenize = 'porter ascii'
);
`

// DB wraps a SQLite FTS5 database for cross-session search.
type DB struct {
	conn *sql.DB
}

// Open opens (or creates) the search database at path.
// Use ":memory:" for tests.
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sessionsearch: open %s: %w", path, err)
	}
	conn.SetMaxOpenConns(1) // SQLite is single-writer
	if _, err := conn.ExecContext(context.Background(), schema); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sessionsearch: schema: %w", err)
	}
	return &DB{conn: conn}, nil
}

// Close releases the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// Index incrementally indexes all *.jsonl files under projectDir.
// Files whose mtime is not newer than the stored indexed_at timestamp
// are skipped.
func (db *DB) Index(projectDir string) error {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("sessionsearch: readdir %s: %w", projectDir, err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(projectDir, e.Name())
		sessionID := strings.TrimSuffix(e.Name(), ".jsonl")
		mtime := info.ModTime().UnixMilli()

		// Check if already indexed and current.
		var storedAt int64
		row := db.conn.QueryRowContext(context.Background(),
			`SELECT indexed_at FROM sessions WHERE id = ?`, sessionID)
		_ = row.Scan(&storedAt)
		if storedAt >= mtime {
			continue
		}

		if err := db.indexFile(sessionID, path, mtime); err != nil {
			// Non-fatal: skip bad files, keep going.
			continue
		}
	}
	return nil
}

// indexFile parses a single JSONL transcript and upserts its messages
// into the FTS index.
func (db *DB) indexFile(sessionID, path string, mtime int64) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("sessionsearch: read %s: %w", path, err)
	}

	type rawEntry struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message"`
	}
	type rawMessage struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}

	tx, err := db.conn.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("sessionsearch: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Remove any previously-indexed rows for this session so we can
	// re-index the whole file cleanly (handles rewinds / compaction).
	if _, err := tx.ExecContext(context.Background(),
		`DELETE FROM messages WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("sessionsearch: delete messages %s: %w", sessionID, err)
	}

	msgIndex := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry rawEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Type != "message" && entry.Type != "user" && entry.Type != "assistant" {
			continue
		}
		if len(entry.Message) == 0 {
			continue
		}
		var msg rawMessage
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}
		role := msg.Role
		if role == "" {
			if entry.Type == "user" || entry.Type == "assistant" {
				role = entry.Type
			}
		}
		if role != "user" && role != "assistant" {
			continue
		}

		text := extractText(msg.Content)
		if text == "" {
			continue
		}

		if _, err := tx.ExecContext(context.Background(),
			`INSERT INTO messages(session_id, msg_index, role, content) VALUES (?, ?, ?, ?)`,
			sessionID, msgIndex, role, text,
		); err != nil {
			return fmt.Errorf("sessionsearch: insert message: %w", err)
		}
		msgIndex++
	}

	if _, err := tx.ExecContext(context.Background(),
		`INSERT OR REPLACE INTO sessions(id, path, indexed_at, message_count) VALUES (?, ?, ?, ?)`,
		sessionID, path, mtime, msgIndex,
	); err != nil {
		return fmt.Errorf("sessionsearch: upsert session: %w", err)
	}

	return tx.Commit()
}

// extractText pulls all text-block content from a raw JSON content field.
// Handles both a bare string and an array of content blocks.
func extractText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// Try bare string first.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try array of content blocks.
	var blocks []api.ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case "tool_result":
			if b.ResultContent != "" {
				parts = append(parts, b.ResultContent)
			}
		}
	}
	return strings.Join(parts, "\n")
}

// MessageWindow is a set of messages around a search match or a scroll
// anchor, with context lines on either side.
type MessageWindow struct {
	SessionID   string          `json:"session_id"`
	SessionDate time.Time       `json:"session_date"`
	Messages    []WindowMessage `json:"messages"`
}

// WindowMessage is one message in a window, annotated with whether it
// was the original match.
type WindowMessage struct {
	Index   int    `json:"index"`
	Role    string `json:"role"`
	Text    string `json:"text"`
	IsMatch bool   `json:"is_match"`
}

// SessionSummary is a lightweight descriptor for a recent session.
type SessionSummary struct {
	SessionID    string    `json:"session_id"`
	Date         time.Time `json:"date"`
	MessageCount int       `json:"message_count"`
	Preview      string    `json:"preview"`
}

// Search runs a full-text query and returns up to maxResults windows,
// each containing the matched message and up to 3 surrounding messages.
func (db *DB) Search(query string, maxResults int) ([]MessageWindow, error) {
	if maxResults <= 0 {
		maxResults = 5
	}
	rows, err := db.conn.QueryContext(context.Background(),
		`SELECT session_id, msg_index FROM messages WHERE content MATCH ? ORDER BY rank LIMIT ?`,
		query, maxResults,
	)
	if err != nil {
		return nil, fmt.Errorf("sessionsearch: fts query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type match struct {
		sessionID string
		msgIndex  int
	}
	var matches []match
	for rows.Next() {
		var m match
		if err := rows.Scan(&m.sessionID, &m.msgIndex); err != nil {
			continue
		}
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sessionsearch: scan matches: %w", err)
	}

	var windows []MessageWindow
	for _, m := range matches {
		w, err := db.Scroll(m.sessionID, m.msgIndex, 3)
		if err != nil || w == nil {
			continue
		}
		// Mark the specific match message.
		for i := range w.Messages {
			if w.Messages[i].Index == m.msgIndex {
				w.Messages[i].IsMatch = true
			}
		}
		windows = append(windows, *w)
	}
	return windows, nil
}

// Scroll returns a window of messages around aroundMsgIndex in the given
// session. windowSize controls how many messages appear on each side.
func (db *DB) Scroll(sessionID string, aroundMsgIndex int, windowSize int) (*MessageWindow, error) {
	if windowSize <= 0 {
		windowSize = 3
	}
	lo := aroundMsgIndex - windowSize
	if lo < 0 {
		lo = 0
	}
	hi := aroundMsgIndex + windowSize

	rows, err := db.conn.QueryContext(context.Background(),
		`SELECT msg_index, role, content FROM messages
		 WHERE session_id = ? AND msg_index >= ? AND msg_index <= ?
		 ORDER BY msg_index`,
		sessionID, lo, hi,
	)
	if err != nil {
		return nil, fmt.Errorf("sessionsearch: scroll query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var msgs []WindowMessage
	for rows.Next() {
		var wm WindowMessage
		if err := rows.Scan(&wm.Index, &wm.Role, &wm.Text); err != nil {
			continue
		}
		msgs = append(msgs, wm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sessionsearch: scroll scan: %w", err)
	}
	if len(msgs) == 0 {
		return nil, nil
	}

	sessionDate := db.sessionDate(sessionID)
	return &MessageWindow{
		SessionID:   sessionID,
		SessionDate: sessionDate,
		Messages:    msgs,
	}, nil
}

// Browse returns a summary of the most recent sessions, ordered by
// indexed_at descending.
func (db *DB) Browse(maxSessions int) ([]SessionSummary, error) {
	if maxSessions <= 0 {
		maxSessions = 10
	}
	rows, err := db.conn.QueryContext(context.Background(),
		`SELECT s.id, s.indexed_at, s.message_count,
		        COALESCE((SELECT content FROM messages WHERE session_id = s.id AND msg_index = 0 LIMIT 1), '')
		 FROM sessions s
		 ORDER BY s.indexed_at DESC
		 LIMIT ?`,
		maxSessions,
	)
	if err != nil {
		return nil, fmt.Errorf("sessionsearch: browse query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var summaries []SessionSummary
	for rows.Next() {
		var (
			id           string
			indexedAtMs  int64
			messageCount int
			preview      string
		)
		if err := rows.Scan(&id, &indexedAtMs, &messageCount, &preview); err != nil {
			continue
		}
		summaries = append(summaries, SessionSummary{
			SessionID:    id,
			Date:         time.UnixMilli(indexedAtMs).UTC(),
			MessageCount: messageCount,
			Preview:      truncatePreview(preview, 200),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sessionsearch: browse scan: %w", err)
	}
	return summaries, nil
}

// sessionDate returns the indexed_at time for the session, or zero time
// if the session is not in the sessions table.
func (db *DB) sessionDate(sessionID string) time.Time {
	var ms int64
	row := db.conn.QueryRowContext(context.Background(),
		`SELECT indexed_at FROM sessions WHERE id = ?`, sessionID)
	if err := row.Scan(&ms); err != nil {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// truncatePreview shortens s to at most maxLen runes.
func truncatePreview(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "…"
}
