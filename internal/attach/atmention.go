package attach

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// AtMention is a parsed @path token from user input. Supports:
//
//	@file.go             — read entire file
//	@"path with spaces"  — quoted path
//	@file.go#L10-20      — line range
//	@dir/                — list directory entries
var (
	quotedAtRe  = regexp.MustCompile(`(?:^|\s)@"([^"]+)"`)
	regularAtRe = regexp.MustCompile(`(?:^|\s)@([^\s"]+)`)
)

type AtMention struct {
	Original  string // original token as written, e.g. "@src/main.go#L1-5"
	Path      string // path portion without line range
	LineStart int    // 1-based; 0 = not specified
	LineEnd   int    // 1-based inclusive; 0 = not specified
}

// AtResult is the resolved content for one @mention.
type AtResult struct {
	DisplayPath string // path relative to cwd for display in the system block
	Content     string // file or directory listing content
}

// ExtractAtMentions parses @path tokens from user text. Tokens like
// @"agent (agent)" (agent mentions) are excluded.
func ExtractAtMentions(text string) []AtMention {
	seen := map[string]bool{}
	var out []AtMention

	// Quoted paths first.
	for _, m := range quotedAtRe.FindAllStringSubmatch(text, -1) {
		raw := m[1]
		if strings.HasSuffix(raw, " (agent)") {
			continue
		}
		key := "@\"" + raw + "\""
		if seen[key] {
			continue
		}
		seen[key] = true
		path, ls, le := parseLineRange(raw)
		out = append(out, AtMention{Original: key, Path: path, LineStart: ls, LineEnd: le})
	}

	// Regular unquoted paths.
	for _, m := range regularAtRe.FindAllStringSubmatch(text, -1) {
		raw := m[1]
		if strings.HasPrefix(raw, "\"") {
			continue // handled above
		}
		key := "@" + raw
		if seen[key] {
			continue
		}
		seen[key] = true
		path, ls, le := parseLineRange(raw)
		out = append(out, AtMention{Original: key, Path: path, LineStart: ls, LineEnd: le})
	}
	return out
}

// parseLineRange splits "file.go#L10-20" into path, lineStart, lineEnd.
// Returns (path, 0, 0) when no line range is present.
func parseLineRange(raw string) (path string, lineStart, lineEnd int) {
	idx := strings.Index(raw, "#L")
	if idx < 0 {
		// Strip non-line-range fragments like "#heading" too.
		if frag := strings.Index(raw, "#"); frag >= 0 {
			return raw[:frag], 0, 0
		}
		return raw, 0, 0
	}
	path = raw[:idx]
	suffix := raw[idx+2:] // strip "#L"
	parts := strings.SplitN(suffix, "-", 2)
	ls, err := strconv.Atoi(parts[0])
	if err != nil {
		return path, 0, 0
	}
	lineStart = ls
	if len(parts) == 2 {
		le, err := strconv.Atoi(parts[1])
		if err == nil {
			lineEnd = le
		}
	}
	return path, lineStart, lineEnd
}

const (
	atMentionMaxLines = 2000
	atMentionMaxBytes = 1 << 20 // 1 MiB
)

// ProcessAtMentions reads all @-mentioned paths and returns content blocks
// suitable for prepending to the API user message.
// cwd is used to expand relative paths and compute display paths.
func ProcessAtMentions(text, cwd string) []AtResult {
	mentions := ExtractAtMentions(text)
	if len(mentions) == 0 {
		return nil
	}
	var results []AtResult
	for _, m := range mentions {
		p := m.Path
		// Expand ~ prefix.
		if strings.HasPrefix(p, "~/") {
			home, _ := os.UserHomeDir()
			p = filepath.Join(home, p[2:])
		} else if !filepath.IsAbs(p) {
			p = filepath.Join(cwd, p)
		}

		info, err := os.Stat(p)
		if err != nil {
			continue // silently skip missing paths
		}

		var content string
		if info.IsDir() {
			content = readDir(p)
		} else {
			content = readFileLines(p, m.LineStart, m.LineEnd)
		}
		if content == "" {
			continue
		}

		display := p
		if rel, err := filepath.Rel(cwd, p); err == nil {
			display = rel
		}
		// Add line range annotation to display path.
		if m.LineStart > 0 {
			if m.LineEnd > 0 {
				display = fmt.Sprintf("%s (lines %d-%d)", display, m.LineStart, m.LineEnd)
			} else {
				display = fmt.Sprintf("%s (from line %d)", display, m.LineStart)
			}
		}
		results = append(results, AtResult{DisplayPath: display, Content: content})
	}
	return results
}

// FormatAtResult wraps a file attachment in the XML envelope that CC uses.
func FormatAtResult(r AtResult) string {
	return fmt.Sprintf("<file_content path=%q>\n%s\n</file_content>", r.DisplayPath, r.Content)
}

func readFileLines(path string, lineStart, lineEnd int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > atMentionMaxBytes {
		data = data[:atMentionMaxBytes]
	}
	lines := strings.Split(string(data), "\n")

	if lineStart > 0 {
		start := lineStart - 1 // 0-indexed
		if start >= len(lines) {
			return ""
		}
		end := len(lines)
		if lineEnd > 0 && lineEnd <= len(lines) {
			end = lineEnd
		} else if lineEnd > len(lines) {
			end = len(lines)
		}
		lines = lines[start:end]
	}

	if len(lines) > atMentionMaxLines {
		truncated := len(lines) - atMentionMaxLines
		lines = lines[:atMentionMaxLines]
		lines = append(lines, fmt.Sprintf("[... %d more lines not shown]", truncated))
	}
	return strings.Join(lines, "\n")
}

func readDir(path string) string {
	const maxEntries = 1000
	entries, err := os.ReadDir(path)
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for i, e := range entries {
		if i >= maxEntries {
			sb.WriteString(fmt.Sprintf("… and %d more entries\n", len(entries)-maxEntries))
			break
		}
		sb.WriteString(e.Name())
		if e.IsDir() {
			sb.WriteByte('/')
		}
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}
