package sessionsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExternalSource describes a foreign session directory to import.
type ExternalSource struct {
	Name    string // "claude-code", "codex", etc.
	RootDir string // e.g. ~/.claude/projects or ~/.codex/sessions
	Format  string // "claude-code" | "codex"
}

// DefaultSources returns the standard external sources to import from.
func DefaultSources() []ExternalSource {
	home, _ := os.UserHomeDir()
	return []ExternalSource{
		{
			Name:    "claude-code",
			RootDir: filepath.Join(home, ".claude", "projects"),
			Format:  "claude-code",
		},
		{
			Name:    "codex",
			RootDir: filepath.Join(home, ".codex", "sessions"),
			Format:  "codex",
		},
	}
}

// ImportExternal indexes sessions from src into db, using src.Format to parse.
// Sessions are tagged with a synthetic project_slug like "cc:<original-slug>" or
// "codex:<date-from-filename>". Only re-imports files whose mtime has changed
// (uses the same indexed_at tracking as IndexAll). Non-fatal: malformed files
// and individual parse errors are silently skipped.
func (db *DB) ImportExternal(src ExternalSource) error {
	if _, err := os.Stat(src.RootDir); os.IsNotExist(err) {
		return nil
	}

	switch src.Format {
	case "claude-code":
		return db.importClaudeCode(src.RootDir)
	case "codex":
		return db.importCodex(src.RootDir)
	default:
		return fmt.Errorf("sessionsearch: unknown external format %q", src.Format)
	}
}

// importClaudeCode walks rootDir/<slug>/*.jsonl, parses CC JSONL files
// (both the flat "message" format and the branching uuid/parentUuid format),
// and indexes user/assistant messages with project_slug = "cc:" + slug.
func (db *DB) importClaudeCode(rootDir string) error {
	entries, err := os.ReadDir(rootDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("sessionsearch: cc readdir %s: %w", rootDir, err)
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		slug := "cc:" + e.Name()
		dir := filepath.Join(rootDir, e.Name())
		_ = db.importClaudeCodeDir(dir, slug) // best-effort per project
	}
	return nil
}

func (db *DB) importClaudeCodeDir(dir, projectSlug string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("sessionsearch: cc readdir %s: %w", dir, err)
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		sessionID := "cc:" + strings.TrimSuffix(e.Name(), ".jsonl")
		mtime := info.ModTime().UnixMilli()

		var storedAt int64
		row := db.conn.QueryRowContext(context.Background(),
			`SELECT indexed_at FROM sessions WHERE id = ?`, sessionID)
		_ = row.Scan(&storedAt)
		if storedAt >= mtime {
			continue
		}

		_ = db.indexCCFile(sessionID, projectSlug, path, mtime) // best-effort per file
	}
	return nil
}

// ccEntry is the union of CC JSONL line shapes.
type ccEntry struct {
	// Flat "message" format (older / conduit sessions)
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message"`

	// Branching format fields
	UUID        string `json:"uuid"`
	ParentUUID  string `json:"parentUuid"`
	IsSidechain bool   `json:"isSidechain"`

	// last-prompt metadata
	LeafUUID string `json:"leafUuid"`
}

type ccMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ccNode is an in-memory node in the CC branching tree.
type ccNode struct {
	parent      string
	role        string
	isSidechain bool
	content     json.RawMessage
}

// indexCCFile parses a CC JSONL file and inserts the main-path user/assistant
// messages into the FTS index. It handles both the flat "message" format and
// the branching uuid/parentUuid format detected by the presence of uuid fields.
func (db *DB) indexCCFile(sessionID, projectSlug, path string, mtime int64) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("sessionsearch: cc read %s: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")

	// First pass: detect format and collect nodes.
	nodes := make(map[string]*ccNode)
	var lastLeaf string
	hasBranching := false
	var flatMessages []struct {
		role    string
		content json.RawMessage
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry ccEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}

		// Track the last-prompt leaf UUID.
		if entry.Type == "last-prompt" && entry.LeafUUID != "" {
			lastLeaf = entry.LeafUUID
			continue
		}

		// Branching format: node has a uuid.
		if entry.UUID != "" {
			hasBranching = true
			if entry.Type != "user" && entry.Type != "assistant" {
				// Non-message node: add to graph so parent links resolve.
				nodes[entry.UUID] = &ccNode{
					parent:      entry.ParentUUID,
					isSidechain: entry.IsSidechain,
				}
				continue
			}
			// Message node: extract role + content from .message.
			var msg ccMessage
			if err := json.Unmarshal(entry.Message, &msg); err != nil {
				continue
			}
			role := msg.Role
			if role == "" {
				role = entry.Type
			}
			if role != "user" && role != "assistant" {
				continue
			}
			nodes[entry.UUID] = &ccNode{
				parent:      entry.ParentUUID,
				role:        role,
				isSidechain: entry.IsSidechain,
				content:     msg.Content,
			}
			continue
		}

		// Flat format: type="message" with a .message field.
		if entry.Type == "message" && len(entry.Message) > 0 {
			var msg ccMessage
			if err := json.Unmarshal(entry.Message, &msg); err != nil {
				continue
			}
			if msg.Role != "user" && msg.Role != "assistant" {
				continue
			}
			flatMessages = append(flatMessages, struct {
				role    string
				content json.RawMessage
			}{
				role: msg.Role, content: msg.Content,
			})
		}
	}

	tx, err := db.conn.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("sessionsearch: cc begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(context.Background(),
		`DELETE FROM messages WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("sessionsearch: cc delete messages %s: %w", sessionID, err)
	}

	msgIndex := 0

	if hasBranching {
		// Walk from lastLeaf back to root, collecting the main-path messages.
		// Reverse so we end up in chronological order.
		var path2 []*ccNode
		cur := lastLeaf
		seen := make(map[string]struct{})
		for cur != "" {
			if _, already := seen[cur]; already {
				break
			}
			seen[cur] = struct{}{}
			n, ok := nodes[cur]
			if !ok {
				break
			}
			path2 = append(path2, n)
			cur = n.parent
		}
		// Reverse to chronological order.
		for i, j := 0, len(path2)-1; i < j; i, j = i+1, j-1 {
			path2[i], path2[j] = path2[j], path2[i]
		}
		for _, n := range path2 {
			if n.isSidechain || n.role == "" {
				continue
			}
			text := extractText(n.content)
			if text == "" {
				continue
			}
			if _, err := tx.ExecContext(context.Background(),
				`INSERT INTO messages(session_id, project_slug, msg_index, role, content) VALUES (?, ?, ?, ?, ?)`,
				sessionID, projectSlug, msgIndex, n.role, text,
			); err != nil {
				return fmt.Errorf("sessionsearch: cc insert message: %w", err)
			}
			msgIndex++
		}
	} else {
		// Flat format: simply iterate in order.
		for _, m := range flatMessages {
			text := extractText(m.content)
			if text == "" {
				continue
			}
			if _, err := tx.ExecContext(context.Background(),
				`INSERT INTO messages(session_id, project_slug, msg_index, role, content) VALUES (?, ?, ?, ?, ?)`,
				sessionID, projectSlug, msgIndex, m.role, text,
			); err != nil {
				return fmt.Errorf("sessionsearch: cc insert message: %w", err)
			}
			msgIndex++
		}
	}

	if _, err := tx.ExecContext(context.Background(),
		`INSERT OR REPLACE INTO sessions(id, project_slug, path, indexed_at, message_count) VALUES (?, ?, ?, ?, ?)`,
		sessionID, projectSlug, path, mtime, msgIndex,
	); err != nil {
		return fmt.Errorf("sessionsearch: cc upsert session: %w", err)
	}

	return tx.Commit()
}

// importCodex walks rootDir/**/*.jsonl (matching rollout-*.jsonl),
// extracts user/assistant messages from Codex response_item lines,
// and indexes them with project_slug = "codex:" + date-from-filename.
func (db *DB) importCodex(rootDir string) error {
	return filepath.WalkDir(rootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip unreadable dirs
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasPrefix(name, "rollout-") || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil //nolint:nilerr
		}

		// Derive session ID from filename (strip "rollout-" prefix and ".jsonl" suffix).
		sessionID := "codex:" + strings.TrimSuffix(strings.TrimPrefix(name, "rollout-"), ".jsonl")

		// Extract date from session ID: rollout-YYYY-MM-DD...
		dateStr := ""
		withoutPrefix := strings.TrimPrefix(name, "rollout-")
		if len(withoutPrefix) >= 10 {
			dateStr = withoutPrefix[:10] // "YYYY-MM-DD"
		}
		projectSlug := "codex:" + dateStr

		mtime := info.ModTime().UnixMilli()
		var storedAt int64
		row := db.conn.QueryRowContext(context.Background(),
			`SELECT indexed_at FROM sessions WHERE id = ?`, sessionID)
		_ = row.Scan(&storedAt)
		if storedAt >= mtime {
			return nil
		}

		_ = db.indexCodexFile(sessionID, projectSlug, path, mtime) // best-effort
		return nil
	})
}

// indexCodexFile parses a Codex JSONL file and indexes user/assistant messages.
//
// Codex JSONL line shape:
//
//	{"timestamp":"...","type":"response_item","payload":{"type":"message","role":"user"|"assistant","content":[{"type":"input_text"|"output_text","text":"..."}]}}
//
// Only payload.type == "message" with role "user" or "assistant" are indexed.
// "developer" role lines (system prompts) are skipped. Tool calls / reasoning are skipped.
func (db *DB) indexCodexFile(sessionID, projectSlug, path string, mtime int64) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("sessionsearch: codex read %s: %w", path, err)
	}

	type codexContentBlock struct {
		Type string `json:"type"` // "input_text", "output_text"
		Text string `json:"text"`
	}
	type codexPayload struct {
		Type    string              `json:"type"` // "message", "function_call", "reasoning", …
		Role    string              `json:"role"` // "user", "assistant", "developer"
		Content []codexContentBlock `json:"content"`
	}
	type codexLine struct {
		Type    string       `json:"type"` // "response_item", "session_meta", "event_msg", …
		Payload codexPayload `json:"payload"`
	}

	tx, err := db.conn.BeginTx(context.Background(), nil)
	if err != nil {
		return fmt.Errorf("sessionsearch: codex begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(context.Background(),
		`DELETE FROM messages WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("sessionsearch: codex delete messages %s: %w", sessionID, err)
	}

	msgIndex := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var cl codexLine
		if err := json.Unmarshal([]byte(line), &cl); err != nil {
			continue
		}
		if cl.Type != "response_item" {
			continue
		}
		p := cl.Payload
		if p.Type != "message" {
			continue
		}
		if p.Role != "user" && p.Role != "assistant" {
			continue
		}

		var parts []string
		for _, c := range p.Content {
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		text := strings.Join(parts, "\n")
		if text == "" {
			continue
		}

		if _, err := tx.ExecContext(context.Background(),
			`INSERT INTO messages(session_id, project_slug, msg_index, role, content) VALUES (?, ?, ?, ?, ?)`,
			sessionID, projectSlug, msgIndex, p.Role, text,
		); err != nil {
			return fmt.Errorf("sessionsearch: codex insert message: %w", err)
		}
		msgIndex++
	}

	if _, err := tx.ExecContext(context.Background(),
		`INSERT OR REPLACE INTO sessions(id, project_slug, path, indexed_at, message_count) VALUES (?, ?, ?, ?, ?)`,
		sessionID, projectSlug, path, mtime, msgIndex,
	); err != nil {
		return fmt.Errorf("sessionsearch: codex upsert session: %w", err)
	}

	return tx.Commit()
}
