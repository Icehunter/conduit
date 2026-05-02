// Package session implements conversation persistence.
//
// Storage layout mirrors the real Claude Code (src/utils/sessionStorage.ts):
//
//	~/.claude/projects/<sanitized-cwd>/<session-id>.jsonl
//
// Each line of the JSONL file is one Entry. On startup we can read any
// previous session's JSONL and restore its message history, enabling
// --continue and /resume.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/api"
)

const maxSanitizedLength = 200

// Entry is one line in the JSONL transcript file.
type Entry struct {
	Type      string          `json:"type"`
	SessionID string          `json:"sessionId"`
	Timestamp int64           `json:"ts,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`
	Summary   string          `json:"summary,omitempty"`
	Title     string          `json:"customTitle,omitempty"`
}

// Session manages the JSONL transcript for one conversation.
type Session struct {
	ID        string
	ProjectDir string
	FilePath  string
}

// New creates a new session rooted at cwd, using sessionID as the file name.
func New(cwd, sessionID string) (*Session, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("session: home dir: %w", err)
	}
	projectsDir := filepath.Join(home, ".claude", "projects")
	sanitized := sanitizePath(cwd)
	projectDir := filepath.Join(projectsDir, sanitized)
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		return nil, fmt.Errorf("session: mkdir %s: %w", projectDir, err)
	}
	return &Session{
		ID:         sessionID,
		ProjectDir: projectDir,
		FilePath:   filepath.Join(projectDir, sessionID+".jsonl"),
	}, nil
}

// Append writes one entry to the JSONL file.
func (s *Session) Append(entry Entry) error {
	entry.SessionID = s.ID
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().UnixMilli()
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("session: marshal: %w", err)
	}
	f, err := os.OpenFile(s.FilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("session: open: %w", err)
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\n", b)
	return err
}

// AppendMessage serializes an api.Message and appends it.
func (s *Session) AppendMessage(msg api.Message) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.Append(Entry{Type: "message", Message: raw})
}

// SetTitle persists a human-readable conversation title.
func (s *Session) SetTitle(title string) error {
	return s.Append(Entry{Type: "custom-title", Title: title})
}

// SetSummary persists a compaction summary.
func (s *Session) SetSummary(summary string) error {
	return s.Append(Entry{Type: "summary", Summary: summary})
}

// Snapshot returns the current message count — used as a rewind point.
// We use the file line count as a proxy. Returns 0 if unavailable.
func (s *Session) Snapshot() int {
	data, err := os.ReadFile(s.FilePath)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "\n")
}

// LoadMessages reads the JSONL file and returns the message history.
func (s *Session) LoadMessages() ([]api.Message, error) {
	return LoadMessages(s.FilePath)
}

// LoadMessages reads a JSONL transcript at path and returns its messages.
func LoadMessages(path string) ([]api.Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("session: read %s: %w", path, err)
	}
	var msgs []api.Message
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry Entry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Type == "message" && len(entry.Message) > 0 {
			var msg api.Message
			if err := json.Unmarshal(entry.Message, &msg); err == nil {
				msgs = append(msgs, msg)
			}
		}
	}
	return msgs, nil
}

// List returns all session IDs for the given cwd, newest first.
func List(cwd string) ([]SessionMeta, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	sanitized := sanitizePath(cwd)
	dir := filepath.Join(home, ".claude", "projects", sanitized)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []SessionMeta
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		info, _ := e.Info()
		mod := time.Time{}
		if info != nil {
			mod = info.ModTime()
		}
		out = append(out, SessionMeta{
			ID:       id,
			FilePath: filepath.Join(dir, e.Name()),
			Modified: mod,
		})
	}
	return out, nil
}

// SessionMeta is a lightweight descriptor for a past session.
type SessionMeta struct {
	ID       string
	FilePath string
	Modified time.Time
	Title    string
}

// sanitizePath converts an arbitrary path to a safe directory name.
// Mirrors sessionStoragePortable.ts sanitizePath + djb2Hash fallback.
func sanitizePath(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	sanitized := b.String()
	if len(sanitized) <= maxSanitizedLength {
		return sanitized
	}
	h := djb2Hash(s)
	suffix := fmt.Sprintf("%x", abs32(h))
	return sanitized[:maxSanitizedLength] + "-" + suffix
}

// djb2Hash mirrors the TS djb2Hash function exactly.
func djb2Hash(s string) int32 {
	var hash int32
	for _, c := range s {
		hash = ((hash << 5) - hash + int32(c))
	}
	return hash
}

func abs32(n int32) int32 {
	if n < 0 {
		return -n
	}
	return n
}
