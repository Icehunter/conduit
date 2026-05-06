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
	Type        string          `json:"type"`
	SessionID   string          `json:"sessionId"`
	Timestamp   int64           `json:"ts,omitempty"`
	UUID        string          `json:"uuid,omitempty"`
	ParentUUID  string          `json:"parentUuid,omitempty"`
	CreatedAt   string          `json:"timestamp,omitempty"`
	IsSidechain bool            `json:"isSidechain,omitempty"`
	Message     json.RawMessage `json:"message,omitempty"`
	Summary     string          `json:"summary,omitempty"`
	Title       string          `json:"customTitle,omitempty"`
	// Mode is set on type="session_settings" entries to persist session-level
	// settings (e.g. "coordinator" or "normal") across resume.
	Mode string `json:"mode,omitempty"`
	// ProviderKind/Provider are conduit-only display metadata for provider
	// routed turns. The embedded API message remains Anthropic-compatible.
	ProviderKind string `json:"providerKind,omitempty"`
	Provider     string `json:"provider,omitempty"`
}

// Session manages the JSONL transcript for one conversation.
type Session struct {
	ID         string
	ProjectDir string
	FilePath   string
}

// SessionMeta is a lightweight descriptor for a past session.
type SessionMeta struct {
	ID       string
	FilePath string
	Modified time.Time
	Title    string
}

// ProjectDir returns the directory where session files for cwd are stored.
func ProjectDir(cwd, home string) string {
	return filepath.Join(home, ".claude", "projects", sanitizePath(cwd))
}

// FromFile wraps an existing JSONL file as a Session so new turns can be appended to it.
func FromFile(filePath string) *Session {
	base := filepath.Base(filePath)
	id := strings.TrimSuffix(base, ".jsonl")
	return &Session{
		ID:         id,
		ProjectDir: filepath.Dir(filePath),
		FilePath:   filePath,
	}
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

// SetMode persists a session-level mode setting (e.g. "coordinator" or "normal").
func (s *Session) SetMode(mode string) error {
	return s.Append(Entry{Type: "session_settings", Mode: mode})
}

// ReadMode scans the session JSONL and returns the most recent mode value
// from a "session_settings" entry. Returns "" if no mode has been set.
func (s *Session) ReadMode() string {
	f, err := os.Open(s.FilePath)
	if err != nil {
		return ""
	}
	defer f.Close()
	mode := ""
	dec := json.NewDecoder(f)
	for {
		var e Entry
		if err := dec.Decode(&e); err != nil {
			break
		}
		if e.Type == "session_settings" && e.Mode != "" {
			mode = e.Mode
		}
	}
	return mode
}

// AppendMessage serializes an api.Message and appends it.
func (s *Session) AppendMessage(msg api.Message) error {
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return s.Append(Entry{
		Type:         "message",
		Message:      raw,
		ProviderKind: msg.ProviderKind,
		Provider:     msg.Provider,
	})
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
