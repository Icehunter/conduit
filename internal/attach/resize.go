package attach

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff" // register TIFF decoder for macOS clipboard images
)

const (
	// API limits from src/constants/apiLimits.ts
	apiImageMaxBase64Bytes = 5 * 1024 * 1024 // 5 MB base64
	apiImageMaxWidth       = 2000
	apiImageMaxHeight      = 2000
	// Target raw byte size before base64 encoding (≈3.75 MB).
	apiImageTargetRawBytes = apiImageMaxBase64Bytes * 3 / 4
)

// MaybeResize decodes the base64 image in img and checks whether its
// dimensions or byte-size exceed the Anthropic API limits. If they do, the
// image is resized (and re-encoded as JPEG for smaller output) and img is
// updated in-place. If the image is within limits, it is returned unchanged.
//
// Mirrors src/utils/imageResizer.ts:maybeResizeAndDownsampleImageBuffer.
func MaybeResize(img *Image) error {
	raw, err := base64.StdEncoding.DecodeString(img.Data)
	if err != nil {
		return err
	}

	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		// Unknown format — leave untouched; API will reject if over limit.
		return nil
	}

	bounds := src.Bounds()
	w := bounds.Dx()
	h := bounds.Dy()

	needResize := w > apiImageMaxWidth || h > apiImageMaxHeight || len(raw) > apiImageTargetRawBytes
	if !needResize {
		return nil
	}

	// Compute new dimensions maintaining aspect ratio.
	newW, newH := scaledDims(w, h, apiImageMaxWidth, apiImageMaxHeight)

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)

	// Re-encode as JPEG (smaller than PNG for photos/screenshots).
	var buf bytes.Buffer
	if encErr := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); encErr != nil {
		// Fall back to PNG.
		buf.Reset()
		if encErr2 := png.Encode(&buf, dst); encErr2 != nil {
			return encErr2
		}
		img.MediaType = "image/png"
	} else {
		img.MediaType = "image/jpeg"
	}

	img.Data = base64.StdEncoding.EncodeToString(buf.Bytes())
	return nil
}

func scaledDims(w, h, maxW, maxH int) (int, int) {
	if w <= maxW && h <= maxH {
		return w, h
	}
	wRatio := float64(maxW) / float64(w)
	hRatio := float64(maxH) / float64(h)
	ratio := wRatio
	if hRatio < ratio {
		ratio = hRatio
	}
	newW := int(float64(w) * ratio)
	newH := int(float64(h) * ratio)
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}
	return newW, newH
}
