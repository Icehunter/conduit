package pendingedits

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

// FlushResult reports the outcome of a single Flush call.
type FlushResult struct {
	Path    string
	Applied bool  // true when the entry was written successfully
	Err     error // non-nil when Applied is false
}

// Flush writes the supplied entries to disk via temp-file + rename. Each
// entry is processed independently — a failure on one path does not abort
// the others. The returned slice has one result per input entry, in the
// same order.
//
// Parent directories are created (0755). When an entry creates a brand-new
// file the result is mode 0644; when it overwrites, the original file mode
// is preserved.
func Flush(entries []Entry) []FlushResult {
	out := make([]FlushResult, len(entries))
	for i, e := range entries {
		out[i] = FlushResult{Path: e.Path}
		if err := flushOne(e); err != nil {
			out[i].Err = err
			continue
		}
		out[i].Applied = true
	}
	return out
}

// FlushOne writes a single entry. Exposed for callers that want per-entry
// error handling without packing into a slice.
func FlushOne(e Entry) error {
	return flushOne(e)
}

func flushOne(e Entry) error {
	if e.Path == "" {
		return errEmptyPath
	}
	dir := filepath.Dir(e.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("pendingedits: mkdir %s: %w", dir, err)
	}
	if err := ensureNoFlushConflict(e); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".pending-*.tmp")
	if err != nil {
		return fmt.Errorf("pendingedits: tempfile: %w", err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if rename never happens; ignored when rename succeeds.
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(e.NewContent); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("pendingedits: write tmp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("pendingedits: sync tmp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("pendingedits: close tmp: %w", err)
	}

	// Preserve permissions when overwriting an existing file.
	if e.OrigExisted {
		if st, err := os.Lstat(e.Path); err == nil {
			_ = os.Chmod(tmpPath, st.Mode())
		}
	}

	if err := os.Rename(tmpPath, e.Path); err != nil {
		return fmt.Errorf("pendingedits: rename %s: %w", e.Path, err)
	}
	return nil
}

func ensureNoFlushConflict(e Entry) error {
	if e.OrigExisted {
		current, err := os.ReadFile(e.Path)
		if err != nil {
			return fmt.Errorf("pendingedits: conflict reading %s: %w", e.Path, err)
		}
		if !bytes.Equal(current, e.OrigContent) {
			return fmt.Errorf("pendingedits: conflict on %s: file changed since edit was staged", e.Path)
		}
		return nil
	}
	if _, err := os.Lstat(e.Path); err == nil {
		return fmt.Errorf("pendingedits: conflict on %s: file appeared since edit was staged", e.Path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("pendingedits: conflict checking %s: %w", e.Path, err)
	}
	return nil
}
