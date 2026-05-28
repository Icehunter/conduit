package skills

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/tools/skilltool"
)

// FSSkill is a skill discovered on disk via the agentskills.io convention.
type FSSkill struct {
	Name        string
	Description string
	Body        string
	Tags        []string
	Path        string // absolute path to SKILL.md
}

// DiscoverFS walks the standard skill directories and returns all found skills.
// Discovery order (later entries override earlier ones with the same name):
//  1. ~/.conduit/skills/<name>/SKILL.md
//  2. ~/.claude/skills/<name>/SKILL.md
//  3. <cwd>/.claude/skills/<name>/SKILL.md
func DiscoverFS(cwd string) []FSSkill {
	dirs := []string{
		filepath.Join(settings.ConduitDir(), "skills"),
		filepath.Join(settings.ClaudeDir(), "skills"),
	}
	if cwd != "" {
		dirs = append(dirs, filepath.Join(cwd, ".claude", "skills"))
	}

	// Use a map keyed by skill name so later dirs override earlier ones.
	byName := make(map[string]FSSkill)
	// Track insertion order so the final slice is deterministic.
	var order []string

	for _, base := range dirs {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			skillFile := filepath.Join(base, e.Name(), "SKILL.md")
			content, err := os.ReadFile(skillFile) //nolint:gosec // skill files are user-owned
			if err != nil {
				continue
			}
			sk := parseFSSkill(e.Name(), string(content), skillFile)
			// Append references/ markdown files if present.
			sk.Body = appendReferences(filepath.Join(base, e.Name()), sk.Body)

			if _, exists := byName[sk.Name]; !exists {
				order = append(order, sk.Name)
			}
			byName[sk.Name] = sk
		}
	}

	out := make([]FSSkill, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out
}

// LoadFS returns all discovered FS skills as skilltool.Command entries,
// ready to merge into a SkillLoader.
func LoadFS(cwd string) []skilltool.Command {
	skills := DiscoverFS(cwd)
	cmds := make([]skilltool.Command, 0, len(skills))
	for _, sk := range skills {
		cmds = append(cmds, skilltool.Command{
			QualifiedName: sk.Name,
			Description:   sk.Description,
			Body:          sk.Body,
		})
	}
	return cmds
}

// parseFSSkill parses a SKILL.md file into an FSSkill. dirName is used as the
// fallback skill name when frontmatter does not supply one.
func parseFSSkill(dirName, content, path string) FSSkill {
	sk := FSSkill{
		Name: dirName,
		Path: path,
	}

	fm, body, hasFM := parseFSFrontmatter(content)
	if hasFM {
		if name := fm["name"]; name != "" {
			sk.Name = name
		}
		sk.Description = fm["description"]
		sk.Tags = parseFSTags(fm["tags"])
	} else {
		body = content
	}

	// Derive description from first non-blank body line if still empty.
	if sk.Description == "" {
		sk.Description = firstNonBlankLine(body)
	}

	sk.Body = body
	return sk
}

// parseFSFrontmatter splits content on --- delimiters and returns a flat
// key→value map of top-level and nested conduit-scoped fields. The parser
// handles `key: value`, `key: [v1, v2]`, and one level of YAML nesting
// (e.g. `metadata:\n  conduit:\n    tags: [...]`). No YAML library is used.
func parseFSFrontmatter(content string) (map[string]string, string, bool) {
	if !strings.HasPrefix(content, "---") {
		return nil, content, false
	}
	// Find closing --- (search from after the opening "---").
	rest := content[3:]
	end := strings.Index(rest, "---")
	if end < 0 {
		return nil, content, false
	}
	fmRaw := rest[:end]
	body := strings.TrimLeft(rest[end+3:], "\n")

	fm := make(map[string]string)
	var pendingKey string // tracks indented nesting context ("metadata", "conduit")

	for _, line := range strings.Split(fmRaw, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}

		// Determine indentation level: 0 = top-level, >0 = nested.
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		trimmed := strings.TrimSpace(line)

		if indent == 0 {
			pendingKey = ""
		}

		colonIdx := strings.Index(trimmed, ":")
		if colonIdx < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:colonIdx])
		val := strings.TrimSpace(trimmed[colonIdx+1:])
		// Strip surrounding quotes.
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}

		// Track nested sections without values: `metadata:` or `conduit:`.
		if val == "" {
			if indent == 0 {
				pendingKey = key
			}
			// Don't store empty-value section headers.
			continue
		}

		// For conduit-scoped nested keys, hoist them to the flat map.
		// e.g. `tags: [t1, t2]` under `conduit:` → fm["tags"] = "[t1, t2]"
		if pendingKey == "conduit" || indent > 0 {
			fm[key] = val
		} else {
			fm[key] = val
		}
	}
	return fm, body, true
}

// parseFSTags parses a YAML inline list `[tag1, tag2]` or a bare `tag1` into
// a string slice. Returns nil for empty input.
func parseFSTags(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Inline list: [tag1, tag2]
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		inner := raw[1 : len(raw)-1]
		parts := strings.Split(inner, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			p = strings.Trim(p, `"'`)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	// Single tag.
	return []string{strings.Trim(raw, `"'`)}
}

// appendReferences reads <skillDir>/references/*.md files and appends them to
// body under a "## References" header. Files are sorted by directory order.
func appendReferences(skillDir, body string) string {
	refDir := filepath.Join(skillDir, "references")
	entries, err := os.ReadDir(refDir)
	if err != nil {
		return body
	}
	var refs []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(refDir, e.Name())) //nolint:gosec // user-owned skill references
		if err != nil {
			continue
		}
		refs = append(refs, strings.TrimSpace(string(data)))
	}
	if len(refs) == 0 {
		return body
	}
	return strings.TrimRight(body, "\n") + "\n\n## References\n\n" + strings.Join(refs, "\n\n")
}

// firstNonBlankLine returns the first non-empty, non-header line from s, used
// as a fallback description when frontmatter is absent.
func firstNonBlankLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		// Strip markdown heading markers.
		line = strings.TrimLeft(line, "#")
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}
