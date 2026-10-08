// Package skilllint validates and lints SKILL.md content written through
// SkillManage. Validate holds the non-negotiable checks (a write that fails
// them is rejected); Lint returns advisory findings that are reported back to
// the writer but never block the write.
package skilllint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	// MaxNameLen is the longest accepted skill name.
	MaxNameLen = 64
	// MaxDescriptionLen is the longest accepted frontmatter description.
	MaxDescriptionLen = 1024
	// MaxSkillChars is the hard cap on SKILL.md size.
	MaxSkillChars = 100_000
	// MaxSupportFileBytes is the hard cap on one references/ file.
	MaxSupportFileBytes = 1 << 20

	// DescriptionSoftLen is the length past which a description stops being
	// a one-line routing hint.
	DescriptionSoftLen = 240
	// BodySoftBudgetChars is the advisory body size: SKILL.md rides in
	// context for every later call of a session once viewed.
	BodySoftBudgetChars = 24_000
	// MaxReferenceFiles is the references/ file count past which a skill is
	// hoarding per-session notes instead of topical depth.
	MaxReferenceFiles = 60

	incidentRefMin     = 4
	incidentRefPerKchr = 0.5
)

// Severity of a Finding.
const (
	Error   = "error"
	Warning = "warning"
)

// Finding is one lint result.
type Finding struct {
	Severity string
	Rule     string
	Message  string
}

func (f Finding) String() string { return fmt.Sprintf("[%s] %s: %s", f.Rule, f.Severity, f.Message) }

var (
	nameRE     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	refPathRE  = regexp.MustCompile(`(?:references|templates|assets)/[\w./-]+`)
	incidentRE = regexp.MustCompile(`(?:^|[^\w/])#\d{3,6}\b|\b(?:PR|issue)\s*#?\d{3,6}\b`)
	fenceRE    = regexp.MustCompile("(?s)```.*?```")
	whenToUse  = regexp.MustCompile(`(?mi)^#+\s+when to use`)
	marketing  = []string{"powerful", "comprehensive", "seamless", "advanced", "cutting-edge", "state-of-the-art", "revolutionary", "robust"}
	forbidden  = []string{"README.md", "CHANGELOG.md", "install.sh", ".env", ".env.example", ".gitignore"}
)

// ValidName reports whether name is an acceptable skill directory name.
func ValidName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("skill name cannot be empty")
	case len(name) > MaxNameLen:
		return fmt.Errorf("skill name %q exceeds %d characters", name, MaxNameLen)
	case !nameRE.MatchString(name):
		return fmt.Errorf("skill name %q must be lowercase letters, digits, hyphens and underscores, starting with a letter or digit", name)
	}
	return nil
}

// Parse splits SKILL.md content into its frontmatter map and body. ok is
// false when the content has no complete leading "---" block. The parser reads
// top-level "key: value" pairs, strips surrounding quotes, and folds "|" / ">"
// block scalars; nested mappings and lists are kept as raw text under their
// top-level key.
func Parse(content string) (fm map[string]string, body string, ok bool) {
	content = strings.TrimPrefix(content, "\ufeff")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(content, "---\n") && content != "---" {
		return nil, content, false
	}
	rest := strings.TrimPrefix(content, "---")
	rest = strings.TrimPrefix(rest, "\n")
	var raw []string
	closed := false
	lines := strings.Split(rest, "\n")
	i := 0
	for ; i < len(lines); i++ {
		if strings.TrimRight(lines[i], " \t") == "---" {
			closed = true
			break
		}
		raw = append(raw, lines[i])
	}
	if !closed {
		return nil, content, false
	}
	body = strings.Join(lines[i+1:], "\n")

	fm = map[string]string{}
	var key string
	var block []string
	flush := func() {
		if key != "" && len(block) > 0 {
			fm[key] = strings.TrimSpace(strings.Join(block, " "))
		}
		key, block = "", nil
	}
	for _, line := range raw {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indented := line[0] == ' ' || line[0] == '\t'
		if indented {
			if key != "" {
				block = append(block, trimmed)
			}
			continue
		}
		flush()
		k, v, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch {
		case v == "" || v == "|" || v == ">" || v == "|-" || v == ">-":
			fm[key] = ""
		default:
			fm[key] = unquote(v)
			// A scalar value may still continue on indented lines.
			block = []string{fm[key]}
		}
	}
	flush()
	return fm, body, true
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}

// Validate applies the non-negotiable checks. A non-nil error means the write
// must be rejected.
func Validate(content string) error {
	if strings.TrimSpace(content) == "" {
		return fmt.Errorf("content cannot be empty")
	}
	if n := len([]rune(content)); n > MaxSkillChars {
		return fmt.Errorf("SKILL.md is %d characters (limit %d); move topic depth into references/ files", n, MaxSkillChars)
	}
	fm, body, ok := Parse(content)
	if !ok {
		return fmt.Errorf("SKILL.md must start with YAML frontmatter delimited by --- lines")
	}
	name, desc := strings.TrimSpace(fm["name"]), strings.TrimSpace(fm["description"])
	if name == "" {
		return fmt.Errorf("frontmatter must include a non-empty 'name' field")
	}
	if err := ValidName(name); err != nil {
		return fmt.Errorf("frontmatter name: %w", err)
	}
	if desc == "" {
		return fmt.Errorf("frontmatter must include a non-empty 'description' field")
	}
	if n := len([]rune(desc)); n > MaxDescriptionLen {
		return fmt.Errorf("description is %d characters (limit %d)", n, MaxDescriptionLen)
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("SKILL.md must have instructions after the frontmatter")
	}
	return nil
}

// Lint returns advisory findings for content. skillDir, when non-empty,
// enables checks against the files on disk (name/dir match, dangling
// references, forbidden files, references sprawl).
func Lint(content, skillDir string) []Finding {
	fm, body, ok := Parse(content)
	if !ok {
		return nil
	}
	var out []Finding
	warn := func(rule, format string, a ...any) {
		out = append(out, Finding{Warning, rule, fmt.Sprintf(format, a...)})
	}

	name := strings.TrimSpace(fm["name"])
	if skillDir != "" && name != "" && name != filepath.Base(skillDir) {
		warn("name-dir-mismatch", "frontmatter name %q does not match directory %q", name, filepath.Base(skillDir))
	}
	desc := strings.TrimSpace(fm["description"])
	if n := len([]rune(desc)); n > DescriptionSoftLen {
		warn("description-length", "description is %d chars; keep it to one routing sentence (under %d)", n, DescriptionSoftLen)
	}
	low := strings.ToLower(desc)
	var hits []string
	for _, w := range marketing {
		if regexp.MustCompile(`\b` + regexp.QuoteMeta(w) + `\b`).MatchString(low) {
			hits = append(hits, w)
		}
	}
	if len(hits) > 0 {
		warn("description-marketing", "description contains marketing words %v; state the capability instead", hits)
	}
	if n := len([]rune(body)); n > BodySoftBudgetChars {
		warn("oversized-body", "body is %d chars; keep always-on rules here and move topic depth into references/<topic>.md", n)
	}
	if !whenToUse.MatchString(body) {
		warn("missing-section", "no '## When to Use' section; skills need explicit trigger conditions near the top")
	}
	prose := fenceRE.ReplaceAllString(body, "")
	if refs := len(incidentRE.FindAllString(prose, -1)); refs >= incidentRefMin &&
		float64(refs)/float64(max(len(prose), 1))*1000 >= incidentRefPerKchr {
		warn("incident-log-shape", "%d PR/issue references in prose; state the rule and drop the incident numbers", refs)
	}
	if skillDir == "" {
		return out
	}
	seen := map[string]bool{}
	for _, rel := range refPathRE.FindAllString(body, -1) {
		rel = strings.TrimRight(rel, ".")
		if seen[rel] || strings.HasSuffix(rel, "/") {
			continue
		}
		seen[rel] = true
		if _, err := os.Stat(filepath.Join(skillDir, filepath.FromSlash(rel))); err != nil {
			warn("dangling-reference", "body references %q but that file does not exist in the skill directory", rel)
		}
	}
	for _, f := range forbidden {
		if _, err := os.Stat(filepath.Join(skillDir, f)); err == nil {
			warn("forbidden-file", "skill ships %q; skills should not include scaffolding files", f)
		}
	}
	n := 0
	_ = filepath.WalkDir(filepath.Join(skillDir, "references"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".md") {
			n++
		}
		return nil
	})
	if n > MaxReferenceFiles {
		warn("references-sprawl", "%d files under references/; merge same-topic notes into one rule set", n)
	}
	return out
}

// ValidSupportPath checks a skill-relative path for write_file/remove_file:
// it must sit under references/, be relative, and not escape the skill dir.
func ValidSupportPath(rel string) error {
	if rel == "" {
		return fmt.Errorf("file_path is required")
	}
	if strings.ContainsRune(rel, 0) || strings.Contains(rel, `\`) {
		return fmt.Errorf("file_path %q contains invalid characters", rel)
	}
	clean := filepath.ToSlash(filepath.Clean(rel))
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || clean != strings.TrimPrefix(rel, "./") {
		return fmt.Errorf("file_path %q must be a clean relative path", rel)
	}
	if !strings.HasPrefix(clean, "references/") || strings.HasSuffix(clean, "/") {
		return fmt.Errorf("file_path %q must be a file under references/", rel)
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." || seg == "" {
			return fmt.Errorf("file_path %q must not contain '..'", rel)
		}
	}
	return nil
}
