package pendingedits

import (
	"strings"
)

// MaxDiffLines is the line-count threshold above which Diff skips the Myers
// comparison and returns a single placeholder line. Both sides are counted
// independently; either exceeding the limit triggers the fallback.
const MaxDiffLines = 10_000

// DiffOp tags one line in a unified diff.
type DiffOp int

const (
	// DiffEqual is an unchanged line (context).
	DiffEqual DiffOp = iota
	// DiffInsert is a line present only in the new content.
	DiffInsert
	// DiffDelete is a line present only in the original content.
	DiffDelete
)

// DiffLine is one line of unified-diff output.
type DiffLine struct {
	Op   DiffOp
	Text string // does not include trailing newline
	// OldLine and NewLine are 1-based line numbers in the original/new files,
	// or 0 when the line did not exist on that side.
	OldLine int
	NewLine int
}

// Diff computes a line-level Myers diff between orig and updated. Empty input
// is handled (treated as "no lines"). Returns DiffLine records suitable for
// rendering as a unified diff.
//
// When either side exceeds MaxDiffLines, a single synthetic DiffLine is
// returned with Op==DiffEqual and a placeholder message. This matches the
// design doc's "file too large to render — approve/revert by header only"
// fallback.
func Diff(orig, updated []byte) []DiffLine {
	a := splitLines(orig)
	b := splitLines(updated)
	if len(a) > MaxDiffLines || len(b) > MaxDiffLines {
		return []DiffLine{{
			Op:   DiffEqual,
			Text: "(file too large to render — approve or revert by header only)",
		}}
	}
	return myers(a, b)
}

// DiffString renders Diff(orig, updated) into a unified-diff-style string with
// `+`, `-`, and ` ` prefixes. Used by tests and for plain-text logging.
func DiffString(orig, updated []byte) string {
	var sb strings.Builder
	for _, ln := range Diff(orig, updated) {
		switch ln.Op {
		case DiffInsert:
			sb.WriteByte('+')
		case DiffDelete:
			sb.WriteByte('-')
		default:
			sb.WriteByte(' ')
		}
		sb.WriteString(ln.Text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// splitLines splits b on '\n', producing one entry per line. A trailing
// newline does NOT produce a final empty entry (matches the intuitive line
// count). An empty input produces a zero-length slice.
func splitLines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	s := string(b)
	if strings.HasSuffix(s, "\n") {
		s = s[:len(s)-1]
	}
	return strings.Split(s, "\n")
}

// myers runs the classic Myers diff and converts the resulting edit script
// into a unified-diff line stream. Implementation is the standard
// O((N+M)*D) algorithm with explicit V-vector snapshots to recover the path.
//
// For modest file sizes this is fast and correct; we don't need the more
// elaborate linear-space variant given the MaxDiffLines cap.
func myers(a, b []string) []DiffLine {
	n, m := len(a), len(b)
	max := n + m
	if max == 0 {
		return nil
	}

	// trace holds a snapshot of the V vector after each D step. V is indexed
	// from -max..+max, so we offset by `max` everywhere.
	trace := make([][]int, 0, max+1)
	v := make([]int, 2*max+1)

	for d := 0; d <= max; d++ {
		snapshot := make([]int, len(v))
		copy(snapshot, v)
		trace = append(trace, snapshot)

		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[k-1+max] < v[k+1+max]) {
				x = v[k+1+max] // down (insertion)
			} else {
				x = v[k-1+max] + 1 // right (deletion)
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
			}
			v[k+max] = x
			if x >= n && y >= m {
				return backtrack(trace, a, b, max)
			}
		}
	}
	// Should be unreachable for finite inputs — fall back to "everything different".
	out := make([]DiffLine, 0, n+m)
	for i, ln := range a {
		out = append(out, DiffLine{Op: DiffDelete, Text: ln, OldLine: i + 1})
	}
	for j, ln := range b {
		out = append(out, DiffLine{Op: DiffInsert, Text: ln, NewLine: j + 1})
	}
	return out
}

// backtrack walks the saved V-vector snapshots in reverse to reconstruct the
// edit script, then converts it into chronological DiffLine records.
func backtrack(trace [][]int, a, b []string, max int) []DiffLine {
	x, y := len(a), len(b)
	var ops []DiffLine // collected in reverse, flipped at the end
	for d := len(trace) - 1; d > 0; d-- {
		v := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && v[k-1+max] < v[k+1+max]) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := v[prevK+max]
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			ops = append(ops, DiffLine{
				Op:      DiffEqual,
				Text:    a[x-1],
				OldLine: x,
				NewLine: y,
			})
			x--
			y--
		}
		if d > 0 {
			if x == prevX {
				ops = append(ops, DiffLine{
					Op:      DiffInsert,
					Text:    b[y-1],
					NewLine: y,
				})
			} else {
				ops = append(ops, DiffLine{
					Op:      DiffDelete,
					Text:    a[x-1],
					OldLine: x,
				})
			}
			x = prevX
			y = prevY
		}
	}
	// Any remaining matched prefix is pure equal lines.
	for x > 0 && y > 0 {
		ops = append(ops, DiffLine{
			Op:      DiffEqual,
			Text:    a[x-1],
			OldLine: x,
			NewLine: y,
		})
		x--
		y--
	}
	for x > 0 {
		ops = append(ops, DiffLine{Op: DiffDelete, Text: a[x-1], OldLine: x})
		x--
	}
	for y > 0 {
		ops = append(ops, DiffLine{Op: DiffInsert, Text: b[y-1], NewLine: y})
		y--
	}
	// Reverse in place — ops was built tail-first.
	for i, j := 0, len(ops)-1; i < j; i, j = i+1, j-1 {
		ops[i], ops[j] = ops[j], ops[i]
	}
	return ops
}
