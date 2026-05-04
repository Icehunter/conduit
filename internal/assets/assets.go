// Package assets embeds static resources shipped with conduit.
package assets

import (
	_ "embed"
	"os"
	"path/filepath"
	"sync"
)

//go:embed conduit.png
var conduitPNG []byte

var (
	iconOnce sync.Once
	iconPath string
)

// IconPath returns a filesystem path to conduit.png suitable for passing to
// system notification APIs. The file is extracted from the embedded bytes to
// a stable location under os.TempDir() on first call and reused thereafter.
// Returns "" if the file cannot be written.
func IconPath() string {
	iconOnce.Do(func() {
		p := filepath.Join(os.TempDir(), "conduit-icon.png")
		if err := os.WriteFile(p, conduitPNG, 0o600); err == nil {
			iconPath = p
		}
	})
	return iconPath
}
