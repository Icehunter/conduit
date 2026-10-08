package bashtool

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestOutputCapture(t *testing.T) {
	chunk := bytes.Repeat([]byte("0123456789\n"), 10) // 110 bytes, 10 lines
	tests := []struct {
		name      string
		threshold int64
		diskCap   int64
		writes    int
		wantMem   bool
		wantKill  bool
		wantFile  int64 // bytes in the spill file; -1 means no file
	}{
		{"stays in memory", 1 << 20, 1 << 30, 5, true, false, -1},
		{"exactly at threshold stays in memory", 550, 1 << 30, 5, true, false, -1},
		{"spills past threshold", 300, 1 << 30, 5, false, false, 550},
		{"killed at the cap keeps the partial output", 300, 400, 5, false, true, 400},
		{"killed while still in memory still saves the partial output", 1 << 20, 400, 5, false, true, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kills := 0
			c := &outputCapture{threshold: tt.threshold, diskCap: tt.diskCap, onKill: func() { kills++ }}
			var all []byte
			for range tt.writes {
				n, err := c.Write(chunk)
				if err != nil || n != len(chunk) {
					t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(chunk))
				}
				all = append(all, chunk...)
			}
			c.finish()

			if raw, ok := c.inMemory(); ok != tt.wantMem || (ok && raw != string(all)) {
				t.Errorf("inMemory = %v (%d bytes), want %v", ok, len(raw), tt.wantMem)
			}
			if c.wasKilled() != tt.wantKill || (kills == 1) != tt.wantKill || kills > 1 {
				t.Errorf("killed = %v with %d onKill calls, want killed %v", c.wasKilled(), kills, tt.wantKill)
			}
			path, err := c.spillPath()
			if tt.wantFile < 0 {
				if path != "" || err != nil {
					t.Errorf("spill file = %q, %v; want none", path, err)
				}
				return
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read spill file %q: %v", path, err)
			}
			if !bytes.Equal(data, all[:tt.wantFile]) {
				t.Errorf("spill file holds %d bytes, want the first %d written", len(data), tt.wantFile)
			}
		})
	}
}

// TestOutputCapture_Excerpt pins the marker arithmetic: the lines it reports
// as omitted are exactly the ones between the head and the tail.
func TestOutputCapture_Excerpt(t *testing.T) {
	c := &outputCapture{threshold: 1 << 10, diskCap: 1 << 30}
	for _, line := range strings.SplitAfter(seqOutput(5000), "\n") {
		if _, err := c.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	c.finish()
	if got := c.excerpt(200, 1, true); !strings.HasPrefix(got, "1\n\n... [4998 lines / ") || !strings.HasSuffix(got, "] ...\n\n5000") {
		t.Errorf("one-line budget: excerpt = %q, want one line at each end", got)
	}
	got := c.excerpt(200, 20, true)
	head, rest, ok := strings.Cut(got, "\n\n... [")
	if !ok {
		t.Fatalf("no marker in %q", got)
	}
	marker, tail, ok := strings.Cut(rest, "] ...\n\n")
	if !ok {
		t.Fatalf("no marker end in %q", got)
	}
	if head != "1\n2\n3\n4\n5\n6\n7\n8\n9\n10" {
		t.Errorf("head = %q, want the first 10 lines", head)
	}
	if tail != "4991\n4992\n4993\n4994\n4995\n4996\n4997\n4998\n4999\n5000" {
		t.Errorf("tail = %q, want the last 10 lines", tail)
	}
	if !strings.HasPrefix(marker, "4980 lines / ") || !strings.Contains(marker, "full output: ") {
		t.Errorf("marker = %q, want 4980 lines omitted and the spill path", marker)
	}
}

func TestFitOutput(t *testing.T) {
	tests := []struct {
		name        string
		text        string
		maxBytes    int
		maxLines    int
		wantWhole   bool
		wantHead    string
		wantTail    string
		wantOmitted string
	}{
		{"fits", "a\nb\nc", 100, 10, true, "", "", ""},
		{"too many lines", "1\n2\n3\n4\n5\n6\n7\n8", 100, 4, false, "1\n2", "7\n8", "4 lines / "},
		{"too many bytes", strings.Repeat("x", 30) + "\n" + strings.Repeat("y", 30), 40, 10, false, "", "", "0 lines / 21 bytes"},
		{"one-line budget still keeps a line at each end", "1\n2\n3\n4", 100, 1, false, "1\n\n", "\n\n4", "2 lines / "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fitOutput(tt.text, tt.maxBytes, tt.maxLines)
			if tt.wantWhole {
				if got != tt.text {
					t.Errorf("fitOutput = %q, want the text unchanged", got)
				}
				return
			}
			if !strings.HasPrefix(got, tt.wantHead) || !strings.HasSuffix(got, tt.wantTail) {
				t.Errorf("fitOutput = %q, want head %q and tail %q", got, tt.wantHead, tt.wantTail)
			}
			if !strings.Contains(got, tt.wantOmitted) {
				t.Errorf("fitOutput = %q, want marker containing %q", got, tt.wantOmitted)
			}
			m := spillPathRe.FindStringSubmatch(got)
			if m == nil {
				t.Fatalf("fitOutput = %q names no spill file", got)
			}
			data, err := os.ReadFile(m[1])
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tt.text {
				t.Errorf("spill file = %q, want the full text", data)
			}
		})
	}
}

// TestFitOutput_RuneSafe: a line wider than the budget is clipped on rune
// boundaries at both ends.
func TestFitOutput_RuneSafe(t *testing.T) {
	got := fitOutput(strings.Repeat("héllo", 100), 101, 10)
	if !utf8.ValidString(got) {
		t.Errorf("fitOutput cut inside a rune: %q", got)
	}
}
