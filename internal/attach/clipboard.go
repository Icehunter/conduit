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

// readDarwin uses osascript to read the clipboard as PNG. osascript is
// available on every macOS install — no external dependencies needed.
func readDarwin() (*Image, error) {
	// Check: does the clipboard contain an image? osascript will error if not.
	check := exec.Command("osascript", "-e", "the clipboard as «class PNGf»")
	if err := check.Run(); err != nil {
		return nil, ErrNoImage
	}

	// Write PNG bytes to a temp file.
	tmp, err := os.CreateTemp("", "conduit-paste-*.png")
	if err != nil {
		return nil, fmt.Errorf("clipboard temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	tmp.Close()

	script := fmt.Sprintf(
		`set png_data to (the clipboard as «class PNGf»)`+
			` & set fp to open for access POSIX file %q with write permission`+
			` & write png_data to fp`+
			` & close access fp`,
		tmp.Name(),
	)
	save := exec.Command("osascript", "-e", script)
	if out, err := save.CombinedOutput(); err != nil {
		// Try alternative one-liner approach.
		alt := exec.Command("osascript",
			"-e", "set png_data to (the clipboard as «class PNGf»)",
			"-e", fmt.Sprintf("set fp to open for access POSIX file %q with write permission", tmp.Name()),
			"-e", "write png_data to fp",
			"-e", "close access fp",
		)
		if out2, err2 := alt.CombinedOutput(); err2 != nil {
			return nil, fmt.Errorf("osascript save failed: %v\n%s\n%s", err, out, out2)
		}
	}

	data, err := os.ReadFile(tmp.Name())
	if err != nil || len(data) == 0 {
		return nil, ErrNoImage
	}
	return &Image{
		Data:      base64.StdEncoding.EncodeToString(data),
		MediaType: "image/png",
	}, nil
}

// readLinux tries xclip then wl-paste (Wayland) for PNG clipboard data.
func readLinux() (*Image, error) {
	// Try xclip first.
	if _, err := exec.LookPath("xclip"); err == nil {
		out, err := exec.Command("xclip", "-selection", "clipboard", "-t", "image/png", "-o").Output()
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
		types, err := exec.Command("wl-paste", "-l").Output()
		if err == nil && strings.Contains(string(types), "image/png") {
			out, err := exec.Command("wl-paste", "--type", "image/png").Output()
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
