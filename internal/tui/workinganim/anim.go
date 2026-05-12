// Package workinganim provides the one-line animated work indicator used by
// the TUI while the assistant is thinking.
package workinganim

import (
	"fmt"
	"image/color"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/lucasb-eyer/go-colorful"
)

const (
	fps                 = 20
	defaultSize         = 12
	labelGap            = " "
	maxBirthOffset      = time.Second
	ellipsisFrameSpeed  = 8
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
	birthDots     []string // pre-rendered '.' chars at each position's gradient color
	labelFrames   []string
	ellipsis      []string
	step          int
	ellipsisStep  int
	initialized   bool

	statusSet    bool
	elapsed      time.Duration
	inputTokens  int
	outputTokens int
	costUSD      float64 // running cost estimate
	statusFrames []string
	statusWidth  int
	statusText   string
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
	a.frames, a.birthDots = renderFrames(size, gradFromColor, gradToColor, bg)
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
	if a.statusSet {
		// Re-render the status suffix with the new label embedded.
		text := formatStatusSuffix(a.elapsed, a.inputTokens, a.outputTokens, a.costUSD, a.label)
		a.statusText = text
		a.statusWidth = lipgloss.Width(text)
		a.statusFrames = renderChars(text, a.labelColor, a.bgColor)
	}
}

func (a *Anim) SetColors(gradFromColor, gradToColor, labelColor color.Color) {
	a.SetColorsWithBackground(gradFromColor, gradToColor, labelColor, a.bgColor)
}

// SetStatus enables a richer status suffix: "(thought for 5s · ↑ 1.2k · $0.0042 · Label)".
// Pass elapsed=0 and inputTokens=outputTokens=0 with set=false (via ClearStatus)
// to revert to the bare label + ellipsis rendering.
func (a *Anim) SetStatus(elapsed time.Duration, inputTokens, outputTokens int, costUSD float64) {
	text := formatStatusSuffix(elapsed, inputTokens, outputTokens, costUSD, a.label)
	if a.statusSet && a.statusText == text {
		return
	}
	a.statusSet = true
	a.elapsed = elapsed
	a.inputTokens = inputTokens
	a.outputTokens = outputTokens
	a.costUSD = costUSD
	a.statusText = text
	a.statusWidth = lipgloss.Width(text)
	a.statusFrames = renderChars(text, a.labelColor, a.bgColor)
}

// ClearStatus reverts to the bare label + ellipsis rendering.
func (a *Anim) ClearStatus() {
	if !a.statusSet {
		return
	}
	a.statusSet = false
	a.elapsed = 0
	a.inputTokens = 0
	a.outputTokens = 0
	a.costUSD = 0
	a.statusText = ""
	a.statusWidth = 0
	a.statusFrames = nil
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
	a.frames, a.birthDots = renderFrames(a.size, gradFromColor, gradToColor, bgColor)
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
			if i < len(a.birthDots) {
				b.WriteString(a.birthDots[i])
			}
			continue
		}
		b.WriteString(frame[i])
	}
	if a.statusSet && len(a.statusFrames) > 0 {
		// Status format ("(thought for 2s · ↑ 1.2k · Thinking)") embeds the
		// label, so we replace the bare label + ellipsis with it.
		if a.bgColor != nil {
			b.WriteString(lipgloss.NewStyle().Background(a.bgColor).Render(labelGap))
		} else {
			b.WriteString(labelGap)
		}
		for _, ch := range a.statusFrames {
			b.WriteString(ch)
		}
		return b.String()
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
	if a.statusSet && a.statusWidth > 0 {
		return width + lipgloss.Width(labelGap) + a.statusWidth
	}
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

func renderFrames(size int, gradFromColor, gradToColor, bgColor color.Color) (frames [][]string, birthDots []string) {
	if size < 1 {
		size = defaultSize
	}
	// Build a seamless A→B→A ramp at 3× width so the scrolling wave loops
	// without a hard color jump. HCL blending keeps colors vibrant and in-gamut.
	ramp := hclBlend1D(size*3, gradFromColor, gradToColor, gradFromColor)
	if len(ramp) == 0 {
		ramp = []color.Color{gradFromColor}
	}

	// Pre-render the static '.' shown during the birth-phase boot sequence.
	// Each position gets the gradient color at that index so dots already
	// paint the full gradient during startup.
	birthDots = make([]string, size)
	for j := range size {
		style := lipgloss.NewStyle().Foreground(ramp[j%len(ramp)])
		if bgColor != nil {
			style = style.Background(bgColor)
		}
		birthDots[j] = style.Render(".")
	}

	numFrames := size * 2
	frames = make([][]string, numFrames)
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
	return frames, birthDots
}

// hclBlend1D produces a slice of n colors by blending through the provided
// stops in HCL space. HCL interpolation keeps hue, chroma, and lightness
// perceptually uniform so the gradient stays vibrant rather than washing out
// through grey (as linear RGB blending can). Requires ≥2 stops.
func hclBlend1D(n int, stops ...color.Color) []color.Color {
	if n <= 0 || len(stops) < 2 {
		return nil
	}
	pts := make([]colorful.Color, len(stops))
	for i, s := range stops {
		pts[i], _ = colorful.MakeColor(s)
	}
	segments := len(stops) - 1
	sizes := make([]int, segments)
	base := n / segments
	rem := n % segments
	for i := range sizes {
		sizes[i] = base
		if i < rem {
			sizes[i]++
		}
	}
	out := make([]color.Color, 0, n)
	for seg := range segments {
		c1, c2 := pts[seg], pts[seg+1]
		for k := range sizes[seg] {
			t := float64(k) / float64(max(sizes[seg], 1))
			out = append(out, c1.BlendHcl(c2, t).Clamped())
		}
	}
	return out
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

// formatStatusSuffix produces the status text shown alongside the spinner —
// matches Claude Code's SpinnerAnimationRow format:
//
//	(thought for 2s · ↑ 1.2k · Thinking)
//
// The arrow direction reflects which side dominates: ↓ when output exceeds
// input (model is generating), ↑ otherwise (model is consuming context).
// When elapsed and tokens are both zero/empty, the parens collapse around
// just the label.
func formatStatusSuffix(elapsed time.Duration, inputTokens, outputTokens int, costUSD float64, label string) string {
	parts := make([]string, 0, 4)
	if elapsed > 0 {
		parts = append(parts, "thought for "+formatElapsed(elapsed))
	}
	if inputTokens > 0 || outputTokens > 0 {
		direction := "↑"
		if outputTokens > inputTokens {
			direction = "↓"
		}
		diff := outputTokens - inputTokens
		if diff < 0 {
			diff = -diff
		}
		parts = append(parts, direction+" "+formatTokenCount(diff))
	}
	if costUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.4f", costUSD))
	}
	if label != "" {
		parts = append(parts, label)
	}
	if len(parts) == 0 {
		return ""
	}
	return "(" + strings.Join(parts, " · ") + ")"
}

// formatElapsed renders a run duration as a compact human-readable string,
// e.g. "3s", "1m 12s", "1h 4m". Sub-second durations clamp to "0s".
func formatElapsed(d time.Duration) string {
	if d < time.Second {
		return "0s"
	}
	totalSec := int(d / time.Second)
	if totalSec < 60 {
		return fmt.Sprintf("%ds", totalSec)
	}
	if totalSec < 3600 {
		m := totalSec / 60
		s := totalSec % 60
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := totalSec / 3600
	m := (totalSec % 3600) / 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}

// formatTokenCount renders a token count compactly: 982 → "982",
// 1234 → "1.2k", 12345 → "12.3k", 1234567 → "1.2M".
func formatTokenCount(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 1_000_000 {
		v := float64(n) / 1000
		// Trim to one decimal, but drop ".0" for tidy output.
		if math.Abs(v-math.Round(v)) < 0.05 {
			return fmt.Sprintf("%.0fk", math.Round(v))
		}
		return fmt.Sprintf("%.1fk", v)
	}
	v := float64(n) / 1_000_000
	if math.Abs(v-math.Round(v)) < 0.05 {
		return fmt.Sprintf("%.0fM", math.Round(v))
	}
	return fmt.Sprintf("%.1fM", v)
}
