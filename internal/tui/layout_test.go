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
	if layout.panel.Min.Y <= 0 || layout.panel.Max.Y >= m.height {
		t.Fatalf("panel should keep top/bottom breathing room: panel=%v height=%d", layout.panel, m.height)
	}
	if layout.panel.Max.Y <= layout.input.Min.Y {
		t.Fatalf("panel should float over bottom chrome when tall: panel=%v input=%v", layout.panel, layout.input)
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

func TestComputeTeamLayout_HorizontalSplit(t *testing.T) {
	// 120-wide terminal, lead + 1 teammate → 2 panes side by side (60 each).
	m := Model{
		width: 120, height: 30,
		teamActive: true,
		teamPanes:  []teamPane{{name: "alice", vp: viewport.New(viewport.WithWidth(60), viewport.WithHeight(10))}},
	}
	m.input = textarea.New()
	m.input.SetHeight(1)

	layout := m.computeLayout(image.Rect(0, 0, 120, 30))

	if len(layout.teamPaneRects) != 2 {
		t.Fatalf("want 2 pane rects, got %d", len(layout.teamPaneRects))
	}
	for i, r := range layout.teamPaneRects {
		if r.Dx() <= 0 {
			t.Errorf("pane %d: zero-width rect %v", i, r)
		}
	}
	// Panes should abut (no gap).
	if layout.teamPaneRects[0].Max.X != layout.teamPaneRects[1].Min.X {
		t.Errorf("panes don't abut: rect[0].Max.X=%d rect[1].Min.X=%d",
			layout.teamPaneRects[0].Max.X, layout.teamPaneRects[1].Min.X)
	}
	// Combined width == viewport width.
	total := 0
	for _, r := range layout.teamPaneRects {
		total += r.Dx()
	}
	if total != layout.viewport.Dx() {
		t.Errorf("pane widths %d don't sum to viewport %d", total, layout.viewport.Dx())
	}
}

func TestComputeTeamLayout_VerticalSplit(t *testing.T) {
	// 60-wide terminal: paneW=30 < minPaneW=40 → fall through to vertical.
	m := Model{
		width: 60, height: 40,
		teamActive: true,
		teamPanes:  []teamPane{{name: "alice", vp: viewport.New(viewport.WithWidth(60), viewport.WithHeight(10))}},
	}
	m.input = textarea.New()
	m.input.SetHeight(1)

	layout := m.computeLayout(image.Rect(0, 0, 60, 40))

	if len(layout.teamPaneRects) != 2 {
		t.Fatalf("want 2 pane rects, got %d", len(layout.teamPaneRects))
	}
	for i, r := range layout.teamPaneRects {
		if r.Dy() <= 0 {
			t.Errorf("pane %d: zero-height rect %v", i, r)
		}
		if r.Dx() != 60 {
			t.Errorf("pane %d: width = %d, want 60", i, r.Dx())
		}
	}
	// Stacked: upper pane's bottom == lower pane's top.
	if layout.teamPaneRects[0].Max.Y != layout.teamPaneRects[1].Min.Y {
		t.Errorf("panes don't abut vertically: rect[0].Max.Y=%d rect[1].Min.Y=%d",
			layout.teamPaneRects[0].Max.Y, layout.teamPaneRects[1].Min.Y)
	}
}

func TestComputeTeamLayout_TaskListStrip(t *testing.T) {
	// When teamTaskListVisible, a strip is reserved at the bottom of the viewport area.
	m := Model{
		width: 120, height: 30,
		teamActive:          true,
		teamTaskListVisible: true,
		teamPanes:           []teamPane{{name: "bob", vp: viewport.New(viewport.WithWidth(60), viewport.WithHeight(10))}},
	}
	m.input = textarea.New()
	m.input.SetHeight(1)

	layout := m.computeLayout(image.Rect(0, 0, 120, 30))

	if layout.teamTaskList.Empty() {
		t.Fatal("teamTaskList should be non-empty when teamTaskListVisible=true")
	}
	for i, r := range layout.teamPaneRects {
		if r.Empty() {
			continue
		}
		if r.Max.Y > layout.teamTaskList.Min.Y {
			t.Errorf("pane %d extends into task list strip: pane.Max.Y=%d strip.Min.Y=%d",
				i, r.Max.Y, layout.teamTaskList.Min.Y)
		}
	}
}

func TestComputeTeamPaneGrid_Horizontal(t *testing.T) {
	area := image.Rect(0, 0, 120, 20)
	rects := computeTeamPaneGrid(area, 3, 0)
	if len(rects) != 3 {
		t.Fatalf("want 3 rects, got %d", len(rects))
	}
	for i, r := range rects {
		if r.Dy() != 20 {
			t.Errorf("rect %d height = %d, want 20", i, r.Dy())
		}
	}
	// Verify abutment: each rect's right edge == next rect's left edge.
	for i := range 2 {
		if rects[i].Max.X != rects[i+1].Min.X {
			t.Errorf("rects[%d].Max.X=%d != rects[%d].Min.X=%d", i, rects[i].Max.X, i+1, rects[i+1].Min.X)
		}
	}
}

func TestComputeTeamPaneGrid_FallbackFocus(t *testing.T) {
	// Too narrow for horizontal (each pane would be 15), too short for vertical.
	area := image.Rect(0, 0, 60, 8)
	rects := computeTeamPaneGrid(area, 4, 2)
	if len(rects) != 4 {
		t.Fatalf("want 4 rects, got %d", len(rects))
	}
	// Only the focused pane (index 2) should have area.
	if rects[2] != area {
		t.Errorf("focused pane rect = %v, want %v", rects[2], area)
	}
	for i, r := range rects {
		if i == 2 {
			continue
		}
		if !r.Empty() {
			t.Errorf("non-focused pane %d should be empty, got %v", i, r)
		}
	}
}
