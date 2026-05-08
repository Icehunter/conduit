package tui

import "image"

type uiLayout struct {
	viewport    image.Rectangle
	todoStrip   image.Rectangle
	workingRow  image.Rectangle
	input       image.Rectangle
	coordinator image.Rectangle
	footer      image.Rectangle
	pickerArea  image.Rectangle
	panel       image.Rectangle
}

func (m Model) computeLayout(area image.Rectangle) uiLayout {
	width := area.Dx()
	height := area.Dy()
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}

	footerRows := m.footerChromeRows()
	if footerRows < 1 {
		footerRows = 1
	}
	if footerRows > height {
		footerRows = height
	}

	inputRows := m.input.Height()
	if inputRows < inputMinRows {
		inputRows = inputMinRows
	}
	if inputRows > inputMaxRows {
		inputRows = inputMaxRows
	}
	inputRows += 2 // border
	if len(m.pendingImages)+len(m.pendingPDFs) > 0 {
		inputRows++
	}

	coordRows := renderedLineCount(renderCoordinatorPanel(width))

	footerTop := area.Max.Y - footerRows
	inputBottom := footerTop - coordRows
	inputTop := inputBottom - inputRows
	if inputTop < area.Min.Y+1 {
		inputTop = area.Min.Y + 1
	}
	workingTop := inputTop - 1
	if workingTop < area.Min.Y {
		workingTop = area.Min.Y
	}

	// Todo strip sits between the working row and the chat viewport.
	// todoStripRows() returns 0 when hidden or empty — strip collapses.
	todoRows := m.todoStripRows()
	todoStripTop := workingTop - todoRows
	if todoStripTop < area.Min.Y {
		todoStripTop = area.Min.Y
	}

	panel := image.Rect(area.Min.X+6, area.Min.Y+2, area.Max.X-6, area.Max.Y-2)
	if panel.Dx() < 20 || panel.Dy() < 8 {
		panel = image.Rect(area.Min.X, area.Min.Y, area.Max.X, area.Max.Y)
	}

	layout := uiLayout{
		viewport:    image.Rect(area.Min.X, area.Min.Y, area.Max.X, todoStripTop),
		todoStrip:   image.Rect(area.Min.X, todoStripTop, area.Max.X, workingTop),
		workingRow:  image.Rect(area.Min.X, workingTop, area.Max.X, inputTop),
		input:       image.Rect(area.Min.X, inputTop, area.Max.X, inputBottom),
		coordinator: image.Rect(area.Min.X, inputBottom, area.Max.X, footerTop),
		footer:      image.Rect(area.Min.X, footerTop, area.Max.X, area.Max.Y),
		pickerArea:  image.Rect(area.Min.X, area.Min.Y, area.Max.X, inputTop),
		panel:       panel,
	}
	if layout.viewport.Dy() < 1 {
		layout.viewport.Max.Y = layout.viewport.Min.Y
	}
	return layout
}

func renderedLineCount(s string) int {
	if s == "" {
		return 0
	}
	return 1 + stringsCountByte(s, '\n')
}

func stringsCountByte(s string, b byte) int {
	count := 0
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			count++
		}
	}
	return count
}
