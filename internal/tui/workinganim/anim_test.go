package workinganim

import (
	"image/color"
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
