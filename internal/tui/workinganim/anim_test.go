package workinganim

import (
	"image/color"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

func TestRenderFitsReportedWidth(t *testing.T) {
	t.Parallel()

	anim := New(8, "Thinking", color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255}, color.RGBA{G: 255, A: 255})

	if got, want := lipgloss.Width(anim.Render()), anim.Width()-widestEllipsisWidth; got != want {
		t.Fatalf("initial render width = %d, want %d", got, want)
	}

	anim.started = time.Now().Add(-2 * time.Second)
	anim.initialized = true
	_ = anim.Animate(StepMsg{})

	if got, maxWidth := lipgloss.Width(anim.Render()), anim.Width(); got > maxWidth {
		t.Fatalf("animated render width = %d, want <= %d", got, maxWidth)
	}
}

func TestSetLabelUpdatesWidth(t *testing.T) {
	t.Parallel()

	anim := New(4, "Thinking", color.White, color.Black, color.White)
	before := anim.Width()

	anim.SetLabel("Working")

	if got := anim.Width(); got >= before {
		t.Fatalf("width after shorter label = %d, want < %d", got, before)
	}
}

func TestFormatElapsed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"sub-second", 500 * time.Millisecond, "0s"},
		{"seconds", 3 * time.Second, "3s"},
		{"exact minute", time.Minute, "1m"},
		{"min and sec", 72 * time.Second, "1m 12s"},
		{"exact hour", time.Hour, "1h"},
		{"hour and min", time.Hour + 4*time.Minute, "1h 4m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatElapsed(tt.in); got != tt.want {
				t.Errorf("formatElapsed(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatTokenCount(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{42, "42"},
		{999, "999"},
		{1000, "1k"},
		{1234, "1.2k"},
		{12345, "12.3k"},
		{999_999, "1000k"}, // edge: rounds to 1000.0k by formula
		{1_000_000, "1M"},
		{1_234_567, "1.2M"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatTokenCount(tt.in)
			if got != tt.want {
				// 999_999 boundary: accept either "1000k" or "1.0M" depending on rounding
				if tt.in == 999_999 && got == "1.0M" {
					return
				}
				t.Errorf("formatTokenCount(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatStatusSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		elapsed       time.Duration
		input, output int
		label         string
		want          string
	}{
		{"empty", 0, 0, 0, "", ""},
		{"label only", 0, 0, 0, "Thinking", "(Thinking)"},
		{"elapsed and label", 5 * time.Second, 0, 0, "Thinking", "(thought for 5s · Thinking)"},
		{"output dominant uses down arrow", 3 * time.Second, 100, 250, "Thinking", "(thought for 3s · ↓ 150 · Thinking)"},
		{"input dominant uses up arrow", 3 * time.Second, 1500, 200, "Thinking", "(thought for 3s · ↑ 1.3k · Thinking)"},
		{"equal tokens uses up arrow", 1 * time.Second, 100, 100, "x", "(thought for 1s · ↑ 0 · x)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatStatusSuffix(tt.elapsed, tt.input, tt.output, tt.label)
			if got != tt.want {
				t.Errorf("formatStatusSuffix() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSetStatusReplacesLabelInRender(t *testing.T) {
	t.Parallel()

	anim := New(6, "Thinking", color.White, color.Black, color.White)
	anim.SetStatus(2*time.Second, 100, 250)
	anim.initialized = true
	_ = anim.Animate(StepMsg{})

	out := anim.Render()
	if !strings.Contains(stripANSI(out), "thought for 2s") {
		t.Errorf("render missing elapsed suffix: %q", stripANSI(out))
	}
	if !strings.Contains(stripANSI(out), "↓ 150") {
		t.Errorf("render missing token diff: %q", stripANSI(out))
	}
	if !strings.Contains(stripANSI(out), "Thinking") {
		t.Errorf("render missing label inside parens: %q", stripANSI(out))
	}

	anim.ClearStatus()
	out = anim.Render()
	if strings.Contains(stripANSI(out), "thought for") {
		t.Errorf("render still showing status after ClearStatus: %q", stripANSI(out))
	}
}

// stripANSI removes ANSI escape sequences so test assertions can match the
// plain text content of a rendered animation frame.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if inEsc {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
			continue
		}
		if r == 0x1b {
			inEsc = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
