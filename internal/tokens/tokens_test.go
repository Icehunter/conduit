package tokens

import (
	"strings"
	"testing"
)

func TestEstimate_Empty(t *testing.T) {
	if got := Estimate(""); got != 0 {
		t.Errorf("Estimate(\"\") = %d; want 0", got)
	}
}

func TestEstimate_HelloWorld(t *testing.T) {
	// "hello world" tokenizes to 2 tokens under cl100k_base.
	got := Estimate("hello world")
	if got != 2 {
		t.Errorf("Estimate(hello world) = %d; want 2", got)
	}
}

func TestEstimate_LongerStringMonotonic(t *testing.T) {
	// Adding more text should never decrease the count.
	short := Estimate("short")
	long := Estimate(strings.Repeat("a longer test string. ", 50))
	if long < short {
		t.Errorf("longer string returned fewer tokens: short=%d long=%d", short, long)
	}
	if long < 100 {
		t.Errorf("expected ~hundreds of tokens for long text; got %d", long)
	}
}

func TestEstimate_NotJustCharsOver4(t *testing.T) {
	// Sanity check that we're using a real tokenizer, not the chars/4
	// fallback. Whitespace is highly compressible (e.g. "          " is 1
	// token under cl100k but chars/4 says 2 or 3) so it's a clear signal.
	got := Estimate("                              ")
	if got > 5 {
		t.Errorf("Estimate of 30 spaces = %d; cl100k should produce ≤5; chars/4 fallback would give 7", got)
	}
}

func TestEstimateMany_Sums(t *testing.T) {
	parts := []string{"hello", " ", "world"}
	sum := Estimate("hello") + Estimate(" ") + Estimate("world")
	if got := EstimateMany(parts); got != sum {
		t.Errorf("EstimateMany = %d; want sum %d", got, sum)
	}
}
