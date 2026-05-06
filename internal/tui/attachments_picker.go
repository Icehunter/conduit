package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/attach"
	"github.com/icehunter/conduit/internal/commands"
)

// expandPastePlaceholders replaces "[Pasted text #N +X lines]" tokens in s
// with the raw content from m.pastedBlocks. Tokens with no matching entry
// are left as-is (shouldn't happen in practice).
func (m Model) expandPastePlaceholders(s string) string {
	if len(m.pastedBlocks) == 0 {
		return s
	}
	return rePasteToken.ReplaceAllStringFunc(s, func(tok string) string {
		sub := rePasteToken.FindStringSubmatch(tok)
		if len(sub) != 2 {
			return tok
		}
		seq, err := strconv.Atoi(sub[1])
		if err != nil {
			return tok
		}
		if raw, ok := m.pastedBlocks[seq]; ok {
			return raw
		}
		return tok
	})
}

func (m Model) userTextContent(text string) []api.ContentBlock {
	content := m.atMentionContent(text)
	content = append(content, api.ContentBlock{Type: "text", Text: text})
	return content
}

func (m Model) atMentionContent(text string) []api.ContentBlock {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	var content []api.ContentBlock
	for _, ref := range attach.ProcessAtMentions(text, cwd) {
		if ref.IsPDF {
			content = append(content, api.ContentBlock{
				Type: "document",
				Source: &api.ImageSource{
					Type:      "base64",
					MediaType: "application/pdf",
					Data:      ref.PDFData,
				},
			})
			continue
		}
		content = append(content, api.ContentBlock{
			Type: "text",
			Text: attach.FormatAtResult(ref),
		})
	}
	return content
}

func localPromptFromContent(content []api.ContentBlock) string {
	var parts []string
	for _, block := range content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// renderCommandPicker renders the slash command picker dropdown.
func (m Model) renderCommandPicker() string {
	const maxItems = 8

	// The current query (text after "/").
	query := strings.ToLower(strings.TrimPrefix(m.input.Value(), "/"))

	// Compute visible window around the selected index.
	start := m.cmdSelected - maxItems/2
	if start < 0 {
		start = 0
	}
	end := start + maxItems
	if end > len(m.cmdMatches) {
		end = len(m.cmdMatches)
		start = end - maxItems
		if start < 0 {
			start = 0
		}
	}

	contentW := floatingInnerWidth(m.width, floatingCommandSpec) - floatingBodyPadX*2
	if contentW < 20 {
		contentW = 20
	}

	if len(m.cmdMatches) == 0 {
		var sb strings.Builder
		sb.WriteString(styleStatusAccent.Render("Commands") + "\n\n")
		displayQuery := strings.TrimPrefix(m.input.Value(), "/")
		if displayQuery == "" {
			sb.WriteString(stylePickerDesc.Render("Type to filter commands."))
		} else {
			sb.WriteString(stylePickerDesc.Render(fmt.Sprintf("No commands found for %q.", displayQuery)))
			sb.WriteByte('\n')
			sb.WriteString(stylePickerDesc.Render("Enter sends it as an unknown command · Esc closes"))
		}
		return sb.String()
	}

	// Compute name column width from the longest name across all matches so
	// the column stays stable as the user scrolls through results.
	nameColW := 0
	for _, cmd := range m.cmdMatches {
		n := len([]rune(cmd.Name)) + 1 // +1 for leading "/"
		if n > nameColW {
			nameColW = n
		}
	}
	const minDescW = 20
	const gap = 2
	if contentW < minDescW+gap+1 {
		contentW = minDescW + gap + 1
	}
	if nameColW > contentW-minDescW-gap {
		nameColW = contentW - minDescW - gap
	}
	descMax := contentW - nameColW - gap
	indent := strings.Repeat(" ", nameColW+gap)

	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render("Commands") + "\n\n")
	for i := start; i < end; i++ {
		if i > start {
			sb.WriteByte('\n')
		}
		cmd := m.cmdMatches[i]

		// Render name: "/" + name padded to nameColW.
		rawName := "/" + cmd.Name
		runes := []rune(rawName)
		if len(runes) > nameColW {
			runes = runes[:nameColW]
		}
		rawName = string(runes) + strings.Repeat(" ", nameColW-len(runes))

		prefix := "  "
		var namePart string
		if i == m.cmdSelected {
			prefix = stylePickerItemSelected.Render("❯ ")
			namePart = highlightMatch(rawName, query, stylePickerItemSelected, stylePickerHighlight)
		} else {
			namePart = highlightMatch(rawName, query, stylePickerItem, stylePickerHighlight)
		}

		// Word-wrap description so it flows to additional lines instead of being cut off.
		descLines := cmdDescWrap(cmd.Description, descMax)
		sb.WriteString(prefix + namePart + surfaceSpaces(gap) + highlightMatch(descLines[0], query, stylePickerDesc, stylePickerHighlight))
		for _, dl := range descLines[1:] {
			sb.WriteByte('\n')
			sb.WriteString(surfaceSpaces(2+lipgloss.Width(indent)) + highlightMatch(dl, query, stylePickerDesc, stylePickerHighlight))
		}
	}

	return sb.String()
}

// cmdDescWrap splits a description into lines of at most maxW runes, breaking
// on word boundaries. Always returns at least one element.
func cmdDescWrap(s string, maxW int) []string {
	if maxW <= 0 || len([]rune(s)) <= maxW {
		return []string{s}
	}
	var lines []string
	words := strings.Fields(s)
	var cur strings.Builder
	for _, w := range words {
		wlen := len([]rune(w))
		if cur.Len() == 0 {
			cur.WriteString(w)
		} else if cur.Len()+1+wlen <= maxW {
			cur.WriteByte(' ')
			cur.WriteString(w)
		} else {
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(w)
		}
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	if len(lines) == 0 {
		return []string{s}
	}
	return lines
}

// highlightMatch renders s with every case-insensitive occurrence of query
// highlighted using highlightStyle, and the rest in baseStyle.
// Returns the base-styled string unchanged if query is empty.
func highlightMatch(s, query string, baseStyle, highlightStyle lipgloss.Style) string {
	if query == "" {
		return baseStyle.Render(s)
	}
	lower := strings.ToLower(s)
	var out strings.Builder
	pos := 0
	for {
		idx := strings.Index(lower[pos:], query)
		if idx < 0 {
			out.WriteString(baseStyle.Render(s[pos:]))
			break
		}
		abs := pos + idx
		if abs > pos {
			out.WriteString(baseStyle.Render(s[pos:abs]))
		}
		out.WriteString(highlightStyle.Render(s[abs : abs+len(query)]))
		pos = abs + len(query)
		if pos >= len(s) {
			break
		}
	}
	return out.String()
}

// computeCommandMatches returns commands matching the current input and resets
// the selection index if the match set changed.
func (m Model) computeCommandMatches() ([]commands.Command, int) {
	text := m.input.Value()
	if !strings.HasPrefix(text, "/") || strings.Contains(text, " ") || m.running {
		return nil, 0
	}
	query := strings.ToLower(strings.TrimPrefix(text, "/"))
	all := m.cfg.Commands.All()
	// Rank matches: 0 = name prefix, 1 = name contains, 2 = description contains.
	// Stable within each rank to preserve alphabetical order from Registry.All().
	type ranked struct {
		cmd  commands.Command
		rank int
	}
	var rs []ranked
	for _, c := range all {
		if c.Name == "quit" {
			continue
		}
		switch {
		case strings.HasPrefix(c.Name, query):
			rs = append(rs, ranked{c, 0})
		case strings.Contains(c.Name, query):
			rs = append(rs, ranked{c, 1})
		case strings.Contains(strings.ToLower(c.Description), query):
			rs = append(rs, ranked{c, 2})
		}
	}
	// Stable sort by rank only; alphabetical order within rank is preserved.
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].rank < rs[j].rank })
	matches := make([]commands.Command, len(rs))
	for i, r := range rs {
		matches[i] = r.cmd
	}
	// Preserve selection if the same set, otherwise reset.
	sel := m.cmdSelected
	if sel >= len(matches) {
		sel = 0
	}
	return matches, sel
}

func (m Model) commandPickerActive() bool {
	text := m.input.Value()
	return !m.running && m.cfg.Commands != nil && strings.HasPrefix(text, "/") && !strings.Contains(text, " ")
}

func (m Model) openCommandPicker() Model {
	if m.running || m.cfg.Commands == nil {
		return m
	}
	m.dismissWelcome()
	m.input.SetValue("/")
	m.input.CursorEnd()
	m.cmdMatches, m.cmdSelected = m.computeCommandMatches()
	m.refreshViewport()
	return m
}

// --- @ file completion ---

// updateAtMatches refreshes the @ file picker only when the cwd or typed
// @fragment changes. Navigation and redraw churn reuse the existing list so
// the picker stays visually stable.
func (m Model) updateAtMatches() Model {
	frag, ok := atFragment(m.input.Value())
	if !ok || m.running {
		m.atMatches = nil
		m.atSelected = 0
		m.atQuery = ""
		m.atCwd = ""
		return m
	}
	cwd, _ := os.Getwd()
	if frag == m.atQuery && cwd == m.atCwd {
		if m.atSelected >= len(m.atMatches) {
			m.atSelected = 0
		}
		return m
	}
	matches := searchFiles(cwd, frag, 8)
	m.atMatches = matches
	m.atSelected = 0
	m.atQuery = frag
	m.atCwd = cwd
	return m
}

func atFragment(val string) (string, bool) {
	if strings.HasPrefix(val, "/") {
		return "", false
	}
	// Find the last token in the input.
	lastIdx := strings.LastIndexAny(val, " \t\n")
	lastToken := val
	if lastIdx >= 0 {
		lastToken = val[lastIdx+1:]
	}
	if !strings.HasPrefix(lastToken, "@") {
		return "", false
	}
	return lastToken[1:], true
}

// acceptAtMatch inserts the selected @ match into the input, replacing the
// current @ fragment.
func (m Model) acceptAtMatch() Model {
	if len(m.atMatches) == 0 {
		return m
	}
	chosen := m.atMatches[m.atSelected]
	cwd, _ := os.Getwd()
	isDir := false
	if info, err := os.Stat(filepath.Join(cwd, chosen)); err == nil && info.IsDir() {
		isDir = true
	}
	val := m.input.Value()
	// Find the @ token at the end and replace it.
	idx := strings.LastIndexAny(val, " \t\n")
	var prefix string
	if idx >= 0 {
		prefix = val[:idx+1]
	}
	// Construct the replacement. Directories keep the picker open for nested
	// selection; files add a trailing space so the user can keep typing.
	replacementPath := chosen
	if isDir {
		replacementPath = strings.TrimRight(chosen, string(os.PathSeparator)) + string(os.PathSeparator)
	}
	replacement := "@" + replacementPath
	if !isDir {
		replacement += " "
	}
	// Quote paths with spaces.
	if strings.Contains(replacementPath, " ") {
		replacement = `@"` + replacementPath + `"`
		if !isDir {
			replacement += " "
		}
	}
	m.input.SetValue(prefix + replacement)
	m.input.CursorEnd()
	m.atMatches = nil
	m.atSelected = 0
	m.atQuery = ""
	m.atCwd = ""
	if isDir {
		return m.updateAtMatches()
	}
	return m
}

// searchFiles returns up to max relative paths in dir matching query.
// It tries fd first (respects .gitignore), falls back to filepath.WalkDir.
func searchFiles(dir, query string, max int) []string {
	// Try fd (fast, respects .gitignore).
	if _, err := exec.LookPath("fd"); err == nil {
		args := []string{"--type", "f", "--type", "d", "--full-path", "--max-results", fmt.Sprintf("%d", max)}
		if query != "" {
			args = append(args, query)
		}
		out, err := exec.CommandContext(context.Background(), "fd", append(args, ".", dir)...).Output()
		if err == nil {
			var paths []string
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if line == "" {
					continue
				}
				rel, err := filepath.Rel(dir, line)
				if err == nil {
					paths = append(paths, rel)
				} else {
					paths = append(paths, line)
				}
				if len(paths) >= max {
					break
				}
			}
			if len(paths) > 0 {
				return paths
			}
		}
	}
	// Fallback: WalkDir with depth ≤ 3.
	queryLow := strings.ToLower(query)
	var paths []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || len(paths) >= max {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "." {
			return nil
		}
		// Skip hidden dirs, .git, node_modules, vendor at depth > 0.
		name := d.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Depth check: count separators.
		depth := strings.Count(rel, string(os.PathSeparator))
		if d.IsDir() && depth >= 3 {
			return filepath.SkipDir
		}
		haystack := strings.ToLower(name)
		if strings.Contains(query, "/") || strings.Contains(query, string(os.PathSeparator)) {
			haystack = strings.ToLower(rel)
		}
		if queryLow == "" || strings.Contains(haystack, queryLow) {
			paths = append(paths, rel)
		}
		return nil
	})
	return paths
}

// renderAtPicker renders the @ file completion picker above the input box.
// Returns "" when no matches are active.
func (m Model) renderAtPicker() string {
	if len(m.atMatches) == 0 {
		return ""
	}
	const maxItems = 8
	cwd, _ := os.Getwd()
	start := m.atSelected - maxItems/2
	if start < 0 {
		start = 0
	}
	end := start + maxItems
	if end > len(m.atMatches) {
		end = len(m.atMatches)
		start = end - maxItems
		if start < 0 {
			start = 0
		}
	}

	contentW := floatingInnerWidth(m.width, floatingPickerSpec)
	var sb strings.Builder
	sb.WriteString(styleStatusAccent.Render("Files") + "\n\n")
	for i := start; i < end; i++ {
		path := m.atMatches[i]
		icon := "+"
		if info, err := os.Stat(filepath.Join(cwd, path)); err == nil && info.IsDir() {
			icon = "◇"
			path += "/"
		}
		path = truncateMiddle(path, contentW-4)
		line := fmt.Sprintf("%s %s", icon, path)
		if i == m.atSelected {
			line = stylePickerItemSelected.Render("❯ " + line)
		} else {
			line = stylePickerItem.Render("  " + line)
		}
		sb.WriteString(line)
		if i < end-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// tabComplete returns the best completion for a partial slash command.
// If exactly one command matches the prefix, it returns "/<name> " (with trailing
// space so the user can immediately type args). If multiple match, it completes
// to the longest common prefix. If none match, returns input unchanged.
func (m Model) tabComplete(input string) string {
	prefix := strings.ToLower(strings.TrimPrefix(input, "/"))
	cmds := m.cfg.Commands.All()

	var matches []string
	for _, c := range cmds {
		if strings.HasPrefix(c.Name, prefix) {
			matches = append(matches, c.Name)
		}
	}
	switch len(matches) {
	case 0:
		return input
	case 1:
		return "/" + matches[0] + " "
	default:
		// Longest common prefix of all matches.
		lcp := matches[0]
		for _, m := range matches[1:] {
			for len(lcp) > 0 && !strings.HasPrefix(m, lcp) {
				lcp = lcp[:len(lcp)-1]
			}
		}
		return "/" + lcp
	}
}
