package tui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	gradientBlue   = "#5E6BFF"
	gradientPurple = "#7C5CFF"
	gradientPink   = "#F05BFF"
)

type rgbColor struct {
	r int
	g int
	b int
}

func brandGradientText(s string) string {
	return gradientText(s, gradientPink, gradientPurple)
}

func ornamentGradientText(s string) string {
	return gradientText(s, gradientBlue, gradientPink)
}

func gradientText(s, from, to string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return ""
	}
	a, okA := parseHexRGB(from)
	b, okB := parseHexRGB(to)
	if !okA || !okB {
		a = rgbColor{r: 94, g: 107, b: 255}
		b = rgbColor{r: 240, g: 91, b: 255}
	}
	steps := 0
	for _, r := range runes {
		if r != ' ' {
			steps++
		}
	}
	if steps <= 1 {
		return gradientStyle(from).Render(s)
	}
	var out strings.Builder
	seen := 0
	for _, r := range runes {
		if r == ' ' {
			out.WriteString(surfaceSpaces(1))
			continue
		}
		t := float64(seen) / float64(steps-1)
		seen++
		out.WriteString(gradientStyle(blendHex(a, b, t)).Render(string(r)))
	}
	return out.String()
}

func gradientStyle(fg string) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(fg)).
		Background(colorWindowBg)
}

func parseHexRGB(s string) (rgbColor, bool) {
	if len(s) != 7 || s[0] != '#' {
		return rgbColor{}, false
	}
	r, errR := strconv.ParseInt(s[1:3], 16, 0)
	g, errG := strconv.ParseInt(s[3:5], 16, 0)
	b, errB := strconv.ParseInt(s[5:7], 16, 0)
	if errR != nil || errG != nil || errB != nil {
		return rgbColor{}, false
	}
	return rgbColor{r: int(r), g: int(g), b: int(b)}, true
}

func blendHex(a, b rgbColor, t float64) string {
	lerp := func(x, y int) int {
		return x + int(float64(y-x)*t+0.5)
	}
	return fmt.Sprintf("#%02X%02X%02X", lerp(a.r, b.r), lerp(a.g, b.g), lerp(a.b, b.b))
}
