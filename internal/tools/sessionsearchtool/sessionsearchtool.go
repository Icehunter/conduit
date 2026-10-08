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
	"regexp"
	"strconv"
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

	// lastIndex is when the index was last refreshed; guarded by mu.
	lastIndex time.Time
	// currentSession reports the live session's id; nil when unknown.
	currentSession func() string
	// now and indexAll are seams for tests.
	now      func() time.Time
	indexAll func(db *sessionsearch.DB, conduitDir string) error
}

// reindexInterval is the minimum gap between incremental index refreshes, so
// sessions written (or compacted) since the tool first opened stay searchable
// without re-walking every project on each call.
const reindexInterval = 30 * time.Second

func (t *Tool) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// Option configures a Tool.
type Option func(*Tool)

// WithCurrentSession lets exclude_current_session omit the live session, whose
// transcript otherwise dominates results with the very question being asked.
func WithCurrentSession(id func() string) Option {
	return func(t *Tool) { t.currentSession = id }
}

// New returns a SessionSearch tool.
func New(opts ...Option) *Tool {
	t := &Tool{}
	for _, o := range opts {
		o(t)
	}
	return t
}

func (*Tool) Name() string { return "session_search" }

func (*Tool) Description() string {
	return "Search past session transcripts across all projects. " +
		"Three modes: " +
		"DISCOVERY (provide query) — full-text search across all indexed sessions; " +
		"SCROLL (provide session_id + around_message_index) — fetch messages around a specific point; " +
		"BROWSE (no args) — list recent sessions with previews. " +
		"DISCOVERY and BROWSE accept after/before date bounds and exclude_session_ids / exclude_current_session."
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
			},
			"after": {
				"type": "string",
				"description": "DISCOVERY/BROWSE. Inclusive lower bound on session date: ISO date or datetime (2026-06-01, 2026-06-01T09:00:00Z) or relative (7d, 24h, 2w = within the last N). Use only when a time frame is named."
			},
			"before": {
				"type": "string",
				"description": "DISCOVERY/BROWSE. Exclusive upper bound on session date: ISO date (midnight UTC) or datetime, or relative (7d = older than a week)."
			},
			"exclude_session_ids": {
				"type": "array",
				"items": {"type": "string"},
				"description": "DISCOVERY/BROWSE. Session ids already inspected; they are omitted so a later query explores instead of repeating hits. Cap 20."
			},
			"exclude_current_session": {
				"type": "boolean",
				"description": "DISCOVERY/BROWSE. Omit the currently running session from results."
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

	After                 string   `json:"after"`
	Before                string   `json:"before"`
	ExcludeSessionIDs     []string `json:"exclude_session_ids"`
	ExcludeCurrentSession bool     `json:"exclude_current_session"`
}

// maxRelativeBound is the longest accepted relative date bound.
const maxRelativeBound = 100 * 365 * 24 * time.Hour

var relativeBoundRE = regexp.MustCompile(`^(\d+)\s*([hdw])$`)

// parseBound parses a date bound: a relative duration (7d, 24h, 2w = now minus
// that), an RFC3339 datetime, or a date-only value taken as midnight UTC.
func parseBound(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if m := relativeBoundRE.FindStringSubmatch(strings.ToLower(raw)); m != nil {
		unit := map[string]time.Duration{"h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[m[2]]
		n, err := strconv.ParseInt(m[1], 10, 64)
		// Bound n before multiplying so n*unit cannot overflow a Duration.
		if err != nil || n > int64(maxRelativeBound/unit) {
			return time.Time{}, fmt.Errorf("relative bound %q is out of range (max 100 years)", raw)
		}
		return now.Add(-time.Duration(n) * unit), nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%q is not an ISO date, RFC3339 datetime, or relative duration like 7d", raw)
}

// filter builds the sessionsearch filter from the input.
func (t *Tool) filter(ctx context.Context, in Input) (sessionsearch.Filter, error) {
	f := sessionsearch.Filter{Project: in.Project}
	now := t.clock()
	var err error
	if strings.TrimSpace(in.After) != "" {
		if f.After, err = parseBound(in.After, now); err != nil {
			return f, fmt.Errorf("after: %w", err)
		}
	}
	if strings.TrimSpace(in.Before) != "" {
		if f.Before, err = parseBound(in.Before, now); err != nil {
			return f, fmt.Errorf("before: %w", err)
		}
	}
	seen := map[string]bool{}
	add := func(id string) {
		if id = strings.TrimSpace(id); id != "" && !seen[id] && len(f.ExcludeSessions) < sessionsearch.MaxExcludedSessions {
			seen[id] = true
			f.ExcludeSessions = append(f.ExcludeSessions, id)
		}
	}
	if in.ExcludeCurrentSession {
		// The loop's live id wins: the getter captured at startup goes
		// stale once the session is switched in-process.
		id := tool.SessionIDFromContext(ctx)
		if id == "" && t.currentSession != nil {
			id = t.currentSession()
		}
		if id == "" {
			return f, fmt.Errorf("exclude_current_session: the current session id is unknown; pass exclude_session_ids instead")
		}
		add(id)
	}
	for _, id := range in.ExcludeSessionIDs {
		add(id)
	}
	return f, nil
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

	t.refreshIndex(db)

	if in.SessionID != "" {
		return t.scroll(db, in)
	}
	f, err := t.filter(ctx, in)
	if err != nil {
		return tool.ErrorResult(fmt.Sprintf("session_search: %v", err)), nil
	}
	if strings.TrimSpace(in.Query) != "" {
		return t.discover(db, in, f)
	}
	return t.browse(db, in, f)
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
	_ = t.runIndexAll(db)
	t.lastIndex = t.clock()

	// Index external sources (Claude Code, Codex) — best-effort, non-fatal.
	for _, src := range sessionsearch.DefaultSources() {
		_ = db.ImportExternal(src)
	}

	t.db = db
	t.dbOk = true
	return db, nil
}

func (t *Tool) runIndexAll(db *sessionsearch.DB) error {
	if t.indexAll != nil {
		return t.indexAll(db, settings.ConduitDir())
	}
	return db.IndexAll(settings.ConduitDir())
}

// refreshIndex re-indexes changed session files (IndexAll skips files whose
// mtime is not newer than the stored one) at most once per reindexInterval.
// Best-effort: a failed refresh leaves the existing index in place.
func (t *Tool) refreshIndex(db *sessionsearch.DB) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock()
	if now.Sub(t.lastIndex) < reindexInterval {
		return
	}
	t.lastIndex = now
	_ = t.runIndexAll(db)
}

// discover runs FTS5 search and formats the matching windows.
func (t *Tool) discover(db *sessionsearch.DB, in Input, f sessionsearch.Filter) (tool.Result, error) {
	windows, err := db.SearchFiltered(in.Query, f, in.MaxResults)
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
func (t *Tool) browse(db *sessionsearch.DB, in Input, f sessionsearch.Filter) (tool.Result, error) {
	summaries, err := db.BrowseFiltered(f, in.MaxResults)
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
