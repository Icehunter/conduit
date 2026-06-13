package ttsr

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Load scans <dir>/.conduit/ttsr/*.md for each dir in dirs and returns a
// deduplicated list of compiled rules. Later files override earlier ones
// when two files declare the same rule name (last one wins).
//
// Returns nil, nil when no TTSR directories or files exist — not an error.
// A file with a bad regex pattern is skipped with a warning written to
// stderr; the rest of the files continue to load.
func Load(dirs ...string) ([]Rule, error) {
	byName := make(map[string]Rule)
	var order []string // track insertion order for stable output

	for _, dir := range dirs {
		ttsrDir := filepath.Join(dir, ".conduit", "ttsr")
		entries, err := os.ReadDir(ttsrDir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("ttsr: load: read dir %s: %w", ttsrDir, err)
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !strings.HasSuffix(name, ".md") {
				continue
			}

			filePath := filepath.Join(ttsrDir, name)
			data, err := os.ReadFile(filePath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "conduit: ttsr: skipping %s: %v\n", filePath, err)
				continue
			}

			fields, body := parseFrontmatter(string(data))
			if fields == nil {
				fmt.Fprintf(os.Stderr, "conduit: ttsr: skipping %s: missing frontmatter\n", filePath)
				continue
			}

			ruleName := fields["name"]
			if ruleName == "" {
				fmt.Fprintf(os.Stderr, "conduit: ttsr: skipping %s: missing 'name' field\n", filePath)
				continue
			}

			patternStr := fields["pattern"]
			if patternStr == "" {
				fmt.Fprintf(os.Stderr, "conduit: ttsr: skipping %s: missing 'pattern' field\n", filePath)
				continue
			}

			compiled, err := regexp.Compile(patternStr)
			if err != nil {
				fmt.Fprintf(os.Stderr, "conduit: ttsr: skipping %s: invalid pattern %q: %v\n", filePath, patternStr, err)
				continue
			}

			correction := fields["correction"]
			if correction == "" {
				correction = body
			}

			maxFires := 0
			if mf := fields["max_fires"]; mf != "" {
				if v, err := strconv.Atoi(mf); err == nil && v >= 0 {
					maxFires = v
				}
			}

			rule := Rule{
				Name:       ruleName,
				Pattern:    compiled,
				Correction: correction,
				MaxFires:   maxFires,
			}

			if _, exists := byName[ruleName]; !exists {
				order = append(order, ruleName)
			}
			byName[ruleName] = rule
		}
	}

	if len(order) == 0 {
		return nil, nil
	}

	rules := make([]Rule, 0, len(order))
	for _, name := range order {
		rules = append(rules, byName[name])
	}
	return rules, nil
}

// parseFrontmatter extracts key-value pairs from YAML-like frontmatter between
// --- markers. Lines after the closing --- are the correction body (used when
// the correction key is absent from the frontmatter).
//
// Returns nil fields if no valid frontmatter was found.
func parseFrontmatter(content string) (map[string]string, string) {
	lines := strings.Split(content, "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return nil, content
	}
	fields := map[string]string{}
	end := -1
	for i := 1; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		if l == "---" {
			end = i
			break
		}
		k, v, ok := strings.Cut(l, ":")
		if ok {
			fields[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	if end < 0 {
		return nil, content
	}
	body := strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	return fields, body
}
