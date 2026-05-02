package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// verifierBytes is the random byte length we draw before base64url-encoding
// to produce the PKCE verifier. 32 bytes -> 43 base64url chars (no padding),
// matching the canonical Claude Code reference.
const verifierBytes = 32

// stateBytes is the random byte length for the OAuth `state` nonce.
// 32 bytes -> 43 base64url chars (no padding), matching the length of
// states emitted by real Claude Code 2.1.126 (empirically captured
// 2026-05-01). The authorize endpoint rejects shorter states with
// "Invalid request format".
const stateBytes = 32

// GenerateVerifier returns a fresh PKCE code verifier per RFC 7636 §4.1.
//
// Output is base64url-without-padding, which is a strict subset of the
// allowed unreserved character set (ALPHA / DIGIT / "-" / "." / "_" / "~").
// Length is fixed at 43 — between the RFC's [43,128] range, matching the
// reference implementation's 32-byte draw.
func GenerateVerifier() (string, error) {
	buf := make([]byte, verifierBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: read random bytes for verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// S256Challenge returns the BASE64URL(SHA256(verifier)) challenge for the
// given verifier, per RFC 7636 §4.2. Padding is omitted.
func S256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// GenerateState returns a fresh OAuth state nonce. Output is base64url-
// without-padding so it's safe to drop into a query string verbatim.
func GenerateState() (string, error) {
	buf := make([]byte, stateBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: read random bytes for state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
