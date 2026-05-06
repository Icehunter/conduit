package app

import (
	"crypto/rand"
	"encoding/hex"
)

// NewSessionID returns a fresh UUIDv4-ish session id.
func NewSessionID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	// Set version (4) and variant bits per RFC 4122 section 4.4.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[:4]) + "-" +
		hex.EncodeToString(b[4:6]) + "-" +
		hex.EncodeToString(b[6:8]) + "-" +
		hex.EncodeToString(b[8:10]) + "-" +
		hex.EncodeToString(b[10:])
}
