package workflow

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// JournalEntry is one line of journal.jsonl.
type JournalEntry struct {
	Type   string          `json:"type"` // start | resumed | result | log | final
	At     time.Time       `json:"at"`
	RunID  string          `json:"runId,omitempty"`
	From   string          `json:"from,omitempty"`
	Key    string          `json:"key,omitempty"`
	Occ    int             `json:"occ,omitempty"`
	Label  string          `json:"label,omitempty"`
	Cached bool            `json:"cached,omitempty"`
	Value  json.RawMessage `json:"value,omitempty"`
	Text   string          `json:"text,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Journal records every agent() result of a run so a later run of the same
// script can replay identical calls instead of paying for them again. Calls
// are matched by content key (script hash + prompt + options) and occurrence,
// not by position, so reordering under different timing does not defeat the
// cache. A new journal is always self-contained: replayed hits are written
// back into it.
type Journal struct {
	Path string

	mu    sync.Mutex
	w     *bufio.Writer
	f     *os.File
	prior map[string][]json.RawMessage
}

// OpenJournal creates journal.jsonl under dir for runID.
func OpenJournal(dir, runID string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("workflow: journal: %w", err)
	}
	path := filepath.Join(dir, "journal.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644) //nolint:gosec // run directory is user-owned
	if err != nil {
		return nil, fmt.Errorf("workflow: journal: %w", err)
	}
	j := &Journal{Path: path, f: f, w: bufio.NewWriter(f), prior: map[string][]json.RawMessage{}}
	j.write(JournalEntry{Type: "start", RunID: runID})
	return j, nil
}

// LoadPrior reads result entries from an earlier journal so they can be
// replayed. Entries whose line cannot be parsed are skipped; a missing file
// is an error because a resume without a journal is not a resume.
func (j *Journal) LoadPrior(path, fromRunID string) error {
	entries, err := ReadJournal(path)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, e := range entries {
		if e.Type == "result" && e.Key != "" {
			j.prior[e.Key] = append(j.prior[e.Key], e.Value)
		}
	}
	j.write(JournalEntry{Type: "resumed", From: fromRunID})
	return nil
}

// Replay returns the cached value for the occ-th call with key, recording
// the hit into this journal.
func (j *Journal) Replay(key string, occ int) (json.RawMessage, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	vals := j.prior[key]
	if occ >= len(vals) {
		return nil, false
	}
	j.write(JournalEntry{Type: "result", Key: key, Occ: occ, Cached: true, Value: vals[occ]})
	return vals[occ], true
}

// Record writes a live agent result.
func (j *Journal) Record(key string, occ int, label string, value json.RawMessage) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.write(JournalEntry{Type: "result", Key: key, Occ: occ, Label: label, Value: value})
}

// Log writes a narrator line.
func (j *Journal) Log(text string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.write(JournalEntry{Type: "log", Text: text})
}

// Final writes the run outcome and closes the file.
func (j *Journal) Final(result json.RawMessage, errText string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.write(JournalEntry{Type: "final", Result: result, Error: errText})
	if j.w != nil {
		_ = j.w.Flush()
	}
	if j.f != nil {
		_ = j.f.Close()
		j.f, j.w = nil, nil
	}
}

func (j *Journal) write(e JournalEntry) {
	if j.w == nil {
		return
	}
	e.At = time.Now()
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = j.w.Write(line)
	_ = j.w.WriteByte('\n')
	_ = j.w.Flush() // flush per line so a killed run leaves a readable journal
}

// ReadJournal parses a journal file. Unparseable lines are skipped.
func ReadJournal(path string) ([]JournalEntry, error) {
	f, err := os.Open(path) //nolint:gosec // caller-supplied run directory
	if err != nil {
		return nil, fmt.Errorf("workflow: journal: %w", err)
	}
	defer f.Close()
	var out []JournalEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e JournalEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("workflow: journal: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("workflow: journal is empty")
	}
	return out, nil
}
