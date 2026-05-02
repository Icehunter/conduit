package secure

// NewDefault returns the appropriate Storage for the current platform.
//
// Until we wire the real platform keychains (zalando/go-keyring on macOS,
// libsecret on Linux, WinCred on Windows), this falls back to a file-based
// store rooted in os.UserConfigDir, mode 0600. That's adequate for personal
// development machines and CI; it is NOT a hardened keychain replacement —
// the real bindings are tracked as a follow-up to M1.
//
// If the user config dir cannot be located (extremely rare; broken HOME),
// we fall back to MemoryStorage so the binary still runs in-process.
func NewDefault() Storage {
	path, err := DefaultFilePath()
	if err != nil {
		return NewMemoryStorage()
	}
	return NewFileStorage(path)
}
