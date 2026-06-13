// Package hashline computes per-line content-hash anchors for files.
// Each anchor hashes the line together with its immediate neighbors so that
// edits above/below a target line do not invalidate the anchor — only changes
// to the line itself or its immediate context do.
package hashline

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// Anchor represents one addressable line in a file.
type Anchor struct {
	Line int    // 1-indexed line number at compute time (advisory only)
	Hash string // first 7 hex chars of SHA-256 over normalized context
}

// Compute returns one Anchor per line of src.
// Each anchor's Hash is the first 7 hex chars of SHA-256 over the
// normalized concatenation of: trimRight(prevLine) + "\n" + trimRight(line) + "\n" + trimRight(nextLine)
// where prevLine/nextLine are empty strings for the first/last line.
// Normalization: strings.TrimRight(s, " \t\r") on each line before hashing.
func Compute(src []byte) []Anchor {
	if len(src) == 0 {
		return nil
	}
	// Split into lines, stripping \r\n → \n
	raw := strings.ReplaceAll(string(src), "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	// If src ends with a trailing newline, Split produces a trailing empty
	// element — drop it so we don't generate a phantom anchor.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}

	norm := func(s string) string { return strings.TrimRight(s, " \t\r") }

	anchors := make([]Anchor, len(lines))
	for i, line := range lines {
		prev := ""
		if i > 0 {
			prev = norm(lines[i-1])
		}
		next := ""
		if i < len(lines)-1 {
			next = norm(lines[i+1])
		}
		ctx := prev + "\n" + norm(line) + "\n" + next
		sum := sha256.Sum256([]byte(ctx))
		anchors[i] = Anchor{
			Line: i + 1,
			Hash: fmt.Sprintf("%x", sum[:])[:7],
		}
	}
	return anchors
}

// Find locates the line matching hash in src (recomputing anchors fresh).
// Returns: lineIdx (0-indexed position in lines), count (number of matching anchors).
// count == 0: stale anchor (line deleted or changed)
// count == 1: unique match (the right one)
// count > 1: ambiguous (multiple lines have same hash — tell model to re-read)
func Find(src []byte, hash string) (lineIdx int, count int) {
	anchors := Compute(src)
	lineIdx = -1
	for _, a := range anchors {
		if a.Hash == hash {
			count++
			if count == 1 {
				lineIdx = a.Line - 1 // convert to 0-indexed
			}
		}
	}
	return lineIdx, count
}
