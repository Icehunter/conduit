// Package episodes stores opt-in, sanitized coding evidence separately from the
// mutable conversation transcript. Content is private code, not anonymized data.
package episodes

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

const (
	Version           = 1
	KindStart         = "episode_start"
	KindRequest       = "model_request"
	KindResponse      = "model_response"
	KindTool          = "tool_execution"
	KindCandidate     = "candidate"
	KindDecision      = "decision"
	KindVerification  = "verification"
	KindEnd           = "episode_end"
	OriginInteractive = "interactive"
	OriginEvaluation  = "evaluation"
	maxRecordBytes    = 4 << 20
)

// Config is independent of settings and API packages. Empty AllowedPaths means
// source-bearing content is denied. IncludeContent only opts in to collection;
// privacy and source policy still apply to every event.
type Config struct {
	Enabled        bool
	IncludeContent bool
	Dir            string
	RepositoryRoot string
	SessionID      string
	AllowedPaths   []string
	ExcludedPaths  []string
	RetentionDays  int
}

// Event is an immutable journal record. Payload accepts any JSON value. Record
// copies and sanitizes it before persistence; the caller's live values stay intact.
// Sources lists files contributing content, including files expanded by routers.
// Missing router context must set Incomplete, and prevents training export.
type Event struct {
	Version     int      `json:"version"`
	ID          string   `json:"id"`
	Seq         uint64   `json:"seq"`
	Timestamp   string   `json:"timestamp"`
	Kind        string   `json:"kind"`
	EpisodeID   string   `json:"episode_id"`
	SessionID   string   `json:"session_id"`
	RunID       string   `json:"run_id,omitempty"`
	ParentRunID string   `json:"parent_run_id,omitempty"`
	AttemptID   string   `json:"attempt_id,omitempty"`
	RequestID   string   `json:"request_id,omitempty"`
	ToolCallID  string   `json:"tool_call_id,omitempty"`
	Origin      string   `json:"origin,omitempty"`
	Sources     []string `json:"sources,omitempty"`
	Payload     any      `json:"payload,omitempty"`
	Incomplete  bool     `json:"incomplete,omitempty"`
	Redacted    bool     `json:"redacted,omitempty"`
}

type Recorder struct {
	mu        sync.Mutex
	cfg       Config
	inputRoot string
	path      string
	file      *os.File
	lock      *flock.Flock
	seq       uint64
	failed    error
	closed    bool
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// New opens one session ledger under an exclusive cross-process lock. Existing
// corrupt or truncated ledgers are refused; no evidence is silently rewritten.
func New(cfg Config) (*Recorder, error) {
	r := &Recorder{cfg: cfg, inputRoot: cfg.RepositoryRoot}
	if !cfg.Enabled {
		return r, nil
	}
	if !safeID.MatchString(cfg.SessionID) || cfg.Dir == "" || cfg.RepositoryRoot == "" {
		return nil, errors.New("episodes: session ID, directory and repository root are required")
	}
	if cfg.RetentionDays < 0 {
		return nil, errors.New("episodes: negative retention")
	}
	root, err := filepath.EvalSymlinks(cfg.RepositoryRoot)
	if err != nil {
		return nil, fmt.Errorf("episodes: repository: %w", err)
	}
	r.cfg.RepositoryRoot, err = filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("episodes: repository path: %w", err)
	}
	if err := privateDir(cfg.Dir); err != nil {
		return nil, err
	}
	r.path = filepath.Join(cfg.Dir, cfg.SessionID+".jsonl")
	for _, p := range []string{r.path, r.path + ".lock"} {
		if st, e := os.Lstat(p); e == nil && !st.Mode().IsRegular() {
			return nil, fmt.Errorf("episodes: refusing non-regular file %s", filepath.Base(p))
		} else if e != nil && !os.IsNotExist(e) {
			return nil, fmt.Errorf("episodes: inspect file: %w", e)
		}
	}
	r.lock = flock.New(r.path+".lock", flock.SetPermissions(0600))
	locked, err := r.lock.TryLock()
	if err != nil {
		return nil, fmt.Errorf("episodes: lock: %w", err)
	}
	if !locked {
		return nil, errors.New("episodes: ledger already open")
	}
	fail := func(err error) (*Recorder, error) { return nil, errors.Join(err, r.lock.Close()) }
	if _, err := os.Stat(r.path); err == nil {
		events, err := ReadFile(r.path)
		if err != nil {
			return fail(err)
		}
		if len(events) > 0 {
			r.seq = events[len(events)-1].Seq
		}
	} else if !os.IsNotExist(err) {
		return fail(fmt.Errorf("episodes: stat: %w", err))
	}
	r.file, err = os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fail(fmt.Errorf("episodes: open: %w", err))
	}
	if err := r.file.Chmod(0600); err != nil {
		return nil, errors.Join(fmt.Errorf("episodes: permissions: %w", err), r.Close())
	}
	return r, nil
}

func privateDir(path string) error {
	if st, err := os.Lstat(path); err == nil && (!st.IsDir() || st.Mode()&os.ModeSymlink != 0) {
		return errors.New("episodes: recording directory must be a real directory")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("episodes: mkdir: %w", err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		return fmt.Errorf("episodes: directory permissions: %w", err)
	}
	return nil
}

// Path returns the local ledger path, or empty for a disabled recorder.
func (r *Recorder) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

// Record synchronously flushes each event. A failed writer is permanently
// disabled for this instance, so later successes cannot conceal a missing event.
func (r *Recorder) Record(e Event) error {
	if r == nil || !r.cfg.Enabled {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed != nil {
		return r.failed
	}
	if r.closed {
		return errors.New("episodes: recorder closed")
	}
	if e.Kind == "" || e.EpisodeID == "" {
		return errors.New("episodes: kind and episode ID required")
	}
	e.Version = Version
	e.SessionID = r.cfg.SessionID
	e.Seq = r.seq + 1
	e.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return fmt.Errorf("episodes: event ID: %w", err)
	}
	e.ID = hex.EncodeToString(id[:])
	if e.Origin == "" {
		e.Origin = OriginInteractive
	}
	if err := r.sanitize(&e); err != nil {
		r.failed = err
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("episodes: encode: %w", err)
	}
	if len(b) > maxRecordBytes {
		r.failed = errors.New("episodes: record too large")
		return r.failed
	}
	b = append(b, '\n')
	n, err := r.file.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = r.file.Sync()
	}
	if err != nil {
		r.failed = fmt.Errorf("episodes: append: %w", err)
		return r.failed
	}
	r.seq = e.Seq
	return nil
}

func (r *Recorder) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var errs []error
	if r.file != nil {
		errs = append(errs, r.file.Close())
	}
	if r.lock != nil {
		errs = append(errs, r.lock.Close())
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("episodes: close: %w", err)
	}
	return nil
}

// ReadFile validates framing, schema version and strict sequence continuity.
func ReadFile(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("episodes: read: %w", err)
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var events []Event
	seen := map[string]bool{}
	for {
		var line []byte
		for {
			part, prefix, readErr := r.ReadLine()
			if readErr != nil {
				if errors.Is(readErr, io.EOF) && len(line) == 0 {
					return validateFinalNewline(path, events)
				}
				return nil, fmt.Errorf("episodes: read record: %w", readErr)
			}
			line = append(line, part...)
			if len(line) > maxRecordBytes {
				return nil, errors.New("episodes: oversized record")
			}
			if !prefix {
				break
			}
		}
		var e Event
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil {
			return nil, fmt.Errorf("episodes: invalid record %d: %w", len(events)+1, err)
		}
		if e.Version != Version || e.ID == "" || seen[e.ID] || e.Seq != uint64(len(events)+1) || e.EpisodeID == "" || e.Kind == "" {
			return nil, fmt.Errorf("episodes: invalid record identity at %d", len(events)+1)
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			return nil, errors.New("episodes: trailing record data")
		}
		seen[e.ID] = true
		events = append(events, e)
	}
}

func validateFinalNewline(path string, events []Event) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("episodes: framing: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("episodes: framing stat: %w", err)
	}
	if st.Size() == 0 {
		return events, nil
	}
	var b [1]byte
	if _, err := f.ReadAt(b[:], st.Size()-1); err != nil {
		return nil, fmt.Errorf("episodes: framing tail: %w", err)
	}
	if b[0] != '\n' {
		return nil, errors.New("episodes: truncated final record")
	}
	return events, nil
}

// Prune removes only closed ledgers older than the explicit retention cutoff.
// It never follows symlinks or removes an active writer's journal.
func Prune(dir string, retentionDays int, now time.Time) error {
	if retentionDays <= 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("episodes: retention read: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") || e.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return fmt.Errorf("episodes: retention stat: %w", err)
		}
		if !info.ModTime().Before(now.AddDate(0, 0, -retentionDays)) {
			continue
		}
		p := filepath.Join(dir, e.Name())
		lock := flock.New(p+".lock", flock.SetPermissions(0600))
		ok, err := lock.TryLock()
		if err != nil {
			return fmt.Errorf("episodes: retention lock: %w", err)
		}
		if !ok {
			continue
		}
		removeErr := os.Remove(p)
		closeErr := lock.Close()
		if err := errors.Join(removeErr, closeErr); err != nil {
			return fmt.Errorf("episodes: retention remove: %w", err)
		}
	}
	return nil
}
