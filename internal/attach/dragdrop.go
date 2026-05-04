package attach

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// DropType classifies a dropped file.
type DropType int

const (
	DropOther DropType = iota
	DropImage
	DropPDF
)

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true,
	".gif": true, ".webp": true, ".tiff": true, ".tif": true,
}

// DroppedFileType classifies a path by extension.
func DroppedFileType(p string) DropType {
	ext := strings.ToLower(filepath.Ext(p))
	if ext == ".pdf" {
		return DropPDF
	}
	if imageExts[ext] {
		return DropImage
	}
	return DropOther
}

// DetectDroppedPaths is the multi-file version of ConvertDroppedFiles — it
// returns the list of resolved absolute paths instead of @mentions.
// Returns (nil, false) when the content doesn't look like dropped files.
func DetectDroppedPaths(content string) ([]string, bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, false
	}
	if strings.Contains(content, "file://") {
		var paths []string
		for _, part := range strings.Fields(content) {
			if !strings.HasPrefix(part, "file://") {
				continue
			}
			u, err := url.PathUnescape(strings.TrimPrefix(part, "file://"))
			if err != nil || u == "" {
				continue
			}
			if _, err := os.Stat(u); err == nil {
				paths = append(paths, u)
			}
		}
		if len(paths) > 0 {
			return paths, true
		}
		return nil, false
	}
	if strings.HasPrefix(content, "/") {
		unescaped := strings.ReplaceAll(content, `\ `, " ")
		if _, err := os.Stat(unescaped); err == nil {
			return []string{unescaped}, true
		}
		var paths []string
		for _, p := range strings.Fields(unescaped) {
			if strings.HasPrefix(p, "/") {
				if _, err := os.Stat(p); err == nil {
					paths = append(paths, p)
				}
			}
		}
		if len(paths) > 0 {
			return paths, true
		}
	}
	return nil, false
}

// MentionPath builds an @mention string for an arbitrary dropped file path.
func MentionPath(p string) string {
	if strings.ContainsAny(p, " \t") {
		return `@"` + p + `"`
	}
	return "@" + p
}

// ReadImageFile reads an image from disk and returns it as an attach.Image.
func ReadImageFile(p string) (*Image, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty file")
	}
	img := &Image{
		Data:      base64.StdEncoding.EncodeToString(data),
		MediaType: "image/png",
	}
	ext := strings.ToLower(filepath.Ext(p))
	switch ext {
	case ".jpg", ".jpeg":
		img.MediaType = "image/jpeg"
	case ".gif":
		img.MediaType = "image/gif"
	case ".webp":
		img.MediaType = "image/webp"
	}
	_ = MaybeResize(img)
	return img, nil
}

// ReadPDFFile reads a PDF from disk and returns it as an attach.PDF.
func ReadPDFFile(p string) (*PDF, error) {
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty file")
	}
	return &PDF{
		Data:      base64.StdEncoding.EncodeToString(data),
		MediaType: "application/pdf",
	}, nil
}
