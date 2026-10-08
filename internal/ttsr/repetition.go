package ttsr

import (
	"bytes"
	"strings"
)

// Repetition guard tuning. A loop is a suffix of the output made of one unit
// repeated back to back: at least repMinRepeats times and repMinRun bytes
// overall, so asked-for repetition ("say X ten times") and ordinary reuse of a
// phrase stay well below it.
//
// Short units (under repShortUnit bytes) are what tables, byte arrays, zero
// fills and repeated log lines are made of, so they need far more evidence: 20
// repeats over 2400 bytes, ten distinct bytes in the unit, and no line break
// inside it (line-structured repetition is data, not a stuck model).
const (
	repWindow     = 4096
	repMinUnit    = 8
	repMaxUnit    = 600
	repMinRepeats = 6
	repMinRun     = 1200

	repShortUnit     = 64
	repShortRepeats  = 20
	repShortRun      = 2400
	repShortDistinct = 10

	// repMaxFires bounds how often the guard may interrupt in one turn; the
	// output after that is let through.
	repMaxFires = 2
)

// repetitionCorrection is injected when the guard fires. It deliberately does
// not quote the looped text, which would seed the loop again.
const repetitionCorrection = "Your previous response got stuck repeating the same text over and over and was cut off. " +
	"Do not repeat it. Take stock of what you were doing and continue with the next concrete step, or give a short answer."

var repetitionRule = &Rule{Name: "repetition-guard", Correction: repetitionCorrection}

// loopDetector keeps the last repWindow bytes of one output channel. It
// ignores everything inside an open ``` code fence, where repetition (byte
// tables, generated data) is expected.
type loopDetector struct {
	buf   []byte
	fence bool   // inside an open ``` fence
	prev  string // last two bytes fed, so a fence split across deltas counts
}

func (d *loopDetector) reset() { d.buf, d.fence, d.prev = d.buf[:0], false, "" }

// feed appends s and reports whether the channel is now looping.
func (d *loopDetector) feed(s string) bool {
	if strings.Count(d.prev+s, "```")%2 == 1 {
		d.fence = !d.fence
	}
	if n := len(s); n >= 2 {
		d.prev = s[n-2:]
	} else {
		d.prev = (d.prev + s)[max(0, len(d.prev+s)-2):]
	}
	d.buf = append(d.buf, s...)
	if over := len(d.buf) - repWindow; over > 0 {
		d.buf = append(d.buf[:0], d.buf[over:]...)
	}
	return !d.fence && loopingSuffix(d.buf)
}

// loopingSuffix reports whether b ends in a repeated unit (see the tuning
// constants). A unit built from fewer than three distinct bytes ("-----",
// "ab ab ab") is ignored: that is ASCII-art rulers and padding.
func loopingSuffix(b []byte) bool {
	n := len(b)
	if n < repMinRun {
		return false
	}
	for p := repMinUnit; p <= repMaxUnit && p*repMinRepeats <= n; p++ {
		// Count how far back the period-p equality b[i] == b[i-p] holds.
		run := 0
		for i := n - 1; i-p >= 0 && b[i] == b[i-p]; i-- {
			run++
		}
		span := run + p
		if span < repMinRun || span < repMinRepeats*p {
			continue
		}
		// Judge the run by its smallest repeating unit, so "0, 0, 0, 0, ..."
		// is a three-byte unit however many of them make one period.
		unit := b[n-p:]
		unit = unit[:rootLen(unit)]
		if q := len(unit); q < repShortUnit {
			if span < repShortRun || span < repShortRepeats*q ||
				bytes.IndexByte(unit, '\n') >= 0 || !distinctBytes(unit, repShortDistinct) {
				continue
			}
			return true
		}
		if distinctBytes(unit, 3) {
			return true
		}
	}
	return false
}

// rootLen returns the length of the smallest unit that repeats to make u.
func rootLen(u []byte) int {
	n := len(u)
	for q := 1; q <= n/2; q++ {
		if n%q != 0 {
			continue
		}
		ok := true
		for i := q; i < n; i++ {
			if u[i] != u[i-q] {
				ok = false
				break
			}
		}
		if ok {
			return q
		}
	}
	return n
}

func distinctBytes(b []byte, want int) bool {
	var seen [256]bool
	count := 0
	for _, c := range b {
		if !seen[c] {
			seen[c] = true
			count++
			if count >= want {
				return true
			}
		}
	}
	return false
}

// repetition feeds d and returns the guard's rule when the channel loops.
func (w *Watcher) repetition(d *loopDetector, delta string) (*Rule, bool) {
	if d == nil || w.repFires >= repMaxFires || !d.feed(delta) {
		return nil, false
	}
	w.repFires++
	d.reset()
	return repetitionRule, true
}

// flattenArgs decodes a tool call's JSON arguments into the text rules match:
// every string value (and number/bool, as written), depth first in key order,
// one per line. It also returns the call's file path, if it names one. Input
// that is not a JSON object is matched as is.
func flattenArgs(raw string) (text, path string) {
	var v any
	if err := unmarshalJSON(raw, &v); err != nil {
		return raw, ""
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return raw, ""
	}
	for _, k := range []string{"file_path", "path", "notebook_path", "filePath"} {
		if s, ok := obj[k].(string); ok && s != "" {
			path = s
			break
		}
	}
	var sb strings.Builder
	collectStrings(&sb, v)
	return sb.String(), path
}

// IsRepetition reports whether r is the repetition guard's rule.
func IsRepetition(r *Rule) bool { return r == repetitionRule }
