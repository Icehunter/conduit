package hashline_test

import (
	"testing"

	"github.com/icehunter/conduit/internal/hashline"
)

func TestCompute_empty(t *testing.T) {
	anchors := hashline.Compute(nil)
	if len(anchors) != 0 {
		t.Fatalf("expected empty, got %d anchors", len(anchors))
	}
	anchors = hashline.Compute([]byte(""))
	if len(anchors) != 0 {
		t.Fatalf("expected empty for empty string, got %d anchors", len(anchors))
	}
}

func TestCompute_singleLine(t *testing.T) {
	anchors := hashline.Compute([]byte("hello world"))
	if len(anchors) != 1 {
		t.Fatalf("expected 1 anchor, got %d", len(anchors))
	}
	if anchors[0].Line != 1 {
		t.Errorf("expected Line=1, got %d", anchors[0].Line)
	}
	if len(anchors[0].Hash) != 7 {
		t.Errorf("expected 7-char hash, got %q", anchors[0].Hash)
	}
}

func TestFind_stability(t *testing.T) {
	// Anchors are stable when a line is inserted that is NOT an immediate
	// neighbor of the target. The context hash includes prev+line+next, so
	// only changes to those three lines invalidate the anchor.
	//
	// File:   line one / line two / line three / line four / line five
	// Target: "line four" (index 3); prev="line three", next="line five"
	// Edit:   insert a new line between "line one" and "line two" — far from target
	original := []byte("line one\nline two\nline three\nline four\nline five\n")
	anchors := hashline.Compute(original)
	if len(anchors) < 4 {
		t.Fatal("expected at least 4 anchors")
	}
	// Use the anchor for "line four" (index 3)
	targetHash := anchors[3].Hash

	// Insert a new line between "line one" and "line two" — does not touch
	// "line four"'s immediate neighbors ("line three" / "line five").
	modified := []byte("line one\nnew inserted line\nline two\nline three\nline four\nline five\n")
	lineIdx, count := hashline.Find(modified, targetHash)
	if count != 1 {
		t.Errorf("stability: expected count=1 after non-adjacent insert, got count=%d", count)
	}
	// "line four" should now be at index 4 (0-indexed) after the insert
	if lineIdx != 4 {
		t.Errorf("stability: expected lineIdx=4, got %d", lineIdx)
	}
}

func TestFind_stale(t *testing.T) {
	original := []byte("alpha\nbeta\ngamma\n")
	anchors := hashline.Compute(original)
	betaHash := anchors[1].Hash

	// Modify "beta" — the hash should become stale
	modified := []byte("alpha\nbeta_changed\ngamma\n")
	_, count := hashline.Find(modified, betaHash)
	if count != 0 {
		t.Errorf("stale: expected count=0 after modifying target line, got count=%d", count)
	}
}

func TestFind_ambiguous(t *testing.T) {
	// Duplicate adjacent identical lines produce an ambiguous anchor.
	src := []byte("header\ndup line\ndup line\nfooter\n")
	anchors := hashline.Compute(src)
	// Both "dup line" entries should have the same hash because they share
	// the same normalized context (header/footer differ but the two dups
	// have identical neighbors — line 2 has header+dup as prev/next and
	// line 3 has dup+footer as prev/next, which ARE different, so they
	// won't be ambiguous).
	// Use identical lines at positions where context is also identical:
	src2 := []byte("dup\ndup\ndup\n")
	anchors2 := hashline.Compute(src2)
	// The middle line has prev=dup, line=dup, next=dup; same hash as outer
	// lines that share the same context. At minimum line[1] should share
	// hash with something if context matches.
	// More reliable: three consecutive identical lines where middle two share context:
	src3 := []byte("x\na\na\na\nx\n")
	anchors3 := hashline.Compute(src3)
	// line[1]: prev=x, line=a, next=a
	// line[2]: prev=a, line=a, next=a
	// line[3]: prev=a, line=a, next=x
	// line[1] and line[3] differ; but if there are 5+ 'a' lines, inner ones match.
	src4 := []byte("x\na\na\na\na\na\nx\n")
	_ = hashline.Compute(src4)
	// lines[2],[3],[4] all have prev=a, line=a, next=a — those ARE ambiguous.
	// Use src4 to test ambiguity:
	_, count := hashline.Find(src4, anchors3[0].Hash) // just verify no panic
	_ = count
	_ = anchors
	_ = anchors2

	// Reliable ambiguity test: find a hash that appears more than once
	src5 := []byte("x\na\na\na\na\na\nx\n")
	anchors5 := hashline.Compute(src5)
	// inner 'a' lines (indices 2,3,4 = lines 3,4,5 in 1-indexed, i.e., the
	// three with prev=a, cur=a, next=a) should share the same hash.
	hashCounts := make(map[string]int)
	for _, a := range anchors5 {
		hashCounts[a.Hash]++
	}
	foundAmbiguous := false
	var ambiguousHash string
	for h, c := range hashCounts {
		if c > 1 {
			foundAmbiguous = true
			ambiguousHash = h
			break
		}
	}
	if !foundAmbiguous {
		t.Fatal("no ambiguous hash found in test input — adjust input")
	}
	_, count2 := hashline.Find(src5, ambiguousHash)
	if count2 < 2 {
		t.Errorf("ambiguous: expected count>=2, got %d for hash %q", count2, ambiguousHash)
	}
}

func TestCompute_crlf(t *testing.T) {
	withCRLF := []byte("line one\r\nline two\r\nline three\r\n")
	withLF := []byte("line one\nline two\nline three\n")
	anchorsCRLF := hashline.Compute(withCRLF)
	anchorsLF := hashline.Compute(withLF)
	if len(anchorsCRLF) != len(anchorsLF) {
		t.Fatalf("CRLF: expected same anchor count, got %d vs %d", len(anchorsCRLF), len(anchorsLF))
	}
	for i := range anchorsCRLF {
		if anchorsCRLF[i].Hash != anchorsLF[i].Hash {
			t.Errorf("CRLF: line %d: hash mismatch: CRLF=%q LF=%q", i+1, anchorsCRLF[i].Hash, anchorsLF[i].Hash)
		}
	}
}

func TestFind_notFound(t *testing.T) {
	src := []byte("foo\nbar\nbaz\n")
	lineIdx, count := hashline.Find(src, "0000000")
	if count != 0 {
		t.Errorf("expected count=0 for nonexistent hash, got %d", count)
	}
	if lineIdx != -1 {
		t.Errorf("expected lineIdx=-1 for not-found, got %d", lineIdx)
	}
}

func FuzzComputeFind(f *testing.F) {
	// Seeds: real multi-line files where every anchor should resolve uniquely
	f.Add([]byte("hello\nworld\nfoo\n"))
	f.Add([]byte("package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n"))
	f.Add([]byte("a\n"))
	f.Add([]byte(""))
	f.Add([]byte("line\r\nwith\r\ncrlf\r\n"))

	f.Fuzz(func(t *testing.T, src []byte) {
		anchors := hashline.Compute(src)
		// For every anchor, Find should return count >= 1 (at least the anchor itself).
		// If the same context appears multiple times (duplicate lines), count > 1 is fine,
		// but count == 0 would be a bug.
		for _, a := range anchors {
			_, count := hashline.Find(src, a.Hash)
			if count == 0 {
				t.Errorf("Find(%q) returned count=0 but Compute produced that hash — round-trip broken", a.Hash)
			}
		}
	})
}
