package commands

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/icehunter/conduit/internal/settings"
)

// RegisterCustomCommands loads .md commands from user-global and project-local
// commands directories and registers them in r.
//
// Loading order (later registrations win on name collision):
//  1. <claudeDir>/commands/  (user-global; respects $CLAUDE_CONFIG_DIR)
//  2. <cwd>/.claude/commands/ (project-local)
//
// Directories that do not exist are silently skipped.
func RegisterCustomCommands(r *Registry, cwd string) {
	loadCommandsFromDir(r, filepath.Join(settings.ClaudeDir(), "commands"))
	if cwd != "" {
		loadCommandsFromDir(r, filepath.Join(cwd, ".claude", "commands"))
	}
}

func loadCommandsFromDir(r *Registry, commandsDir string) {
	info, err := os.Stat(commandsDir)
	if err != nil || !info.IsDir() {
		return
	}
	_ = filepath.WalkDir(commandsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(commandsDir, path)
			// Allow only one level of subdirectory nesting.
			if rel != "." && strings.Count(filepath.ToSlash(rel), "/") >= 1 {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			return nil
		}
		rel, err := filepath.Rel(commandsDir, path)
		if err != nil {
			return nil
		}
		cmdName := commandNameFromPath(rel)
		if cmdName == "" {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // user config files; TOCTOU not a concern here
		if err != nil {
			return nil
		}
		desc, body := parseFrontmatter(string(data))
		if desc == "" {
			desc = "/" + cmdName
		}
		captured := body
		r.Register(Command{
			Name:        cmdName,
			Description: desc,
			Handler: func(args string) Result {
				text := strings.ReplaceAll(captured, "$ARGUMENTS", args)
				return Result{Type: "prompt", Text: strings.TrimSpace(text)}
			},
		})
		return nil
	})
}

// commandNameFromPath derives a slash command name from a path relative to the
// commands directory. Matches CC's NU7 naming logic.
//
//	foo.md           → "foo"
//	dir/name.md      → "dir:name"
//	dir/SKILL.md     → "dir"  (case-insensitive SKILL.md)
func commandNameFromPath(relPath string) string {
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	if !strings.HasSuffix(strings.ToLower(last), ".md") {
		return ""
	}
	if strings.EqualFold(last, "skill.md") {
		// SKILL.md: name is directory components only.
		if len(parts) < 2 {
			return ""
		}
		return strings.ToLower(strings.Join(parts[:len(parts)-1], ":"))
	}
	name := strings.Join(parts, ":")
	name = name[:len(name)-len(".md")]
	return strings.ToLower(name)
}

// parseFrontmatter splits optional YAML front matter (between --- delimiters)
// from the body. Only the description: key is extracted; all other frontmatter
// keys are ignored. No external YAML parser is required for this simple format.
func parseFrontmatter(content string) (description, body string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", content
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
		if after, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), "description:"); ok {
			description = strings.TrimSpace(after)
		}
	}
	if end < 0 {
		// Unclosed frontmatter — treat whole file as body.
		return "", content
	}
	return description, strings.Join(lines[end+1:], "\n")
}
