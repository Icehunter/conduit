// Package decisionlog persists agent-authored decisions to an append-only
// JSONL log keyed by project. Future sessions can recall the log so that the
// agent inherits the project's decision history rather than rediscovering
// constraints from scratch.
//
// Conduit-original feature; no Claude Code counterpart.
//
// Storage layout matches the council transcript scheme so a single project
// keeps related artefacts together:
//
//	~/.conduit/projects/<sha256(cwd)[:12]>/decisions/<YYYY-MM-DD>.jsonl
//
// One entry per line; readers tolerate gaps and malformed lines so a partial
// write never corrupts the whole log.
package decisionlog

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Kind classifies the decision. Validated at write time; readers accept any
// non-empty value so old logs survive future enum changes.
type Kind string

const (
	KindChose      Kind = "chose"
	KindRuledOut   Kind = "ruled_out"
	KindConstraint Kind = "constraint"
	KindDiscovery  Kind = "discovery"
)

// Entry is one decision record. Fields are short and opinion-shaped; this is
// a journal of judgement calls, not a diff store.
type Entry struct {
	Timestamp      time.Time `json:"ts"`
	SessionID      string    `json:"session_id,omitempty"`
	Kind           Kind      `json:"kind"`
	Scope          string    `json:"scope"`
	Summary        string    `json:"summary"`
	Why            string    `json:"why,omitempty"`
	Files          []string  `json:"files,omitempty"`
	RelatedCouncil string    `json:"related_council,omitempty"`
	RelatedPR      string    `json:"related_pr,omitempty"`
}

// Caps prevent a runaway agent from filling disk with one decision call. Long
// values are truncated rather than rejected so the agent's intent survives.
const (
	maxSummaryLen = 240
	maxWhyLen     = 600
	maxScopeLen   = 120
	maxFiles      = 10
)

// ErrEmptySummary is returned when an entry has no summary text.
var ErrEmptySummary = errors.New("decisionlog: summary is required")

// projectKey hashes cwd the same way council transcripts do so a project's
// decisions live next to its debates on disk.
func projectKey(cwd string) string {
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	h := sha256.Sum256([]byte(cwd))
	return fmt.Sprintf("%x", h[:6])
}

// Dir returns the absolute decisions directory for cwd. Created on first
// Append; readers tolerate non-existence.
func Dir(cwd string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		// Fall back to a relative path so we still produce *something*
		// callers can inspect; appends will fail loudly.
		return filepath.Join(".conduit", "projects", projectKey(cwd), "decisions")
	}
	return filepath.Join(home, ".conduit", "projects", projectKey(cwd), "decisions")
}

// pathFor returns the JSONL file for the entry's date (UTC).
func pathFor(cwd string, ts time.Time) string {
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return filepath.Join(Dir(cwd), ts.UTC().Format("2006-01-02")+".jsonl")
}

// appendMu serialises writers in this process. Cross-process concurrency is
// not a goal: conduit assumes one agent per cwd.
var appendMu sync.Mutex

// Append writes one entry to today's log. Validates kind/summary, fills in
// Timestamp if zero, and truncates over-long fields.
func Append(cwd string, e Entry) error {
	if strings.TrimSpace(e.Summary) == "" {
		return ErrEmptySummary
	}
	if e.Kind == "" {
		e.Kind = KindChose
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	} else {
		e.Timestamp = e.Timestamp.UTC()
	}
	e.Summary = truncate(e.Summary, maxSummaryLen)
	e.Why = truncate(e.Why, maxWhyLen)
	e.Scope = truncate(e.Scope, maxScopeLen)
	if len(e.Files) > maxFiles {
		e.Files = e.Files[:maxFiles]
	}

	dir := Dir(cwd)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("decisionlog: mkdir: %w", err)
	}
	path := pathFor(cwd, e.Timestamp)

	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("decisionlog: marshal: %w", err)
	}

	appendMu.Lock()
	defer appendMu.Unlock()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("decisionlog: open: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("decisionlog: write: %w", err)
	}
	return nil
}

// Recent returns the most recent N entries across all log files, newest first.
// Malformed lines are skipped silently so a single bad write can't poison the
// reader; the loss is logged elsewhere via Append's caller.
func Recent(cwd string, limit int) ([]Entry, error) {
	if limit <= 0 {
		return nil, nil
	}
	files, err := listLogFiles(cwd)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	// Walk newest file first so we exit early once we have `limit` entries.
	sort.Sort(sort.Reverse(sort.StringSlice(files)))

	var collected []Entry
	for _, p := range files {
		entries, err := readFile(p)
		if err != nil {
			continue
		}
		collected = append(collected, entries...)
		if len(collected) >= limit*2 {
			break // we have enough to sort and trim
		}
	}
	sort.Slice(collected, func(i, j int) bool {
		return collected[i].Timestamp.After(collected[j].Timestamp)
	})
	if len(collected) > limit {
		collected = collected[:limit]
	}
	return collected, nil
}

// RelevantToScope returns entries whose scope or summary mentions any keyword
// extracted from `scope`. Token overlap, no embeddings — same approach as
// memdir.RelevantMemories. Newest first, capped at limit (0 = unlimited).
func RelevantToScope(cwd, scope string, limit int) ([]Entry, error) {
	keywords := tokenize(scope)
	if len(keywords) == 0 {
		return Recent(cwd, limit)
	}
	files, err := listLogFiles(cwd)
	if err != nil || len(files) == 0 {
		return nil, err
	}
	var matched []Entry
	for _, p := range files {
		entries, err := readFile(p)
		if err != nil {
			continue
		}
		for _, e := range entries {
			haystack := strings.ToLower(e.Scope + " " + e.Summary + " " + e.Why)
			for _, k := range keywords {
				if strings.Contains(haystack, k) {
					matched = append(matched, e)
					break
				}
			}
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		return matched[i].Timestamp.After(matched[j].Timestamp)
	})
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	return matched, nil
}

// listLogFiles returns absolute paths to every YYYY-MM-DD.jsonl file in the
// decisions directory. Returns (nil, nil) when the directory doesn't exist
// yet — that's the normal first-run state.
func listLogFiles(cwd string) ([]string, error) {
	dir := Dir(cwd)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	return paths, nil
}

// readFile parses one JSONL log file. Malformed lines are skipped; we never
// abort on a single bad line because partial-write recovery matters more than
// strict validation here.
func readFile(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []Entry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	if err := scanner.Err(); err != nil {
		return entries, fmt.Errorf("decisionlog: scan %s: %w", path, err)
	}
	return entries, nil
}

// tokenize extracts lowercased alphanumeric tokens of length ≥ 3. Used for
// keyword overlap matching; intentionally crude.
func tokenize(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ToLower(s)
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() >= 3 {
			out = append(out, b.String())
		}
		b.Reset()
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return out
}

// truncate caps a string at n runes, appending an ellipsis when shortened.
func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

// FormatPromptBlock renders a compact list suitable for direct injection into
// the system prompt. Returns "" when there are no entries.
//
// Format example:
//
//	## Recent project decisions
//	- [chose] auth middleware: pattern B (compliance) — 2 days ago
//	- [ruled_out] handler pattern C: harder to test in isolation — 2 days ago
//
// `byteCap` clamps the rendered size; further entries are dropped silently.
func FormatPromptBlock(entries []Entry, byteCap int) string {
	if len(entries) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("## Recent project decisions\n\n")
	sb.WriteString("These are decisions you (or prior sessions) recorded for this project. Honour them unless the user explicitly overrides; if a decision is now wrong, record a new entry rather than silently diverging.\n\n")
	now := time.Now().UTC()
	for _, e := range entries {
		line := fmt.Sprintf("- [%s] %s: %s", e.Kind, e.Scope, e.Summary)
		if e.Why != "" {
			line += " — " + e.Why
		}
		line += fmt.Sprintf(" (%s)\n", relativeAge(now.Sub(e.Timestamp)))
		if byteCap > 0 && sb.Len()+len(line) > byteCap {
			sb.WriteString("- … older entries elided\n")
			break
		}
		sb.WriteString(line)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func relativeAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	}
}
