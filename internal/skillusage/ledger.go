package skillusage

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/gofrs/flock"

	"github.com/icehunter/conduit/internal/settings"
)

// The skill edit ledger records every skill mutation (who, when, which file,
// before/after content hashes) in an append-only JSONL file. File contents
// are kept content-addressed under blobs/ so a single edit can be undone.
//
// Recording is telemetry and never blocks a write: failures are logged.
// LedgerRollback is the exception and fails closed.

// Actors that can author a skill mutation.
const (
	ActorAgent   = "agent"
	ActorCurator = "curator"
	ActorUser    = "user"
)

// Ledger operations.
const (
	OpCreate     = "create"
	OpUpdate     = "update"
	OpPatch      = "patch"
	OpWriteFile  = "write_file"
	OpRemoveFile = "remove_file"
	OpRollback   = "rollback"
	// Informational ops: recorded for audit but not reversible via the ledger
	// because they move or delete whole directories.
	OpPromote = "promote"
	OpArchive = "archive"
	OpPurge   = "purge"
)

const (
	ledgerMaxBytes   = 5 << 20
	blobMaxBytes     = 64 << 20
	ledgerTrimTarget = 0.8
	blobGCGrace      = time.Hour
	ledgerLockWait   = 2 * time.Second
	ledgerLockPoll   = 10 * time.Millisecond
)

// LedgerEntry is one recorded skill mutation.
type LedgerEntry struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Actor string    `json:"actor"`
	Op    string    `json:"op"`
	Skill string    `json:"skill"`
	// Path is the absolute path of the file that changed. Empty for
	// directory-level informational entries.
	Path string `json:"path,omitempty"`
	// Before and After are sha256 hex digests of the file content. Empty
	// means the file did not exist (Before) or was removed (After).
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Note   string `json:"note,omitempty"`
	// Reversible is true when blobs for Before/After exist and Path is a
	// skill file the ledger may restore.
	Reversible bool `json:"reversible"`
}

type actorKey struct{}

// WithActor returns ctx carrying the actor that should be credited for skill
// writes made under it (for example the curator pass).
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

// ActorFrom returns the actor bound to ctx, or fallback when none is set.
func ActorFrom(ctx context.Context, fallback string) string {
	if a, ok := ctx.Value(actorKey{}).(string); ok && a != "" {
		return a
	}
	return fallback
}

func ledgerDir() string      { return filepath.Join(settings.ConduitDir(), "skill-ledger") }
func ledgerFile() string     { return filepath.Join(ledgerDir(), "ledger.jsonl") }
func blobsDir() string       { return filepath.Join(ledgerDir(), "blobs") }
func ledgerLockFile() string { return filepath.Join(ledgerDir(), "ledger.lock") }

// skillFilePathRE restricts what a ledger rollback may write: SKILL.md or a
// references/ file directly inside a skills/<name>/ directory.
var skillFilePathRE = regexp.MustCompile(`/skills/[^/]+/(SKILL\.md|references/.+)$`)

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func validHash(h string) bool {
	if len(h) != 64 {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil
}

// storeBlob writes data under its hash if absent and returns the hash.
func storeBlob(data []byte) (string, error) {
	h := hashBytes(data)
	p := filepath.Join(blobsDir(), h)
	if _, err := os.Stat(p); err == nil {
		// Refresh the mtime so a concurrent GC sees this blob as in use.
		now := time.Now()
		_ = os.Chtimes(p, now, now)
		return h, nil
	}
	if err := os.MkdirAll(blobsDir(), 0o700); err != nil {
		return "", fmt.Errorf("skillusage: ledger blobs dir: %w", err)
	}
	if err := writeFileAtomic(p, data, 0o600); err != nil {
		return "", err
	}
	return h, nil
}

func readBlob(h string) ([]byte, error) {
	if !validHash(h) {
		return nil, fmt.Errorf("skillusage: invalid blob hash %q", h)
	}
	data, err := os.ReadFile(filepath.Join(blobsDir(), h)) //nolint:gosec // hash validated above
	if err != nil {
		return nil, fmt.Errorf("skillusage: ledger blob %s unavailable: %w", h[:12], err)
	}
	if hashBytes(data) != h {
		return nil, fmt.Errorf("skillusage: ledger blob %s is corrupt", h[:12])
	}
	return data, nil
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return fmt.Errorf("skillusage: write %s: %w", path, err)
	}
	name := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("skillusage: write %s: %w", path, err)
	}
	if err := os.Chmod(name, perm); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("skillusage: chmod %s: %w", path, err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("skillusage: rename %s: %w", path, err)
	}
	return nil
}

// captureFile stores the current content of path as a blob and returns its
// hash. A missing file yields "" with no error.
func captureFile(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // skill path chosen by caller
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("skillusage: capture %s: %w", path, err)
	}
	return storeBlob(data)
}

func newEntryID(now time.Time) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return now.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

// lockLedger takes the cross-process ledger lock.
func lockLedger() (func(), error) {
	if err := os.MkdirAll(ledgerDir(), 0o700); err != nil {
		return nil, fmt.Errorf("skillusage: ledger dir: %w", err)
	}
	fl := flock.New(ledgerLockFile())
	ctx, cancel := context.WithTimeout(context.Background(), ledgerLockWait)
	defer cancel()
	ok, err := fl.TryLockContext(ctx, ledgerLockPoll)
	if err != nil || !ok {
		return nil, fmt.Errorf("skillusage: ledger lock busy: %w", errors.Join(err, ctx.Err()))
	}
	return func() { _ = fl.Unlock() }, nil
}

// appendEntry adds e to the ledger and trims it when over the size bound.
// The caller must not hold the ledger lock.
func appendEntry(e LedgerEntry) error {
	unlock, err := lockLedger()
	if err != nil {
		return err
	}
	defer unlock()
	return appendLocked(e)
}

// appendLocked is appendEntry for a caller already holding the ledger lock.
func appendLocked(e LedgerEntry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("skillusage: encode ledger entry: %w", err)
	}
	f, err := os.OpenFile(ledgerFile(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("skillusage: open ledger: %w", err)
	}
	_, werr := f.Write(append(line, '\n'))
	if err := errors.Join(werr, f.Close()); err != nil {
		return fmt.Errorf("skillusage: append ledger: %w", err)
	}
	trimLocked(ledgerMaxBytes, blobMaxBytes, time.Now())
	return nil
}

// readEntries returns all parseable entries, oldest first. Caller holds no lock
// requirement: appends are single writes and rewrites are atomic renames.
func readEntries() []LedgerEntry {
	data, err := os.ReadFile(ledgerFile())
	if err != nil {
		return nil
	}
	var out []LedgerEntry
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var e LedgerEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID != "" {
			out = append(out, e)
		}
	}
	return out
}

func blobsSize() int64 {
	var total int64
	des, _ := os.ReadDir(blobsDir())
	for _, de := range des {
		if info, err := de.Info(); err == nil {
			total += info.Size()
		}
	}
	return total
}

// trimLocked drops the oldest entries when the ledger exceeds maxBytes or the
// blobs exceed maxBlobBytes, keeping the newest entries that fit within 80% of
// each bound (the newest entry always survives), then collects unreferenced
// blobs older than the grace period. Caller holds the ledger lock.
func trimLocked(maxBytes, maxBlobBytes int64, now time.Time) {
	fi, err := os.Stat(ledgerFile())
	if err != nil {
		return
	}
	if fi.Size() <= maxBytes && blobsSize() <= maxBlobBytes {
		return
	}
	entries := readEntries()
	lineTarget := int64(float64(maxBytes) * ledgerTrimTarget)
	blobTarget := int64(float64(maxBlobBytes) * ledgerTrimTarget)
	var lineTotal, blobTotal int64
	counted := map[string]bool{}
	blobSize := func(h string) int64 {
		if h == "" || counted[h] {
			return 0
		}
		counted[h] = true
		if info, err := os.Stat(filepath.Join(blobsDir(), h)); err == nil {
			return info.Size()
		}
		return 0
	}
	keepFrom := len(entries)
	for i := len(entries) - 1; i >= 0; i-- {
		line, _ := json.Marshal(entries[i])
		lt := lineTotal + int64(len(line)) + 1
		bt := blobTotal + blobSize(entries[i].Before) + blobSize(entries[i].After)
		if keepFrom != len(entries) && (lt > lineTarget || bt > blobTarget) {
			break
		}
		lineTotal, blobTotal, keepFrom = lt, bt, i
	}
	var buf bytes.Buffer
	for _, e := range entries[keepFrom:] {
		line, _ := json.Marshal(e)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	if err := writeFileAtomic(ledgerFile(), buf.Bytes(), 0o600); err != nil {
		log.Printf("skillusage: ledger trim: %v", err)
		return
	}
	gcBlobs(entries[keepFrom:], now)
}

// gcBlobs removes blobs no remaining entry references, sparing recent ones
// that may belong to an in-flight capture.
func gcBlobs(keep []LedgerEntry, now time.Time) {
	live := map[string]bool{}
	for _, e := range keep {
		live[e.Before], live[e.After] = true, true
	}
	des, err := os.ReadDir(blobsDir())
	if err != nil {
		return
	}
	for _, de := range des {
		if live[de.Name()] || !validHash(de.Name()) {
			continue
		}
		if info, err := de.Info(); err == nil && now.Sub(info.ModTime()) > blobGCGrace {
			_ = os.Remove(filepath.Join(blobsDir(), de.Name()))
		}
	}
}

// RecordInfo appends an informational (non-reversible) entry.
func RecordInfo(actor, op, skill, path, note string) {
	now := time.Now()
	if err := appendEntry(LedgerEntry{
		ID: newEntryID(now), Time: now, Actor: actor, Op: op, Skill: skill, Path: path, Note: note,
	}); err != nil {
		log.Printf("skillusage: ledger: %v", err)
	}
}

// TrackEdit runs mutate and records the resulting change to path. The file
// content before and after is captured so the edit can be rolled back. A
// failing mutate records nothing. Ledger failures are logged, never returned.
func TrackEdit(actor, op, skill, path string, mutate func() error) error {
	return trackEdit(actor, op, skill, path, mutate, appendEntry)
}

func trackEdit(actor, op, skill, path string, mutate func() error, appendFn func(LedgerEntry) error) error {
	before, berr := captureFile(path)
	if berr != nil {
		log.Printf("skillusage: ledger: %v", berr)
	}
	if err := mutate(); err != nil {
		return err
	}
	after, aerr := captureFile(path)
	if aerr != nil {
		log.Printf("skillusage: ledger: %v", aerr)
	}
	if berr != nil || aerr != nil || before == after {
		return nil
	}
	now := time.Now()
	if err := appendFn(LedgerEntry{
		ID: newEntryID(now), Time: now, Actor: actor, Op: op, Skill: skill, Path: path,
		Before: before, After: after, Reversible: skillFilePathRE.MatchString(filepath.ToSlash(path)),
	}); err != nil {
		log.Printf("skillusage: ledger: %v", err)
	}
	return nil
}

// LedgerList returns entries newest first, optionally for one skill, capped at
// limit when limit > 0.
func LedgerList(skill string, limit int) []LedgerEntry {
	all := readEntries()
	var out []LedgerEntry
	for i := len(all) - 1; i >= 0; i-- {
		if skill != "" && all[i].Skill != skill {
			continue
		}
		out = append(out, all[i])
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// LedgerRollback undoes one recorded edit by restoring the file to its
// pre-edit content (or deleting it when the edit created it). It fails closed:
// the entry must be reversible, point at a skill file, have an intact blob,
// and the file must still match the edit's result, so newer changes are never
// silently overwritten. The rollback is itself recorded and can be undone.
func LedgerRollback(id, actor string) (LedgerEntry, error) {
	// Hold the lock across the staleness check and the write so two
	// rollbacks (or a rollback and an append-time trim) cannot interleave.
	unlock, err := lockLedger()
	if err != nil {
		return LedgerEntry{}, err
	}
	defer unlock()
	var found *LedgerEntry
	for _, e := range readEntries() {
		if e.ID == id {
			found = &e
			break
		}
	}
	if found == nil {
		return LedgerEntry{}, fmt.Errorf("skillusage: ledger entry %q not found", id)
	}
	e := *found
	if !e.Reversible {
		return e, fmt.Errorf("skillusage: entry %s (%s) is not reversible", e.ID, e.Op)
	}
	p := filepath.ToSlash(e.Path)
	if !filepath.IsAbs(e.Path) || filepath.ToSlash(filepath.Clean(e.Path)) != p || !skillFilePathRE.MatchString(p) {
		return e, fmt.Errorf("skillusage: entry %s has an unsafe path %q", e.ID, e.Path)
	}
	if e.Before != "" {
		if _, err := readBlob(e.Before); err != nil {
			return e, err
		}
	}
	cur, err := captureFile(e.Path)
	if err != nil {
		return e, err
	}
	if cur != e.After {
		return e, fmt.Errorf("skillusage: %s changed after edit %s; roll back newer edits first", e.Path, e.ID)
	}
	restore := func() error {
		if e.Before == "" {
			if err := os.Remove(e.Path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("skillusage: remove %s: %w", e.Path, err)
			}
			// Drop the skill directory when the removed file was its last.
			_ = os.Remove(filepath.Dir(e.Path))
			return nil
		}
		data, err := readBlob(e.Before)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(e.Path), 0o755); err != nil {
			return fmt.Errorf("skillusage: mkdir for %s: %w", e.Path, err)
		}
		return writeFileAtomic(e.Path, data, 0o644)
	}
	if err := trackEdit(actor, OpRollback, e.Skill, e.Path, restore, appendLocked); err != nil {
		return e, err
	}
	return e, nil
}
