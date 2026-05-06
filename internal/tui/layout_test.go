package tui

import (
	"image"
	"testing"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
)

func TestComputeLayout_ReservesInputAndFooterRows(t *testing.T) {
	m := Model{width: 100, height: 30, usageStatusEnabled: true}
	m.input = textarea.New()
	m.input.SetHeight(1)

	layout := m.computeLayout(image.Rect(0, 0, m.width, m.height))
	if layout.footer.Dy() != 5 {
		t.Fatalf("footer height = %d, want 5", layout.footer.Dy())
	}
	if layout.workingRow.Dy() != 1 {
		t.Fatalf("working row height = %d, want 1", layout.workingRow.Dy())
	}
	if layout.input.Min.Y <= layout.viewport.Max.Y {
		t.Fatalf("input overlaps viewport: viewport=%v input=%v", layout.viewport, layout.input)
	}
}

func TestView_DoesNotShrinkViewportForPicker(t *testing.T) {
	m := Model{ready: true, width: 100, height: 30}
	m.input = textarea.New()
	m.input.SetHeight(1)
	m.vp = viewport.New(viewport.WithWidth(100), viewport.WithHeight(24))
	m.atMatches = []string{"README.md", "STATUS.md", "internal/tui/model.go"}

	before := m.vp.Height()
	_ = m.View()
	after := m.vp.Height()
	if after != before {
		t.Fatalf("View changed viewport height from %d to %d", before, after)
	}
}

func TestFloatingWindowClampsToMinMax(t *testing.T) {
	spec := floatingSpec{minWidth: 40, maxWidth: 80, minHeight: 6, maxHeight: 12}

	width := floatingOuterWidth(200, spec)
	if width != 80 {
		t.Fatalf("width = %d, want max clamp 80", width)
	}

	height := floatingOuterHeight("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten", 40, spec)
	if height != 12 {
		t.Fatalf("height = %d, want max clamp 12", height)
	}

	narrowWidth := floatingOuterWidth(44, spec)
	if narrowWidth != 40 {
		t.Fatalf("narrow width = %d, want min clamp 40", narrowWidth)
	}
}

func TestFloatingRectAboveAnchorsToBottom(t *testing.T) {
	area := image.Rect(0, 2, 100, 22)
	rect := floatingRectAbove(area, 40, 8)
	if rect.Max.Y != area.Max.Y {
		t.Fatalf("rect = %v, want bottom anchored at %d", rect, area.Max.Y)
	}
	if rect.Min.X != 30 {
		t.Fatalf("rect.Min.X = %d, want centered at 30", rect.Min.X)
	}
}
