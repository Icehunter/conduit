package pendingedits

import "bytes"

// Apply rebuilds a file's content by replaying only the approved hunks against
// the original DiffLine stream. Unapproved hunks fall back to original lines.
//
// `lines` must be the full DiffLine stream produced by Diff(orig, updated).
// `hunks` must be the result of Hunks(lines, ctx) for the same stream.
// `approvedIdx` is the set of hunk indices the user approved (true == apply).
//
// Behaviour:
//   - len(approvedIdx) == 0 → returns content equivalent to the original file.
//   - All hunks approved → returns content equivalent to the updated file.
//   - Mixed → approved hunks contribute their +/- net effect; rejected hunks
//     contribute the original lines unchanged.
//
// Trailing-newline semantics: the result ends with `\n` iff the contributing
// side at the file's last line did. For an all-approved result that's the
// updated content; for an all-rejected result that's the original; for mixed
// results we use the side that supplied the last produced line. Empty results
// are returned as nil regardless of input newlines.
func Apply(orig, updated []byte, lines []DiffLine, hunks []Hunk, approvedIdx []bool) []byte {
	// Map each line index -> hunk index (-1 if outside any hunk).
	hunkOf := make([]int, len(lines))
	for i := range hunkOf {
		hunkOf[i] = -1
	}
	cursor := 0
	for hi, h := range hunks {
		// Find the segment in `lines` matching this hunk by pointer-equivalent
		// content walk: hunks were built from a contiguous slice of `lines`,
		// so we locate the slice's first line by linear scan from `cursor`.
		// (Diff produces a single slice; equal Text + same Op + same OldLine
		// uniquely identifies a position.)
		first := h.Lines[0]
		for ; cursor < len(lines); cursor++ {
			if lines[cursor].Op == first.Op &&
				lines[cursor].Text == first.Text &&
				lines[cursor].OldLine == first.OldLine &&
				lines[cursor].NewLine == first.NewLine {
				break
			}
		}
		for j := 0; j < len(h.Lines) && cursor+j < len(lines); j++ {
			hunkOf[cursor+j] = hi
		}
		cursor += len(h.Lines)
	}

	approved := func(hi int) bool {
		if hi < 0 || hi >= len(approvedIdx) {
			return false
		}
		return approvedIdx[hi]
	}

	// Walk the full diff stream. Lines outside any hunk are passed through
	// unchanged. Lines inside a hunk are filtered by the approval mask.
	var out []string
	lastFromUpdated := false
	for i, ln := range lines {
		hi := hunkOf[i]
		if hi == -1 {
			out = append(out, ln.Text)
			lastFromUpdated = true // equal context exists on both sides
			continue
		}
		if approved(hi) {
			switch ln.Op {
			case DiffEqual:
				out = append(out, ln.Text)
				lastFromUpdated = true
			case DiffInsert:
				out = append(out, ln.Text)
				lastFromUpdated = true
			case DiffDelete:
				// Dropped.
			}
		} else {
			switch ln.Op {
			case DiffEqual:
				out = append(out, ln.Text)
				lastFromUpdated = true
			case DiffDelete:
				out = append(out, ln.Text)
				lastFromUpdated = false
			case DiffInsert:
				// Dropped.
			}
		}
	}

	if len(out) == 0 {
		return nil
	}

	var buf bytes.Buffer
	for i, s := range out {
		if i > 0 {
			buf.WriteByte('\n')
		}
		buf.WriteString(s)
	}
	// Trailing newline: match the side that provided the last line.
	addNL := false
	if lastFromUpdated {
		addNL = bytes.HasSuffix(updated, []byte{'\n'})
	} else {
		addNL = bytes.HasSuffix(orig, []byte{'\n'})
	}
	if addNL {
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}
