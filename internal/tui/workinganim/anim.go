// Package workinganim provides the one-line animated work indicator used by
// the TUI while the assistant is thinking.
package workinganim

import (
	"image/color"
	"math/rand/v2"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	fps                 = 20
	defaultSize         = 12
	labelGap            = " "
	maxBirthOffset      = time.Second
	ellipsisFrameSpeed  = 8
	defaultFrameCount   = 36
	widestEllipsisWidth = 3
)

var (
	availableRunes = []rune("0123456789abcdefABCDEF~!@#$%^&*+=_")
	ellipsisFrames = []string{".", "..", "...", ""}
)

type StepMsg struct{}

type Anim struct {
	size          int
	label         string
	labelWidth    int
	labelColor    color.Color
	gradFromColor color.Color
	gradToColor   color.Color
	bgColor       color.Color
	started       time.Time
	birthOffsets  []time.Duration
	frames        [][]string
	labelFrames   []string
	ellipsis      []string
	step          int
	ellipsisStep  int
	initialized   bool
}

func New(size int, label string, gradFromColor, gradToColor, labelColor color.Color, bgColor ...color.Color) *Anim {
	if size < 1 {
		size = defaultSize
	}
	var bg color.Color
	if len(bgColor) > 0 {
		bg = bgColor[0]
	}
	a := &Anim{
		size:          size,
		labelColor:    labelColor,
		gradFromColor: gradFromColor,
		gradToColor:   gradToColor,
		bgColor:       bg,
		started:       time.Now(),
	}
	a.frames = renderFrames(size, gradFromColor, gradToColor, bg)
	a.birthOffsets = make([]time.Duration, size)
	for i := range a.birthOffsets {
		a.birthOffsets[i] = time.Duration(rand.N(int64(maxBirthOffset)))
	}
	a.SetLabel(label)
	return a
}

func (a *Anim) SetLabel(label string) {
	if a.label == label {
		return
	}
	a.label = label
	a.labelWidth = lipgloss.Width(label)
	a.labelFrames = renderChars(label, a.labelColor, a.bgColor)
	a.ellipsis = make([]string, 0, len(ellipsisFrames))
	style := lipgloss.NewStyle().Foreground(a.labelColor)
	if a.bgColor != nil {
		style = style.Background(a.bgColor)
	}
	for _, frame := range ellipsisFrames {
		a.ellipsis = append(a.ellipsis, style.Render(frame))
	}
}

func (a *Anim) SetColors(gradFromColor, gradToColor, labelColor color.Color) {
	a.SetColorsWithBackground(gradFromColor, gradToColor, labelColor, a.bgColor)
}

func (a *Anim) SetColorsWithBackground(gradFromColor, gradToColor, labelColor, bgColor color.Color) {
	if sameColor(a.gradFromColor, gradFromColor) &&
		sameColor(a.gradToColor, gradToColor) &&
		sameColor(a.labelColor, labelColor) &&
		sameColor(a.bgColor, bgColor) {
		return
	}
	a.gradFromColor = gradFromColor
	a.gradToColor = gradToColor
	a.labelColor = labelColor
	a.bgColor = bgColor
	a.frames = renderFrames(a.size, gradFromColor, gradToColor, bgColor)
	label := a.label
	a.label = ""
	a.SetLabel(label)
}

func (a *Anim) Start() tea.Cmd {
	return a.Step()
}

func (a *Anim) Animate(StepMsg) tea.Cmd {
	a.step++
	if a.step >= len(a.frames) {
		a.step = 0
	}
	if a.initialized {
		a.ellipsisStep++
		if a.ellipsisStep >= ellipsisFrameSpeed*len(ellipsisFrames) {
			a.ellipsisStep = 0
		}
	} else if time.Since(a.started) >= maxBirthOffset {
		a.initialized = true
	}
	return a.Step()
}

func (a *Anim) Render() string {
	if len(a.frames) == 0 {
		return ""
	}
	frame := a.frames[a.step%len(a.frames)]
	var b strings.Builder
	for i := range a.size {
		if !a.initialized && i < len(a.birthOffsets) && time.Since(a.started) < a.birthOffsets[i] {
			style := lipgloss.NewStyle().Foreground(a.gradFromColor)
			if a.bgColor != nil {
				style = style.Background(a.bgColor)
			}
			b.WriteString(style.Render("."))
			continue
		}
		b.WriteString(frame[i])
	}
	if a.labelWidth > 0 {
		if a.bgColor != nil {
			b.WriteString(lipgloss.NewStyle().Background(a.bgColor).Render(labelGap))
		} else {
			b.WriteString(labelGap)
		}
		for _, ch := range a.labelFrames {
			b.WriteString(ch)
		}
		if a.initialized && len(a.ellipsis) > 0 {
			b.WriteString(a.ellipsis[(a.ellipsisStep/ellipsisFrameSpeed)%len(a.ellipsis)])
		}
	}
	return b.String()
}

func (a *Anim) Width() int {
	width := a.size
	if a.labelWidth > 0 {
		width += lipgloss.Width(labelGap) + a.labelWidth + widestEllipsisWidth
	}
	return width
}

func (a *Anim) Step() tea.Cmd {
	return tea.Tick(time.Second/time.Duration(fps), func(time.Time) tea.Msg {
		return StepMsg{}
	})
}

func renderFrames(size int, gradFromColor, gradToColor, bgColor color.Color) [][]string {
	if size < 1 {
		size = defaultSize
	}
	ramp := lipgloss.Blend1D(size*2, gradFromColor, gradToColor)
	if len(ramp) == 0 {
		ramp = []color.Color{gradFromColor}
	}
	frames := make([][]string, defaultFrameCount)
	for i := range frames {
		frames[i] = make([]string, size)
		for j := range size {
			r := availableRunes[rand.IntN(len(availableRunes))]
			c := ramp[(i+j)%len(ramp)]
			style := lipgloss.NewStyle().Foreground(c)
			if bgColor != nil {
				style = style.Background(bgColor)
			}
			frames[i][j] = style.Render(string(r))
		}
	}
	return frames
}

func renderChars(s string, c, bgColor color.Color) []string {
	if s == "" {
		return nil
	}
	style := lipgloss.NewStyle().Foreground(c)
	if bgColor != nil {
		style = style.Background(bgColor)
	}
	chars := make([]string, 0, len([]rune(s)))
	for _, r := range s {
		chars = append(chars, style.Render(string(r)))
	}
	return chars
}

func sameColor(a, b color.Color) bool {
	if a == nil || b == nil {
		return a == b
	}
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}
