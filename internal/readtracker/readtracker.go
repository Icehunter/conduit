// Package readtracker records which file contents the model has seen so the
// file-mutating tools can refuse blind or stale writes (Claude Code's "must
// Read before Edit/Write" rule).
//
// State is keyed by path and holds content hashes, not mtimes: a hash answers
// "is the file the model saw the file on disk now" exactly, and a bare touch
// does not force a re-read. The caller always passes the bytes it just read
// from disk, so the check costs no extra I/O.
//
// A nil *Tracker is valid and permissive, so tools constructed without one
// (tests, ad-hoc registries) keep their old behaviour.
//
// One Tracker is created per interactive session by the app registry and
// shared by every file tool. Sub-agent registries are views over the parent
// registry, so sub-agents share the parent's tracker: a file a sub-agent read
// counts as read for the parent and the other way round.
package readtracker

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
)

// ErrNotRead is returned by Check for a file that was never read.
var ErrNotRead = errors.New("file has not been read")

// ErrChanged is returned by Check for a file whose content differs from
// every version the model has seen.
var ErrChanged = errors.New("file changed since it was last read")

type digest [sha256.Size]byte

type entry struct {
	// known holds the content digests the model is allowed to overwrite:
	// the last version read or written, plus any staged (not yet flushed)
	// version.
	known map[digest]struct{}
	// shown holds the anchor hashes displayed by anchored reads of the
	// current content. Anchors are content-derived, so they stay valid when
	// edits elsewhere shift line numbers.
	shown map[string]struct{}
}

// Tracker is safe for concurrent use.
type Tracker struct {
	mu    sync.Mutex
	files map[string]*entry
}

// New returns an empty Tracker.
func New() *Tracker { return &Tracker{files: map[string]*entry{}} }

// key normalises path so aliases of one file (symlinks, "." segments) share
// an entry, and so the key is the same before and after the file exists: when
// the file itself cannot be resolved, its directory is resolved and the base
// name re-appended (macOS /var is a symlink to /private/var).
func key(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		return filepath.Join(dir, filepath.Base(path))
	}
	return path
}

func sum(content []byte) digest { return sha256.Sum256(content) }

// RecordRead notes that the model was shown content (the full raw bytes of
// the file) at path. shownAnchors lists the anchor hashes of the lines that
// were displayed, for an anchored read; it is nil otherwise.
func (t *Tracker) RecordRead(path string, content []byte, shownAnchors []string) {
	if t == nil {
		return
	}
	d := sum(content)
	k := key(path)
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.files[k]
	if e == nil {
		e = &entry{shown: map[string]struct{}{}}
		t.files[k] = e
	}
	if _, same := e.known[d]; !same {
		// New content: anchors shown for older versions no longer count.
		e.shown = map[string]struct{}{}
	}
	e.known = map[digest]struct{}{d: {}}
	for _, a := range shownAnchors {
		e.shown[a] = struct{}{}
	}
}

// RecordWrite notes that a tool wrote content to path. The model knows what
// it wrote, so the file counts as read at that version. Anchors already shown
// are kept; edited lines carry new anchors that were never shown.
func (t *Tracker) RecordWrite(path string, content []byte) {
	if t == nil {
		return
	}
	k := key(path)
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.files[k]
	if e == nil {
		e = &entry{shown: map[string]struct{}{}}
		t.files[k] = e
	}
	e.known = map[digest]struct{}{sum(content): {}}
}

// RecordStaged notes that content was staged for path but not yet written.
// Disk still holds the previous version, so both versions are accepted: the
// one on disk now and the one a later flush will put there.
func (t *Tracker) RecordStaged(path string, content []byte) {
	if t == nil {
		return
	}
	k := key(path)
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.files[k]
	if e == nil {
		e = &entry{known: map[digest]struct{}{}, shown: map[string]struct{}{}}
		t.files[k] = e
	}
	e.known[sum(content)] = struct{}{}
}

// Check reports whether the model may modify the existing file at path whose
// current on-disk bytes are disk. It returns an error wrapping ErrNotRead or
// ErrChanged, worded for the model, when it may not.
func (t *Tracker) Check(path string, disk []byte) error {
	if t == nil {
		return nil
	}
	k := key(path)
	t.mu.Lock()
	e := t.files[k]
	var ok bool
	if e != nil {
		_, ok = e.known[sum(disk)]
	}
	t.mu.Unlock()
	switch {
	case e == nil:
		return fmt.Errorf("%w: use the Read tool on %s before modifying it, then retry", ErrNotRead, path)
	case !ok:
		return fmt.Errorf("%w: %s was modified on disk (by the user, a formatter or another process) after you last read it; use the Read tool on it again before modifying it", ErrChanged, path)
	}
	return nil
}

// Shown reports whether an anchored read of the current content of path
// displayed the line with this anchor hash.
func (t *Tracker) Shown(path, anchor string) bool {
	if t == nil {
		return true
	}
	k := key(path)
	t.mu.Lock()
	defer t.mu.Unlock()
	e := t.files[k]
	if e == nil {
		return false
	}
	_, ok := e.shown[anchor]
	return ok
}

// Reset forgets everything, for callers that discard the conversation's
// history (the model can no longer be assumed to remember file contents).
func (t *Tracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.files = map[string]*entry{}
	t.mu.Unlock()
}
