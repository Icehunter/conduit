// Package auth implements the OAuth 2.0 + PKCE flow used by Claude Code.
//
// PKCE is RFC 7636. The decoded reference (decoded/1220.js) uses
// `code_challenge_method = S256`, so we generate a random verifier of the
// allowed unreserved-character alphabet and produce its SHA-256 challenge.
package auth

import (
	"strings"
	"testing"
)

// TestS256Challenge_RFC7636AppendixB verifies the worked example from
// RFC 7636 Appendix B: the verifier "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
// produces challenge "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM".
//
// This is the canonical PKCE test vector. If we don't pass this, every other
// OAuth provider will reject our challenges.
func TestS256Challenge_RFC7636AppendixB(t *testing.T) {
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"

	got := S256Challenge(verifier)
	if got != want {
		t.Fatalf("S256Challenge(%q) = %q; want %q", verifier, got, want)
	}
}

// TestGenerateVerifier_Length checks we produce a verifier in [43, 128]
// characters, the RFC 7636 §4.1 range. Anthropic's reference uses 32 random
// bytes encoded with base64url-without-padding, which yields 43 characters.
func TestGenerateVerifier_Length(t *testing.T) {
	v, err := GenerateVerifier()
	if err != nil {
		t.Fatalf("GenerateVerifier: %v", err)
	}
	if len(v) < 43 || len(v) > 128 {
		t.Fatalf("verifier length %d out of [43,128]", len(v))
	}
}

// TestGenerateVerifier_Alphabet checks every character is in the RFC 7636
// unreserved set: ALPHA / DIGIT / "-" / "." / "_" / "~".
func TestGenerateVerifier_Alphabet(t *testing.T) {
	v, err := GenerateVerifier()
	if err != nil {
		t.Fatalf("GenerateVerifier: %v", err)
	}
	const allowed = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"
	for i, r := range v {
		if !strings.ContainsRune(allowed, r) {
			t.Fatalf("verifier[%d]=%q outside RFC 7636 unreserved set", i, r)
		}
	}
}

// TestGenerateVerifier_Unique checks two consecutive calls produce different
// values. Not a randomness test, just a sanity check that we're not memoizing.
func TestGenerateVerifier_Unique(t *testing.T) {
	a, err := GenerateVerifier()
	if err != nil {
		t.Fatal(err)
	}
	b, err := GenerateVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("two calls produced identical verifier: %q", a)
	}
}

// TestGenerateState produces a non-empty, URL-safe random string.
func TestGenerateState(t *testing.T) {
	s, err := GenerateState()
	if err != nil {
		t.Fatalf("GenerateState: %v", err)
	}
	if s == "" {
		t.Fatal("empty state")
	}
	for _, r := range s {
		// base64url alphabet
		if !((r >= 'A' && r <= 'Z') || //nolint:staticcheck
			(r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_') {
			t.Fatalf("state contains non-base64url char: %q", r)
		}
	}
}
