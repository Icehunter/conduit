// Package attach — ANSI to PNG conversion.
// Renders terminal text (with or without ANSI escape sequences) to a PNG image.
// Mirrors the intent of src/utils/ansiToPng.ts.
//
// Implementation uses Go's standard image library with a monospace pixel font.
// ANSI SGR codes are parsed to apply foreground/background colors (8-color,
// 256-color, and truecolor). Unsupported sequences are ignored.
package attach

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

var ansiEscapeRe = regexp.MustCompile(`\x1b\[[0-9;]*m|\x1b\[[0-9;]*[A-Za-z]|\x1b\][^\x07]*\x07`)

// ANSIToPNG renders the given terminal text (may contain ANSI escapes) to a
// PNG image and returns it as a base64-encoded string. The image uses a dark
// terminal theme (black background, light text). Returns "" on failure.
func ANSIToPNG(text string) string {
	data := renderANSI(text)
	if data == nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(data)
}

// StripANSI removes all ANSI escape sequences from s.
func StripANSI(s string) string {
	return ansiEscapeRe.ReplaceAllString(s, "")
}

const (
	charW   = 7  // basicfont.Face7x13 character width
	charH   = 13 // basicfont.Face7x13 character height
	padX    = 8  // horizontal padding
	padY    = 8  // vertical padding
	maxCols = 220
	maxRows = 500
)

// dark terminal palette — matches most dark themes
var (
	bgColor = color.RGBA{R: 30, G: 30, B: 30, A: 255}
	fgColor = color.RGBA{R: 204, G: 204, B: 204, A: 255}
	ansi16  = [16]color.RGBA{
		{R: 0, G: 0, B: 0, A: 255},       // 0 black
		{R: 187, G: 0, B: 0, A: 255},     // 1 red
		{R: 0, G: 187, B: 0, A: 255},     // 2 green
		{R: 187, G: 187, B: 0, A: 255},   // 3 yellow
		{R: 0, G: 0, B: 187, A: 255},     // 4 blue
		{R: 187, G: 0, B: 187, A: 255},   // 5 magenta
		{R: 0, G: 187, B: 187, A: 255},   // 6 cyan
		{R: 187, G: 187, B: 187, A: 255}, // 7 white
		{R: 85, G: 85, B: 85, A: 255},    // 8 bright black
		{R: 255, G: 85, B: 85, A: 255},   // 9 bright red
		{R: 85, G: 255, B: 85, A: 255},   // 10 bright green
		{R: 255, G: 255, B: 85, A: 255},  // 11 bright yellow
		{R: 85, G: 85, B: 255, A: 255},   // 12 bright blue
		{R: 255, G: 85, B: 255, A: 255},  // 13 bright magenta
		{R: 85, G: 255, B: 255, A: 255},  // 14 bright cyan
		{R: 255, G: 255, B: 255, A: 255}, // 15 bright white
	}
)

type span struct {
	text string
	fg   color.RGBA
	bg   color.RGBA
}

type styledLine []span

func renderANSI(text string) []byte {
	// Parse into styled lines.
	lines := parseANSI(text)
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > maxRows {
		lines = lines[:maxRows]
	}

	// Compute image dimensions.
	cols := 0
	for _, line := range lines {
		w := 0
		for _, sp := range line {
			w += len([]rune(sp.text))
		}
		if w > cols {
			cols = w
		}
	}
	if cols > maxCols {
		cols = maxCols
	}
	if cols == 0 {
		cols = 80
	}

	imgW := cols*charW + padX*2
	imgH := len(lines)*charH + padY*2
	img := image.NewRGBA(image.Rect(0, 0, imgW, imgH))

	// Fill background.
	for y := 0; y < imgH; y++ {
		for x := 0; x < imgW; x++ {
			img.Set(x, y, bgColor)
		}
	}

	// Draw text.
	face := basicfont.Face7x13
	for row, line := range lines {
		x := padX
		y := padY + row*charH
		for _, sp := range line {
			for _, r := range sp.text {
				// Background cell.
				if sp.bg != bgColor {
					for dy := 0; dy < charH; dy++ {
						for dx := 0; dx < charW; dx++ {
							img.Set(x+dx, y+dy, sp.bg)
						}
					}
				}
				// Glyph.
				d := font.Drawer{
					Dst:  img,
					Src:  image.NewUniform(sp.fg),
					Face: face,
					Dot:  fixed.P(x, y+charH-3),
				}
				d.DrawString(string(r))
				x += charW
				if x > imgW-padX {
					break
				}
			}
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// parseANSI turns escape-annotated text into a slice of styled lines.
func parseANSI(text string) []styledLine {
	// Split on real newlines.
	rawLines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	// Remove trailing empty line from trailing \n.
	if len(rawLines) > 0 && rawLines[len(rawLines)-1] == "" {
		rawLines = rawLines[:len(rawLines)-1]
	}

	result := make([]styledLine, 0, len(rawLines))
	curFG := fgColor
	curBG := bgColor

	// Regex to split on escape sequences.
	splitter := regexp.MustCompile(`(\x1b\[[0-9;]*m)`)

	for _, raw := range rawLines {
		parts := splitter.Split(raw, -1)
		seqs := splitter.FindAllString(raw, -1)
		var line styledLine
		seqIdx := 0
		for i, part := range parts {
			if part != "" {
				line = append(line, span{text: part, fg: curFG, bg: curBG})
			}
			if i < len(seqs) {
				curFG, curBG = applySGR(seqs[seqIdx], curFG, curBG)
				seqIdx++
			}
		}
		result = append(result, line)
	}
	return result
}

// applySGR interprets one \x1b[...m sequence and returns updated fg/bg colors.
func applySGR(seq string, fg, bg color.RGBA) (color.RGBA, color.RGBA) {
	inner := strings.TrimPrefix(strings.TrimSuffix(seq, "m"), "\x1b[")
	if inner == "" || inner == "0" {
		return fgColor, bgColor
	}
	codes := strings.Split(inner, ";")
	i := 0
	for i < len(codes) {
		n, _ := strconv.Atoi(codes[i])
		switch {
		case n == 0:
			fg, bg = fgColor, bgColor
		case n == 1: // bold — ignored for simplicity
		case n >= 30 && n <= 37:
			fg = ansi16[n-30]
		case n == 38:
			fg, i = parseSGRColor(codes, i)
		case n == 39:
			fg = fgColor
		case n >= 40 && n <= 47:
			bg = ansi16[n-40]
		case n == 48:
			bg, i = parseSGRColor(codes, i)
		case n == 49:
			bg = bgColor
		case n >= 90 && n <= 97:
			fg = ansi16[n-90+8]
		case n >= 100 && n <= 107:
			bg = ansi16[n-100+8]
		}
		i++
	}
	return fg, bg
}

func parseSGRColor(codes []string, i int) (color.RGBA, int) {
	if i+1 >= len(codes) {
		return fgColor, i
	}
	switch codes[i+1] {
	case "5": // 256-color
		if i+2 < len(codes) {
			idx, _ := strconv.Atoi(codes[i+2])
			return color256(idx), i + 2
		}
	case "2": // truecolor
		if i+4 < len(codes) {
			r, _ := strconv.Atoi(codes[i+2])
			g, _ := strconv.Atoi(codes[i+3])
			b, _ := strconv.Atoi(codes[i+4])
			return color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}, i + 4
		}
	}
	return fgColor, i
}

// color256 maps a 256-color index to RGBA.
func color256(n int) color.RGBA {
	if n < 16 {
		return ansi16[n]
	}
	if n >= 232 { // grayscale ramp
		v := uint8(8 + (n-232)*10)
		return color.RGBA{R: v, G: v, B: v, A: 255}
	}
	// 6×6×6 color cube.
	n -= 16
	b := n % 6
	g := (n / 6) % 6
	r := (n / 36) % 6
	c := func(x int) uint8 {
		if x == 0 {
			return 0
		}
		return uint8(55 + x*40)
	}
	return color.RGBA{R: c(r), G: c(g), B: c(b), A: 255}
}
