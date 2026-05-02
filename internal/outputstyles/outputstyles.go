// Package outputstyles loads user/project output style definitions from
// markdown files in .claude/output-styles/ directories.
//
// Mirrors src/outputStyles/loadOutputStylesDir.ts.
//
// Style files live at:
//   - ~/.claude/output-styles/*.md         (user-level)
//   - <cwd>/.claude/output-styles/*.md     (project-level, overrides user)
//
// Each .md file becomes one OutputStyle. Frontmatter fields:
//
//	name: Human-readable name (defaults to filename stem)
//	description: One-line description
//	keep-coding-instructions: true/false
package outputstyles

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Style is one loaded output style definition.
type Style struct {
	Name                   string
	Description            string
	Prompt                 string
	Source                 string // "user" or "project"
	KeepCodingInstructions bool
}

// LoadAll loads output styles from both the user home directory and the
// project directory, with project styles taking precedence over user styles
// of the same name.
func LoadAll(cwd string) ([]Style, error) {
	home, _ := os.UserHomeDir()
	userDir := filepath.Join(home, ".claude", "output-styles")
	projDir := filepath.Join(cwd, ".claude", "output-styles")

	userStyles, _ := loadDir(userDir)
	projStyles, _ := loadDir(projDir)

	// Merge: project overrides user by name.
	byName := make(map[string]Style, len(userStyles)+len(projStyles))
	for _, s := range userStyles {
		byName[s.Name] = s
	}
	for _, s := range projStyles {
		byName[s.Name] = s
	}

	out := make([]Style, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	return out, nil
}

// loadDir reads all *.md files from dir and returns parsed styles.
func loadDir(dir string) ([]Style, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var styles []Style
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		stem := strings.TrimSuffix(e.Name(), ".md")
		s := parse(stem, string(data))
		styles = append(styles, s)
	}
	return styles, nil
}

// parse extracts frontmatter and body from a markdown file.
func parse(stem, content string) Style {
	s := Style{Name: stem}

	fm, body := splitFrontmatter(content)
	s.Prompt = strings.TrimSpace(body)

	if name, ok := fm["name"]; ok && name != "" {
		s.Name = name
	}
	if desc, ok := fm["description"]; ok {
		s.Description = desc
	}
	switch strings.ToLower(fm["keep-coding-instructions"]) {
	case "true", "1", "yes":
		s.KeepCodingInstructions = true
	}
	return s
}

// splitFrontmatter separates YAML frontmatter (--- delimited) from body.
// Returns a flat key→value map (single-line string values only) and the body.
func splitFrontmatter(content string) (map[string]string, string) {
	fm := map[string]string{}
	if !strings.HasPrefix(content, "---") {
		return fm, content
	}
	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Scan() // consume opening ---
	var fmLines []string
	var bodyLines []string
	inFM := true
	for scanner.Scan() {
		line := scanner.Text()
		if inFM {
			if line == "---" {
				inFM = false
				continue
			}
			fmLines = append(fmLines, line)
		} else {
			bodyLines = append(bodyLines, line)
		}
	}
	for _, line := range fmLines {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		// Strip surrounding quotes.
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		fm[key] = val
	}
	return fm, strings.Join(bodyLines, "\n")
}
