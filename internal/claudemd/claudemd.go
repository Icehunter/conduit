// Package claudemd loads CLAUDE.md instruction files from the project
// directory hierarchy and injects them into the agent system prompt.
//
// Load order (mirrors utils/claudemd.ts):
//  1. User global:   ~/.claude/CLAUDE.md  and ~/.claude/rules/*.md
//  2. Project:       CLAUDE.md, .claude/CLAUDE.md, .claude/rules/*.md
//                    discovered by walking from cwd up to filesystem root
//                    (closer to cwd = higher priority = loaded later)
//  3. Local private: CLAUDE.local.md (gitignored, per-directory)
//
// Within each directory, files closer to cwd override files from parents.
// @include directives are resolved recursively with circular-reference protection.
package claudemd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// MaxCharCount is the recommended max character count for a single memory file.
// Files larger than this are truncated with a warning. Mirrors TS constant.
const MaxCharCount = 40000

// MemoryType identifies where a file came from.
type MemoryType int

const (
	TypeUser    MemoryType = iota // ~/.claude/CLAUDE.md
	TypeProject                   // CLAUDE.md or .claude/CLAUDE.md in project tree
	TypeLocal                     // CLAUDE.local.md (private, gitignored)
)

// File is one loaded CLAUDE.md file.
type File struct {
	Path    string
	Content string
	Type    MemoryType
}

// MEMORY_INSTRUCTION_PROMPT is the header injected before CLAUDE.md content.
// Mirrors the TS constant of the same name.
const memoryInstructionPrompt = "Codebase and user instructions are shown below. Be sure to adhere to these instructions. IMPORTANT: These instructions OVERRIDE any default behavior and you MUST follow them exactly as written."

// Load reads all applicable CLAUDE.md files for cwd and returns them in
// priority order (global first, local project last = highest priority).
func Load(cwd string) ([]File, error) {
	home, _ := os.UserHomeDir()
	var files []File
	seen := map[string]bool{}

	// 1. User global: ~/.claude/CLAUDE.md and ~/.claude/rules/*.md
	// Honour CLAUDE_CONFIG_DIR if set (matches mcp/config.go and settings/env.go).
	userClaudeDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if userClaudeDir == "" && home != "" {
		userClaudeDir = filepath.Join(home, ".claude")
	}
	if userClaudeDir != "" {
		if f, err := loadFile(filepath.Join(userClaudeDir, "CLAUDE.md"), TypeUser, seen); err == nil && f != nil {
			files = append(files, *f)
		}
		ruleFiles, _ := loadRulesDir(filepath.Join(userClaudeDir, "rules"), TypeUser, seen)
		files = append(files, ruleFiles...)
	}

	// 2. Project files: walk from cwd up to root, collect dirs
	dirs := collectDirs(cwd)
	// dirs[0] = root, dirs[last] = cwd; we want cwd-closest last (highest priority)
	// so iterate root→cwd order
	for _, dir := range dirs {
		// CLAUDE.md
		if f, err := loadFile(filepath.Join(dir, "CLAUDE.md"), TypeProject, seen); err == nil && f != nil {
			files = append(files, *f)
		}
		// .claude/CLAUDE.md
		if f, err := loadFile(filepath.Join(dir, ".claude", "CLAUDE.md"), TypeProject, seen); err == nil && f != nil {
			files = append(files, *f)
		}
		// .claude/rules/*.md
		ruleFiles, _ := loadRulesDir(filepath.Join(dir, ".claude", "rules"), TypeProject, seen)
		files = append(files, ruleFiles...)
		// CLAUDE.local.md
		if f, err := loadFile(filepath.Join(dir, "CLAUDE.local.md"), TypeLocal, seen); err == nil && f != nil {
			files = append(files, *f)
		}
	}

	// Resolve @include directives in all loaded files.
	expanded := make([]File, 0, len(files))
	includeSeen := map[string]bool{}
	for _, f := range files {
		includeSeen[f.Path] = true
		included := resolveIncludes(f, includeSeen)
		expanded = append(expanded, included...)
	}

	return expanded, nil
}

// BuildPrompt builds the system-prompt text block from loaded CLAUDE.md files.
// Returns empty string if no files were loaded.
func BuildPrompt(files []File) string {
	if len(files) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(memoryInstructionPrompt)
	sb.WriteString("\n\n")
	for _, f := range files {
		sb.WriteString("Contents of ")
		sb.WriteString(f.Path)
		sb.WriteString(":\n\n")
		sb.WriteString(f.Content)
		sb.WriteString("\n\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// collectDirs returns the directory chain from root down to cwd (inclusive).
func collectDirs(cwd string) []string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	var dirs []string
	dir := abs
	for {
		dirs = append(dirs, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// reverse: dirs[0]=root, dirs[last]=cwd
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

// loadFile reads one file, truncates if over MaxCharCount, deduplicates by path.
func loadFile(path string, typ MemoryType, seen map[string]bool) (*File, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	if seen[abs] {
		return nil, nil
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	seen[abs] = true
	content := string(data)
	if len(content) > MaxCharCount {
		content = content[:MaxCharCount] + fmt.Sprintf("\n\n[truncated: file exceeds %d characters]", MaxCharCount)
	}
	return &File{Path: abs, Content: content, Type: typ}, nil
}

// loadRulesDir loads all *.md files from a rules directory.
func loadRulesDir(dir string, typ MemoryType, seen map[string]bool) ([]File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []File
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // deterministic order
	for _, name := range names {
		if f, err := loadFile(filepath.Join(dir, name), typ, seen); err == nil && f != nil {
			files = append(files, *f)
		}
	}
	return files, nil
}

// resolveIncludes processes @include directives in a file, returning the
// included files (in order) followed by the file itself.
// includeSeen prevents circular references.
func resolveIncludes(f File, seen map[string]bool) []File {
	var result []File
	baseDir := filepath.Dir(f.Path)

	lines := strings.Split(f.Content, "\n")
	var filteredLines []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "@") {
			filteredLines = append(filteredLines, line)
			continue
		}
		// @include directive — extract path
		ref := strings.TrimSpace(trimmed[1:])
		if ref == "" {
			filteredLines = append(filteredLines, line)
			continue
		}
		includePath := resolveIncludePath(ref, baseDir)
		if includePath == "" || seen[includePath] {
			// Skip circular or unresolvable
			continue
		}
		seen[includePath] = true
		data, err := os.ReadFile(includePath)
		if err != nil {
			// Non-existent includes are silently ignored
			continue
		}
		content := string(data)
		if len(content) > MaxCharCount {
			content = content[:MaxCharCount] + fmt.Sprintf("\n\n[truncated: file exceeds %d characters]", MaxCharCount)
		}
		included := File{Path: includePath, Content: content, Type: f.Type}
		// Recurse for nested includes
		result = append(result, resolveIncludes(included, seen)...)
	}

	// Rebuild file content without @include lines
	f.Content = strings.Join(filteredLines, "\n")
	result = append(result, f)
	return result
}

// resolveIncludePath resolves an @include path reference.
// Supports: @/absolute, @~/home, @./relative, @bare (treated as relative).
func resolveIncludePath(ref, baseDir string) string {
	home, _ := os.UserHomeDir()
	switch {
	case strings.HasPrefix(ref, "/"):
		return ref
	case strings.HasPrefix(ref, "~/"):
		return filepath.Join(home, ref[2:])
	case strings.HasPrefix(ref, "./") || strings.HasPrefix(ref, "../"):
		return filepath.Join(baseDir, ref)
	default:
		return filepath.Join(baseDir, ref)
	}
}
