// Package sessionsearchtool implements the session_search tool, which
// provides full-text search over Conduit's JSONL session transcripts via
// a SQLite FTS5 index maintained by internal/sessionsearch.
//
// Three modes:
//
//	DISCOVERY — provide query; returns matching message windows across all projects
//	SCROLL    — provide session_id + around_message_index; returns context
//	BROWSE    — no args; lists recent sessions with previews
package sessionsearchtool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/icehunter/conduit/internal/sessionsearch"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tool"
)

// Tool implements the session_search tool.
type Tool struct {
	tool.NotDeferrable

	mu   sync.Mutex
	db   *sessionsearch.DB
	dbOk bool // true once db is open and initial index ran
}

// New returns a SessionSearch tool.
func New() *Tool {
	return &Tool{}
}

func (*Tool) Name() string { return "session_search" }

func (*Tool) Description() string {
	return "Search past session transcripts across all projects. " +
		"Three modes: " +
		"DISCOVERY (provide query) — full-text search across all indexed sessions; " +
		"SCROLL (provide session_id + around_message_index) — fetch messages around a specific point; " +
		"BROWSE (no args) — list recent sessions with previews."
}

func (*Tool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {
				"type": "string",
				"description": "Full-text search query (DISCOVERY mode)"
			},
			"session_id": {
				"type": "string",
				"description": "Session ID to scroll within (SCROLL mode)"
			},
			"around_message_index": {
				"type": "integer",
				"description": "Message index to center the scroll window on (SCROLL mode)",
				"default": 0
			},
			"max_results": {
				"type": "integer",
				"description": "Maximum number of results to return (default 5)",
				"default": 5
			},
			"project": {
				"type": "string",
				"description": "Optional project slug to restrict search to a single project. Empty or omitted means all projects."
			}
		}
	}`)
}

func (*Tool) IsReadOnly(json.RawMessage) bool        { return true }
func (*Tool) IsConcurrencySafe(json.RawMessage) bool { return false }

// Input is the typed view of the JSON input.
type Input struct {
	Query              string `json:"query"`
	SessionID          string `json:"session_id"`
	AroundMessageIndex int    `json:"around_message_index"`
	MaxResults         int    `json:"max_results"`
	Project            string `json:"project"`
}

// Execute dispatches to DISCOVERY, SCROLL, or BROWSE based on the input.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (tool.Result, error) {
	var in Input
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.ErrorResult(fmt.Sprintf("session_search: invalid input: %v", err)), nil
	}
	if in.MaxResults <= 0 {
		in.MaxResults = 5
	}

	db, err := t.openDB()
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("session_search: open db: %v", err)), nil
	}

	switch {
	case in.SessionID != "":
		return t.scroll(db, in)
	case strings.TrimSpace(in.Query) != "":
		return t.discover(db, in)
	default:
		return t.browse(db, in)
	}
}

// openDB lazily opens the SQLite database and runs an initial cross-project
// index pass. Subsequent calls return the cached *DB. Thread-safe.
func (t *Tool) openDB() (*sessionsearch.DB, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.dbOk {
		return t.db, nil
	}

	dbPath := filepath.Join(settings.ConduitDir(), "session-search.db")
	db, err := sessionsearch.Open(dbPath)
	if err != nil {
		return nil, err
	}

	// Index all projects under ~/.conduit/projects/ in one pass.
	// Best-effort; don't fail if some directories are missing or empty.
	_ = db.IndexAll(settings.ConduitDir())

	t.db = db
	t.dbOk = true
	return db, nil
}

// discover runs FTS5 search and formats the matching windows.
func (t *Tool) discover(db *sessionsearch.DB, in Input) (tool.Result, error) {
	windows, err := db.Search(in.Query, in.Project, in.MaxResults)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("session_search: search failed: %v", err)), nil
	}
	if len(windows) == 0 {
		if in.Project != "" {
			return tool.TextResult(fmt.Sprintf("No session transcripts in project %q matched %q.", in.Project, in.Query)), nil
		}
		return tool.TextResult(fmt.Sprintf("No session transcripts matched %q.", in.Query)), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Session search results for %q (%d window(s)):\n\n", in.Query, len(windows))
	for _, w := range windows {
		writeWindow(&sb, w)
	}
	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}

// scroll fetches messages around a specific index in a session.
func (t *Tool) scroll(db *sessionsearch.DB, in Input) (tool.Result, error) {
	w, err := db.Scroll(in.SessionID, in.AroundMessageIndex, 3)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("session_search: scroll failed: %v", err)), nil
	}
	if w == nil {
		return tool.TextResult(fmt.Sprintf("No messages found in session %q around index %d.", in.SessionID, in.AroundMessageIndex)), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Session context for %s around message %d:\n\n", in.SessionID, in.AroundMessageIndex)
	writeWindow(&sb, *w)
	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}

// browse lists recent sessions across all projects.
func (t *Tool) browse(db *sessionsearch.DB, in Input) (tool.Result, error) {
	summaries, err := db.Browse(in.MaxResults)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("session_search: browse failed: %v", err)), nil
	}
	if len(summaries) == 0 {
		return tool.TextResult("No indexed sessions found. Sessions are indexed on first tool use."), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Recent sessions (%d):\n\n", len(summaries))
	for _, s := range summaries {
		fmt.Fprintf(&sb, "  Session: %s\n", s.SessionID)
		if s.ProjectSlug != "" {
			fmt.Fprintf(&sb, "  Project: %s\n", s.ProjectSlug)
		}
		fmt.Fprintf(&sb, "  Date:    %s\n", s.Date.Format(time.RFC3339))
		fmt.Fprintf(&sb, "  Messages: %d\n", s.MessageCount)
		if s.Preview != "" {
			fmt.Fprintf(&sb, "  Preview: %s\n", s.Preview)
		}
		sb.WriteString("\n")
	}
	return tool.TextResult(strings.TrimRight(sb.String(), "\n")), nil
}

// writeWindow appends a formatted MessageWindow to sb.
func writeWindow(sb *strings.Builder, w sessionsearch.MessageWindow) {
	dateStr := ""
	if !w.SessionDate.IsZero() {
		dateStr = " (" + w.SessionDate.Format(time.RFC3339) + ")"
	}
	projectStr := ""
	if w.ProjectSlug != "" {
		projectStr = " [" + w.ProjectSlug + "]"
	}
	fmt.Fprintf(sb, "--- Session: %s%s%s ---\n", w.SessionID, projectStr, dateStr)
	for _, m := range w.Messages {
		marker := ""
		if m.IsMatch {
			marker = " [MATCH]"
		}
		// Truncate long messages to keep output readable.
		text := m.Text
		const maxMsgLen = 400
		if len([]rune(text)) > maxMsgLen {
			text = string([]rune(text)[:maxMsgLen]) + "…"
		}
		fmt.Fprintf(sb, "  [%d] %s%s: %s\n", m.Index, m.Role, marker, text)
	}
	sb.WriteString("\n")
}
