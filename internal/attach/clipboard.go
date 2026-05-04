// Package attach handles clipboard attachments — images and large text
// pastes. Mirrors the clipboard-reading logic in src/utils/imagePaste.ts.
//
// Platform support:
//   - macOS: osascript (NSPasteboard)
//   - Linux: xclip or wl-paste
//   - Windows: not implemented (returns ErrNotSupported)
package attach

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
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
	return img, nil
}

// readLinux tries xclip then wl-paste (Wayland) for PNG clipboard data.
func readLinux() (*Image, error) {
	// Try xclip first.
	if _, err := exec.LookPath("xclip"); err == nil {
		out, err := exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o").Output() //nolint:noctx
		if err == nil && len(out) > 0 {
			return &Image{
				Data:      base64.StdEncoding.EncodeToString(out),
				MediaType: "image/png",
			}, nil
		}
	}
	// Try wl-paste (Wayland).
	if _, err := exec.LookPath("wl-paste"); err == nil {
		// Check available types first.
		types, err := exec.Command("wl-paste", "-l").Output() //nolint:noctx
		if err == nil && strings.Contains(string(types), "image/png") {
			out, err := exec.Command("wl-paste", "--type", "image/png").Output() //nolint:noctx
			if err == nil && len(out) > 0 {
				return &Image{
					Data:      base64.StdEncoding.EncodeToString(out),
					MediaType: "image/png",
				}, nil
			}
		}
	}
	return nil, ErrNoImage
}
