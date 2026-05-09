package pendingedits

// DefaultHunkContext is the number of unchanged context lines kept on each
// side of a change region. Three matches the `git diff -U3` default.
const DefaultHunkContext = 3

// Hunk is a contiguous run of DiffLine records (changes plus surrounding
// equal-line context) that the user can approve or revert as a unit.
//
// OldStart / NewStart are 1-based line numbers in the original / new file at
// the first line of the hunk (or 0 if the hunk has no lines on that side).
// Length fields count how many lines from each side the hunk represents,
// matching the unified-diff `@@ -a,b +c,d @@` semantics.
type Hunk struct {
	Lines     []DiffLine
	OldStart  int
	OldLength int
	NewStart  int
	NewLength int
}

// HasChanges reports whether the hunk contains any insert or delete lines.
// A hunk made up entirely of equal lines is degenerate and should not be
// produced by Hunks; the predicate exists for defensive callers.
func (h Hunk) HasChanges() bool {
	for _, ln := range h.Lines {
		if ln.Op != DiffEqual {
			return true
		}
	}
	return false
}

// Hunks groups a flat DiffLine stream into change regions surrounded by up to
// `context` lines of equal-line context on each side. Adjacent change regions
// whose context windows overlap are merged into a single hunk.
//
// A diff with no changes returns a nil slice. A diff with only changes (no
// equal lines anywhere) returns a single hunk containing every line.
func Hunks(lines []DiffLine, context int) []Hunk {
	if len(lines) == 0 {
		return nil
	}
	context = max(context, 0)

	// Locate every index where Op != DiffEqual.
	var changeIdx []int
	for i, ln := range lines {
		if ln.Op != DiffEqual {
			changeIdx = append(changeIdx, i)
		}
	}
	if len(changeIdx) == 0 {
		return nil
	}

	// Build [start,end] line-index ranges by expanding each change index by
	// `context` and merging overlapping windows.
	type hunkRange struct{ start, end int }
	var ranges []hunkRange
	for _, ci := range changeIdx {
		s := ci - context
		s = max(s, 0)
		e := ci + context
		if e > len(lines)-1 {
			e = len(lines) - 1
		}
		if len(ranges) == 0 || s > ranges[len(ranges)-1].end+1 {
			ranges = append(ranges, hunkRange{s, e})
		} else if e > ranges[len(ranges)-1].end {
			ranges[len(ranges)-1].end = e
		}
	}

	out := make([]Hunk, 0, len(ranges))
	for _, r := range ranges {
		segment := lines[r.start : r.end+1]
		h := Hunk{Lines: append([]DiffLine(nil), segment...)}
		// Compute 1-based start+length for both sides by scanning the segment.
		for _, ln := range segment {
			if ln.OldLine > 0 {
				if h.OldStart == 0 {
					h.OldStart = ln.OldLine
				}
				h.OldLength++
			}
			if ln.NewLine > 0 {
				if h.NewStart == 0 {
					h.NewStart = ln.NewLine
				}
				h.NewLength++
			}
		}
		out = append(out, h)
	}
	return out
}
