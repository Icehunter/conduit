package tui

import (
	"image"
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	floatingHeaderPadX = 1
	floatingBodyPadX   = 2
	floatingBodyPadY   = 1
)

type floatingSpec struct {
	minWidth  int
	maxWidth  int
	minHeight int
	maxHeight int
}

var (
	floatingPickerSpec      = floatingSpec{minWidth: 52, maxWidth: 96, minHeight: 5, maxHeight: 18}
	floatingModelPickerSpec = floatingSpec{minWidth: 86, maxWidth: 132, minHeight: 26, maxHeight: 26}
	floatingCommandSpec     = floatingSpec{minWidth: 72, maxWidth: 120, minHeight: 18, maxHeight: 18}
	floatingModalSpec       = floatingSpec{minWidth: 56, maxWidth: 100, minHeight: 7, maxHeight: 22}
)

func floatingWindowStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(colorWindowBorder).
		Background(colorWindowBg).
		BorderBackground(colorWindowBg)
}

func floatingHeaderStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(colorWindowTitle).
		Background(colorWindowBg).
		PaddingLeft(floatingHeaderPadX).
		PaddingRight(floatingHeaderPadX)
}

func floatingBodyStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(colorFg).
		Background(colorWindowBg).
		PaddingLeft(floatingBodyPadX).
		PaddingRight(floatingBodyPadX).
		PaddingTop(floatingBodyPadY).
		PaddingBottom(floatingBodyPadY)
}

func floatingOuterWidth(areaWidth int, spec floatingSpec) int {
	available := areaWidth - 2
	if available < 1 {
		available = areaWidth
	}
	if available < 1 {
		return 1
	}
	maxWidth := spec.maxWidth
	if maxWidth <= 0 || maxWidth > available {
		maxWidth = available
	}
	minWidth := spec.minWidth
	if minWidth <= 0 {
		minWidth = 1
	}
	if minWidth > maxWidth {
		minWidth = maxWidth
	}
	return clampInt(areaWidth*7/10, minWidth, maxWidth)
}

func floatingOuterHeight(content string, areaHeight int, spec floatingSpec) int {
	style := floatingWindowStyle()
	frame := style.GetVerticalFrameSize()
	want := floatingContentHeight(content) + frame
	maxHeight := spec.maxHeight
	if maxHeight <= 0 || maxHeight > areaHeight {
		maxHeight = areaHeight
	}
	minHeight := spec.minHeight
	if minHeight <= 0 {
		minHeight = 1
	}
	if minHeight > maxHeight {
		minHeight = maxHeight
	}
	return clampInt(want, minHeight, maxHeight)
}

func floatingInnerWidth(areaWidth int, spec floatingSpec) int {
	style := floatingWindowStyle()
	outer := floatingOuterWidth(areaWidth, spec)
	inner := outer - style.GetHorizontalFrameSize()
	if inner < 1 {
		return 1
	}
	return inner
}

func floatingRect(area image.Rectangle, width, height int) image.Rectangle {
	if width > area.Dx() {
		width = area.Dx()
	}
	if height > area.Dy() {
		height = area.Dy()
	}
	width = max(width, 1)
	height = max(height, 1)
	x := area.Min.X + (area.Dx()-width)/2
	y := area.Min.Y + (area.Dy()-height)/2
	return image.Rect(x, y, x+width, y+height)
}

func floatingRectAbove(area image.Rectangle, width, height int) image.Rectangle {
	rect := floatingRect(area, width, height)
	return rect.Add(image.Pt(0, area.Max.Y-rect.Max.Y))
}

func renderFloatingWindow(content string, width, height int) string {
	width = max(width, 1)
	height = max(height, 1)
	content = decorateFloatingHeader(content, width)
	innerW := width - floatingWindowStyle().GetHorizontalFrameSize()
	innerW = max(innerW, 1)
	innerH := height - floatingWindowStyle().GetVerticalFrameSize()
	innerH = max(innerH, 1)
	header, body := splitFloatingContent(content)
	renderedHeader := floatingHeaderStyle().Width(innerW).MaxWidth(innerW).Render(header)
	bodyH := innerH - lipgloss.Height(renderedHeader)
	bodyH = max(bodyH, 1)
	renderedBody := floatingBodyStyle().Width(innerW).Height(bodyH).Render(body)
	return floatingWindowStyle().Width(width).Height(height).Render(renderedHeader + "\n" + renderedBody)
}

func decorateFloatingHeader(content string, outerWidth int) string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return content
	}
	innerW := outerWidth - floatingWindowStyle().GetHorizontalFrameSize() - floatingHeaderPadX*2
	if innerW < 8 {
		return content
	}
	title := lines[0]
	if strings.Contains(title, "////") {
		return content
	}
	titleW := lipgloss.Width(title)
	fillW := innerW - titleW - 2
	if fillW < 6 {
		return content
	}
	lines[0] = title + surfaceSpaces(2) + ornamentGradientText(strings.Repeat("/", fillW))
	return strings.Join(lines, "\n")
}

func splitFloatingContent(content string) (string, string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return "", ""
	}
	body := ""
	if len(lines) > 1 {
		body = strings.Join(lines[1:], "\n")
		body = strings.TrimPrefix(body, "\n")
	}
	return lines[0], body
}

func floatingContentHeight(content string) int {
	header, body := splitFloatingContent(content)
	h := lipgloss.Height(header)
	if strings.TrimSpace(body) != "" {
		h += lipgloss.Height(body) + floatingBodyPadY*2
	}
	return h
}
