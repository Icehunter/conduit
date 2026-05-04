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

// ErrNoPDF is returned when the clipboard contains no PDF data.
var ErrNoPDF = errors.New("no PDF on clipboard")

// PDF holds a base64-encoded PDF grabbed from the clipboard.
type PDF struct {
	Data      string // base64-encoded PDF bytes
	MediaType string // always "application/pdf"
}

// ReadClipboardPDF attempts to read a PDF from the system clipboard.
// On macOS this works when a PDF file is copied in Finder (puts the file
// path on the clipboard) or when apps copy PDF data directly.
// Returns ErrNoPDF when no PDF is found.
// Returns ErrNotSupported on Windows.
func ReadClipboardPDF() (*PDF, error) {
	switch runtime.GOOS {
	case "darwin":
		return readPDFDarwin()
	case "linux":
		return readPDFLinux()
	default:
		return nil, ErrNotSupported
	}
}

// readPDFDarwin reads PDF data from the macOS clipboard.
// Strategy:
//  1. Try «class PDF » pasteboard type for apps that copy PDF data directly.
//  2. Try reading the file path from the pasteboard (Finder copy of a .pdf).
func readPDFDarwin() (*PDF, error) {
	// Try direct PDF pasteboard type first.
	tmp, err := os.CreateTemp("", "conduit-pdf-*.pdf")
	if err != nil {
		return nil, fmt.Errorf("temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_ = tmp.Close()

	save := exec.Command("osascript", //nolint:noctx
		"-e", "try",
		"-e", "  set pdfData to (the clipboard as «class PDF »)",
		"-e", fmt.Sprintf("  set fp to open for access POSIX file %q with write permission", tmp.Name()),
		"-e", "  write pdfData to fp",
		"-e", "  close access fp",
		"-e", "on error",
		"-e", "  error number -1",
		"-e", "end try",
	)
	if err := save.Run(); err == nil {
		if data, err := os.ReadFile(tmp.Name()); err == nil && len(data) > 4 && string(data[:4]) == "%PDF" {
			return &PDF{
				Data:      base64.StdEncoding.EncodeToString(data),
				MediaType: "application/pdf",
			}, nil
		}
	}

	// Fall back to reading file path from clipboard (Finder copy).
	out, err := exec.Command("osascript", //nolint:noctx
		"-e", "try",
		"-e", "  POSIX path of (the clipboard as «class furl»)",
		"-e", "on error",
		"-e", "  \"\"",
		"-e", "end try",
	).Output()
	if err == nil {
		path := strings.TrimSpace(string(out))
		if strings.HasSuffix(strings.ToLower(path), ".pdf") {
			data, err := os.ReadFile(path)
			if err == nil && len(data) > 0 {
				return &PDF{
					Data:      base64.StdEncoding.EncodeToString(data),
					MediaType: "application/pdf",
				}, nil
			}
		}
	}

	return nil, ErrNoPDF
}

// readPDFLinux reads a PDF path from the clipboard via xclip/wl-paste,
// then reads the file. Linux clipboard rarely holds raw PDF bytes; copying
// a PDF file in a file manager typically puts the file URI on the clipboard.
func readPDFLinux() (*PDF, error) {
	var raw []byte

	if _, err := exec.LookPath("xclip"); err == nil {
		// Try raw PDF bytes first.
		out, err := exec.Command("xclip", "-selection", "clipboard", "-t", "application/pdf", "-o").Output() //nolint:noctx
		if err == nil && len(out) > 4 && string(out[:4]) == "%PDF" {
			raw = out
		}
		if raw == nil {
			// Try file URI.
			uriOut, err := exec.Command("xclip", "-selection", "clipboard", "-t", "text/uri-list", "-o").Output() //nolint:noctx
			if err == nil {
				raw = pdfFromURIList(string(uriOut))
			}
		}
	}

	if raw == nil {
		if _, err := exec.LookPath("wl-paste"); err == nil {
			types, _ := exec.Command("wl-paste", "-l").Output() //nolint:noctx
			if strings.Contains(string(types), "application/pdf") {
				out, err := exec.Command("wl-paste", "--type", "application/pdf").Output() //nolint:noctx
				if err == nil {
					raw = out
				}
			}
			if raw == nil && strings.Contains(string(types), "text/uri-list") {
				uriOut, err := exec.Command("wl-paste", "--type", "text/uri-list").Output() //nolint:noctx
				if err == nil {
					raw = pdfFromURIList(string(uriOut))
				}
			}
		}
	}

	if raw == nil {
		return nil, ErrNoPDF
	}
	return &PDF{
		Data:      base64.StdEncoding.EncodeToString(raw),
		MediaType: "application/pdf",
	}, nil
}

// pdfFromURIList reads the first file:// URI pointing to a .pdf and returns
// its contents, or nil if none found.
func pdfFromURIList(uriList string) []byte {
	for _, line := range strings.Split(uriList, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "file://") && strings.HasSuffix(strings.ToLower(line), ".pdf") {
			path := strings.TrimPrefix(line, "file://")
			data, err := os.ReadFile(path)
			if err == nil && len(data) > 0 {
				return data
			}
		}
	}
	return nil
}
