package skillusage

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/settings"
)

// Backup describes a single skills backup snapshot.
type Backup struct {
	ID        string // RFC3339 timestamp (colons replaced with _) used as directory name
	Path      string // full path to the tar.gz
	CreatedAt time.Time
}

// backupsDir returns the root directory where backups are stored.
func backupsDir() string {
	return filepath.Join(settings.ConduitDir(), "skill-backups")
}

// idLayout is an RFC3339Nano-like layout with nanoseconds, used to generate
// filesystem-safe backup directory names.  Colons are replaced with
// underscores because colons are forbidden in directory names on Windows and
// cause problems on some macOS file systems.
const idLayout = "2006-01-02T15_04_05.000000000Z"

// makeID converts a time.Time into a filesystem-safe timestamp string.
func makeID(t time.Time) string {
	return t.UTC().Format(idLayout)
}

// parseID converts an ID string back to a time.Time.
func parseID(id string) (time.Time, error) {
	return time.Parse(idLayout, id)
}

// Snapshot archives ~/.conduit/skills/ and ~/.claude/skills/ into a tar.gz
// under ~/.conduit/skill-backups/<id>/skills.tar.gz.
// Returns the ID string used as the directory name.
func Snapshot() (string, error) {
	id := makeID(time.Now())
	destDir := filepath.Join(backupsDir(), id)
	if err := os.MkdirAll(destDir, 0o700); err != nil {
		return "", fmt.Errorf("skillusage: snapshot mkdir: %w", err)
	}

	archivePath := filepath.Join(destDir, "skills.tar.gz")
	f, err := os.Create(archivePath) //nolint:gosec // path is constructed internally
	if err != nil {
		return "", fmt.Errorf("skillusage: snapshot create: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("skillusage: snapshot close: %w", cerr)
		}
	}()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)

	sources := []struct {
		dir      string
		archName string
	}{
		{filepath.Join(settings.ConduitDir(), "skills"), "conduit-skills"},
		{filepath.Join(settings.ClaudeDir(), "skills"), "claude-skills"},
	}

	for _, src := range sources {
		if _, statErr := os.Stat(src.dir); errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if walkErr := addDirToTar(tw, src.dir, src.archName); walkErr != nil {
			return "", fmt.Errorf("skillusage: snapshot walk %s: %w", src.dir, walkErr)
		}
	}

	if err = tw.Close(); err != nil {
		return "", fmt.Errorf("skillusage: snapshot tar close: %w", err)
	}
	if err = gz.Close(); err != nil {
		return "", fmt.Errorf("skillusage: snapshot gzip close: %w", err)
	}

	return id, nil
}

// addDirToTar walks srcDir and writes every file to tw with paths prefixed by
// archPrefix.
func addDirToTar(tw *tar.Writer, srcDir, archPrefix string) error {
	return filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return fmt.Errorf("skillusage: rel path: %w", err)
		}
		// Use forward slashes inside the archive regardless of OS.
		archPath := archPrefix + "/" + filepath.ToSlash(rel)

		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("skillusage: tar header for %s: %w", path, err)
		}
		hdr.Name = archPath

		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("skillusage: write tar header %s: %w", archPath, err)
		}
		if info.IsDir() {
			return nil
		}

		f, err := os.Open(path) //nolint:gosec // path is from filepath.Walk
		if err != nil {
			return fmt.Errorf("skillusage: open %s: %w", path, err)
		}
		defer f.Close()

		if _, err := io.Copy(tw, f); err != nil {
			return fmt.Errorf("skillusage: copy %s: %w", path, err)
		}
		return nil
	})
}

// PruneBackups keeps only the `keep` most-recent backup directories and
// removes the rest.  No-op if fewer than `keep` backups exist.
func PruneBackups(keep int) {
	backups, err := ListBackups()
	if err != nil {
		log.Printf("skillusage: PruneBackups list: %v", err)
		return
	}
	if len(backups) <= keep {
		return
	}
	// backups is newest-first; remove the tail (oldest).
	toRemove := backups[keep:]
	for _, b := range toRemove {
		dir := filepath.Dir(b.Path)
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("skillusage: PruneBackups remove %s: %v", dir, err)
		}
	}
}

// ListBackups returns all backups sorted newest-first.
func ListBackups() ([]Backup, error) {
	entries, err := os.ReadDir(backupsDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("skillusage: list backups: %w", err)
	}

	var backups []Backup
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, err := parseID(e.Name())
		if err != nil {
			continue // skip dirs that don't match our naming scheme
		}
		archivePath := filepath.Join(backupsDir(), e.Name(), "skills.tar.gz")
		if _, statErr := os.Stat(archivePath); errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		backups = append(backups, Backup{
			ID:        e.Name(),
			Path:      archivePath,
			CreatedAt: t,
		})
	}

	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreatedAt.After(backups[j].CreatedAt)
	})
	return backups, nil
}

// Rollback first takes a pre-rollback snapshot (for safety), then extracts
// the tar.gz identified by id, restoring conduit-skills → ~/.conduit/skills/
// and claude-skills → ~/.claude/skills/.
func Rollback(id string) (retErr error) {
	backups, err := ListBackups()
	if err != nil {
		return fmt.Errorf("skillusage: rollback list: %w", err)
	}

	var target *Backup
	for i := range backups {
		if backups[i].ID == id {
			target = &backups[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("skillusage: rollback: snapshot %q not found", id)
	}

	// Safety snapshot before overwriting anything.
	if _, err := Snapshot(); err != nil {
		return fmt.Errorf("skillusage: rollback pre-snapshot: %w", err)
	}

	destMap := map[string]string{
		"conduit-skills": filepath.Join(settings.ConduitDir(), "skills"),
		"claude-skills":  filepath.Join(settings.ClaudeDir(), "skills"),
	}

	f, err := os.Open(target.Path) //nolint:gosec // path comes from ListBackups
	if err != nil {
		return fmt.Errorf("skillusage: rollback open archive: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && retErr == nil {
			retErr = fmt.Errorf("skillusage: rollback close archive: %w", cerr)
		}
	}()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("skillusage: rollback gzip reader: %w", err)
	}
	defer func() {
		if cerr := gr.Close(); cerr != nil && retErr == nil {
			retErr = fmt.Errorf("skillusage: rollback gzip close: %w", cerr)
		}
	}()

	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("skillusage: rollback read tar: %w", err)
		}

		// Determine destination path by mapping the archive prefix.
		parts := strings.SplitN(hdr.Name, "/", 2)
		if len(parts) < 2 {
			continue
		}
		prefix, rest := parts[0], parts[1]
		destBase, ok := destMap[prefix]
		if !ok {
			continue
		}
		destPath := filepath.Join(destBase, filepath.FromSlash(rest))

		// Guard against path traversal.
		if !strings.HasPrefix(filepath.Clean(destPath), filepath.Clean(destBase)) {
			return fmt.Errorf("skillusage: rollback: unsafe path %q", hdr.Name)
		}

		if hdr.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(destPath, hdr.FileInfo().Mode()); err != nil {
				return fmt.Errorf("skillusage: rollback mkdir %s: %w", destPath, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0o700); err != nil {
			return fmt.Errorf("skillusage: rollback mkdir parent %s: %w", destPath, err)
		}
		out, err := os.Create(destPath) //nolint:gosec
		if err != nil {
			return fmt.Errorf("skillusage: rollback create %s: %w", destPath, err)
		}
		if _, copyErr := io.Copy(out, tr); copyErr != nil { //nolint:gosec
			_ = out.Close()
			return fmt.Errorf("skillusage: rollback write %s: %w", destPath, copyErr)
		}
		if err := out.Close(); err != nil {
			return fmt.Errorf("skillusage: rollback close %s: %w", destPath, err)
		}
	}

	return nil
}
