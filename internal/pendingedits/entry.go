// Package pendingedits implements the staging table for the diff-first
// review gate. File-write tools route their output through Stage instead
// of touching disk; the foreground agent loop drains the table at end of
// turn, presents the diffs to the user, and flushes only the approved
// subset atomically.
//
// Sub-agents (council, summariser, Task tool) construct file tools with a
// nil Stager and continue to write directly. The single foreground loop
// owns one Table per session.
package pendingedits

import (
	"time"
)

// Op identifies the kind of pending change.
type Op int

const (
	// OpEdit is an in-place edit (Edit tool).
	OpEdit Op = iota
	// OpWrite is a full overwrite or new-file create (Write tool).
	OpWrite
	// OpDelete is reserved for a future delete tool. Unused in v1.6.
	OpDelete
)

// String returns a stable lowercase tag suitable for JSONL ("edit", "write", "delete").
func (o Op) String() string {
	switch o {
	case OpEdit:
		return "edit"
	case OpWrite:
		return "write"
	case OpDelete:
		return "delete"
	}
	return "unknown"
}

// Entry is one pending change keyed by absolute path.
//
// OrigContent is the on-disk content captured at the time of the FIRST stage
// for this path. Subsequent stages on the same path overwrite NewContent (the
// composite-merge resolution from the design doc) but never change OrigContent
// — the diff shown to the user is always disk → final.
//
// OrigExisted tracks whether the file existed on disk at first-stage time, so
// the flusher can distinguish "create" from "overwrite" when computing
// permissions and parent-directory creation.
type Entry struct {
	Path        string
	OrigContent []byte
	OrigExisted bool
	NewContent  []byte
	Op          Op
	ToolName    string
	StagedAt    time.Time
}

// Stager is the contract file tools use to defer a write. The implementation
// must be safe for concurrent use (file tools may run in parallel).
//
// Stage takes ownership of the supplied byte slices — callers must not mutate
// them after the call.
type Stager interface {
	Stage(e Entry) error
}
