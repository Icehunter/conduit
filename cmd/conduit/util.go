package main

import (
	"crypto/rand"
	"encoding/hex"
)

// newSessionID returns a fresh UUIDv4-ish 32-hex-char session id.
// Real UUID formatting (with dashes) lands when we add the uuid package; for
// the X-Claude-Code-Session-Id header the API only requires uniqueness.
func newSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	// Set version (4) and variant bits per RFC 4122 §4.4.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[:4]) + "-" +
		hex.EncodeToString(b[4:6]) + "-" +
		hex.EncodeToString(b[6:8]) + "-" +
		hex.EncodeToString(b[8:10]) + "-" +
		hex.EncodeToString(b[10:])
}
