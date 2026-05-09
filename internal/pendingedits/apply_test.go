package pendingedits

import (
	"bytes"
	"testing"
)

func TestHunks(t *testing.T) {
	tests := []struct {
		name    string
		orig    string
		updated string
		ctx     int
		want    int // expected number of hunks
	}{
		{"no changes", "a\nb\nc\n", "a\nb\nc\n", 3, 0},
		{"single change", "a\nb\nc\n", "a\nB\nc\n", 3, 1},
		{
			"two distant changes — separate hunks",
			"a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n",
			"A\nb\nc\nd\ne\nf\ng\nh\ni\nJ\n",
			1,
			2,
		},
		{
			"two distant changes with wide context — merged",
			"a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n",
			"A\nb\nc\nd\ne\nf\ng\nh\ni\nJ\n",
			10,
			1,
		},
		{"empty file -> populated", "", "x\ny\n", 3, 1},
		{"populated -> empty", "x\ny\n", "", 3, 1},
		{"all-different", "a\nb\nc\n", "x\ny\nz\n", 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Diff([]byte(tt.orig), []byte(tt.updated))
			got := Hunks(d, tt.ctx)
			if len(got) != tt.want {
				t.Fatalf("Hunks(...) = %d hunks, want %d\nlines=%v\nhunks=%v",
					len(got), tt.want, d, got)
			}
			for i, h := range got {
				if !h.HasChanges() {
					t.Errorf("hunk %d has no changes — Hunks() must not produce all-equal hunks", i)
				}
			}
		})
	}
}

func TestApplyRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		orig    string
		updated string
	}{
		{"no trailing newline", "a\nb\nc", "a\nB\nc"},
		{"trailing newline", "a\nb\nc\n", "a\nB\nc\n"},
		{"create file", "", "hello\nworld\n"},
		{"delete file", "hello\nworld\n", ""},
		{"multi-region", "a\nb\nc\nd\ne\nf\ng\nh\n", "A\nb\nc\nd\ne\nf\ng\nH\n"},
		{"insert in middle", "a\nb\nc\n", "a\nx\ny\nb\nc\n"},
		{"unicode", "α\nβ\nγ\n", "α\nB\nγ\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orig := []byte(tt.orig)
			updated := []byte(tt.updated)
			lines := Diff(orig, updated)
			hunks := Hunks(lines, 3)

			// All-rejected → must round-trip to orig.
			rejected := make([]bool, len(hunks))
			gotRejected := Apply(orig, updated, lines, hunks, rejected)
			if !bytes.Equal(gotRejected, orig) {
				t.Errorf("all-rejected: got %q, want %q", gotRejected, orig)
			}

			// All-approved → must round-trip to updated.
			approved := make([]bool, len(hunks))
			for i := range approved {
				approved[i] = true
			}
			gotApproved := Apply(orig, updated, lines, hunks, approved)
			if !bytes.Equal(gotApproved, updated) {
				t.Errorf("all-approved: got %q, want %q", gotApproved, updated)
			}
		})
	}
}

func TestApplyMixed(t *testing.T) {
	// Two independent changes; approve only the second.
	orig := []byte("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n")
	updated := []byte("A\nb\nc\nd\ne\nf\ng\nh\ni\nJ\n")
	lines := Diff(orig, updated)
	hunks := Hunks(lines, 1)
	if len(hunks) != 2 {
		t.Fatalf("want 2 hunks, got %d", len(hunks))
	}
	got := Apply(orig, updated, lines, hunks, []bool{false, true})
	want := []byte("a\nb\nc\nd\ne\nf\ng\nh\ni\nJ\n")
	if !bytes.Equal(got, want) {
		t.Errorf("mixed: got %q, want %q", got, want)
	}

	// Approve only the first.
	got2 := Apply(orig, updated, lines, hunks, []bool{true, false})
	want2 := []byte("A\nb\nc\nd\ne\nf\ng\nh\ni\nj\n")
	if !bytes.Equal(got2, want2) {
		t.Errorf("mixed (other side): got %q, want %q", got2, want2)
	}
}

func TestApplyNoChanges(t *testing.T) {
	orig := []byte("a\nb\nc\n")
	lines := Diff(orig, orig)
	hunks := Hunks(lines, 3)
	if len(hunks) != 0 {
		t.Fatalf("want 0 hunks, got %d", len(hunks))
	}
	got := Apply(orig, orig, lines, hunks, nil)
	if !bytes.Equal(got, orig) {
		t.Errorf("no-changes Apply diverged: got %q, want %q", got, orig)
	}
}

func TestHunksContextZero(t *testing.T) {
	// With context=0, two changes one line apart should still merge if their
	// expanded ranges abut. Ranges only abut at distance ≤ context+1.
	orig := []byte("a\nb\nc\n")
	updated := []byte("A\nb\nC\n")
	lines := Diff(orig, updated)
	got := Hunks(lines, 0)
	if len(got) != 2 {
		t.Errorf("ctx=0 with one equal line between: want 2 hunks, got %d", len(got))
	}
}
