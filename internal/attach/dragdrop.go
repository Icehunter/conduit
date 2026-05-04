package attach

import (
	"net/url"
	"os"
	"strings"
)

// ConvertDroppedFiles detects terminal file-drop paste content and converts
// it to @mention syntax suitable for the attach pipeline.
//
// Terminals emit dragged files as space-separated file:// URIs (iTerm2,
// Ghostty, macOS Terminal) or bare absolute paths. We detect either form,
// verify each path exists, and return "@path" tokens joined by spaces.
//
// Returns (converted, true) when at least one file path was found.
// Returns ("", false) when the content doesn't look like dropped files.
func ConvertDroppedFiles(content string) (string, bool) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", false
	}

	// Split on whitespace — file:// URIs from Finder are space-separated.
	parts := strings.Fields(content)
	var paths []string
	for _, part := range parts {
		// Decode file:// URI.
		if strings.HasPrefix(part, "file://") {
			u, err := url.PathUnescape(strings.TrimPrefix(part, "file://"))
			if err == nil && u != "" {
				part = u
			}
		}
		// Only accept absolute paths that actually exist.
		if strings.HasPrefix(part, "/") {
			if _, err := os.Stat(part); err == nil {
				paths = append(paths, part)
				continue
			}
		}
		// If any part is neither a file:// URI nor a valid path, bail out —
		// this is normal text, not a file drop.
		if !strings.HasPrefix(part, "file://") {
			return "", false
		}
	}
	if len(paths) == 0 {
		return "", false
	}

	// Build @mention tokens.
	var mentions []string
	for _, p := range paths {
		if strings.ContainsAny(p, " \t") {
			mentions = append(mentions, `@"`+p+`"`)
		} else {
			mentions = append(mentions, "@"+p)
		}
	}
	return strings.Join(mentions, " "), true
}
