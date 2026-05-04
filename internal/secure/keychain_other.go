//go:build !darwin

package secure

// NewDefault on non-macOS (Linux, Windows) returns a file-based store at
// ~/.claude/.conduit-credentials.json.
// TODO: add libsecret support for Linux (mirrors CC's roadmap).
func NewDefault() Storage {
	return newLinuxFileStorage()
}
