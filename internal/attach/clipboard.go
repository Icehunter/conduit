// Package attach handles clipboard attachments — images and large text
// pastes. Mirrors the clipboard-reading logic in src/utils/imagePaste.ts.
//
// Platform support:
//   - macOS: osascript (NSPasteboard)
//   - Linux: xclip or wl-paste
//   - Windows: not implemented (returns ErrNotSupported)
package attach

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/icehunter/conduit/internal/settings"
)

// ErrNoImage is returned when the clipboard contains no image data.
var ErrNoImage = errors.New("no image on clipboard")

// ErrNotSupported is returned on platforms where clipboard image access
// is not implemented.
var ErrNotSupported = errors.New("image paste not supported on this platform")

// Image holds a base64-encoded PNG image grabbed from the clipboard.
type Image struct {
	Data      string // base64-encoded PNG bytes
	MediaType string // "image/png" always for now
	// Path is the absolute path of a file holding the image, so a sent
	// attachment can link back to it. Empty when no file could be kept.
	Path string
}

// pastedImagesMaxAge is how long a kept clipboard image survives unused.
const pastedImagesMaxAge = 7 * 24 * time.Hour

func pastedImagesDir() string {
	return filepath.Join(settings.ConduitDir(), "pasted-images")
}

// persistImage writes the image to a stable file under the conduit config
// directory and records it in img.Path. A clipboard image has no file of its
// own, and its temp file is gone by the time the user clicks the sent row.
// The file is named by a hash of its bytes, so pasting the same image again
// reuses one file. Failure leaves Path empty: the attachment still sends,
// just without a link.
func persistImage(img *Image) {
	data, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil || len(data) == 0 {
		return
	}
	dir := pastedImagesDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	sum := sha256.Sum256(data)
	name := filepath.Join(dir, "paste-"+hex.EncodeToString(sum[:8])+".png")
	if _, err := os.Stat(name); err == nil {
		// Same bytes already kept: refresh its age so pruning spares a file in use.
		_ = os.Chtimes(name, time.Time{}, time.Now()) // best-effort; a stale mtime only risks an early prune
	} else {
		f, err := os.CreateTemp(dir, "paste-*.tmp")
		if err != nil {
			return
		}
		tmp := f.Name()
		_, werr := f.Write(data)
		cerr := f.Close()
		if werr != nil || cerr != nil || os.Rename(tmp, name) != nil {
			_ = os.Remove(tmp) // best-effort cleanup of a partial file
			return
		}
	}
	if abs, err := filepath.Abs(name); err == nil {
		img.Path = abs
	}
}

// PrunePastedImages deletes kept clipboard images that have not been used for
// a week. It returns the joined errors of every file it could not remove;
// a missing directory is not an error.
func PrunePastedImages() error {
	return prunePastedImages(pastedImagesDir(), time.Now(), pastedImagesMaxAge)
}

func prunePastedImages(dir string, now time.Time, maxAge time.Duration) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("attach: read pasted-images dir: %w", err)
	}
	var errs []error
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fmt.Errorf("attach: stat %s: %w", e.Name(), err))
			}
			continue
		}
		if now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("attach: remove %s: %w", e.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// ReadClipboardImage attempts to read a PNG image from the system clipboard.
// Returns ErrNoImage when the clipboard holds text or is empty.
// Returns ErrNotSupported on Windows.
func ReadClipboardImage() (*Image, error) {
	switch runtime.GOOS {
	case "darwin":
		return readDarwin()
	case "linux":
		return readLinux()
	default:
		return nil, ErrNotSupported
	}
}

// readDarwin writes clipboard image data directly to a temp file via
// osascript, then reads it back. Each `-e` flag is a separate AppleScript
// statement — never join with `&` (string concat in AppleScript).
// cmd+shift+4 screenshots land as TIFF on macOS; we try PNG first (faster
// on terminals that copy PNG directly) then TIFF.
func readDarwin() (*Image, error) {
	for _, appleType := range []string{"«class PNGf»", "«class TIFF»"} {
		img, err := readDarwinType(appleType)
		if err == nil {
			return img, nil
		}
	}
	return nil, ErrNoImage
}

func readDarwinType(appleType string) (*Image, error) {
	tmp, err := os.CreateTemp("", "conduit-paste-*.png")
	if err != nil {
		return nil, fmt.Errorf("temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_ = tmp.Close()

	// Write clipboard → file. Each -e is one statement.
	// The 'try' block converts AppleScript errors (e.g. wrong type, no
	// image on clipboard) into a non-zero exit instead of hanging.
	save := exec.Command("osascript", //nolint:noctx
		"-e", "try",
		"-e", "  set img_data to (the clipboard as "+appleType+")",
		"-e", fmt.Sprintf("  set fp to open for access POSIX file %q with write permission", tmp.Name()),
		"-e", "  write img_data to fp",
		"-e", "  close access fp",
		"-e", "on error",
		"-e", "  error number -1",
		"-e", "end try",
	)
	if out, err := save.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("no %s on clipboard: %s", appleType, strings.TrimSpace(string(out)))
	}

	data, err := os.ReadFile(tmp.Name())
	if err != nil || len(data) == 0 {
		return nil, ErrNoImage
	}
	img := &Image{
		Data:      base64.StdEncoding.EncodeToString(data),
		MediaType: "image/png",
	}
	_ = MaybeResize(img) // silently skip resize failures
	persistImage(img)
	return img, nil
}

// readLinux tries xclip then wl-paste (Wayland) for PNG clipboard data.
func readLinux() (*Image, error) {
	// Try xclip first.
	if _, err := exec.LookPath("xclip"); err == nil {
		out, err := exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o").Output() //nolint:noctx
		if err == nil && len(out) > 0 {
			img := &Image{
				Data:      base64.StdEncoding.EncodeToString(out),
				MediaType: "image/png",
			}
			persistImage(img)
			return img, nil
		}
	}
	// Try wl-paste (Wayland).
	if _, err := exec.LookPath("wl-paste"); err == nil {
		// Check available types first.
		types, err := exec.Command("wl-paste", "-l").Output() //nolint:noctx
		if err == nil && strings.Contains(string(types), "image/png") {
			out, err := exec.Command("wl-paste", "--type", "image/png").Output() //nolint:noctx
			if err == nil && len(out) > 0 {
				img := &Image{
					Data:      base64.StdEncoding.EncodeToString(out),
					MediaType: "image/png",
				}
				persistImage(img)
				return img, nil
			}
		}
	}
	return nil, ErrNoImage
}
