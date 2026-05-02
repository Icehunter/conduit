package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	prefixYou    = "▶ You"
	prefixClaude = "◀ Claude"

	// outerPad is the number of spaces added to each side of viewport content.
	outerPad = 2
)

// renderMessage renders one message for display in the viewport.
// width is the full viewport width; inner content uses width-2*outerPad.
func renderMessage(msg Message, width int) string {
	if width < 20 {
		width = 80
	}
	inner := width - outerPad*2
	pad := strings.Repeat(" ", outerPad)

	switch msg.Role {
	case RoleUser:
		prefix := styleYouPrefix.Render(prefixYou)
		body := styleUserText.Render(msg.Content)
		return pad + prefix + "  " + body

	case RoleAssistant:
		prefix := styleClaudePrefix.Render(prefixClaude)
		body := renderMarkdown(msg.Content, inner)
		// Indent body lines by outerPad
		indented := indentLines(body, pad)
		return pad + prefix + "\n" + indented

	case RoleTool:
		badge := styleToolBadge.Render("⚙ " + msg.ToolName)
		content := styleToolContent.Render(msg.Content)
		return pad + "  " + badge + "  " + content

	case RoleError:
		return pad + styleErrorText.Render("✗ " + msg.Content)

	case RoleSystem:
		return pad + styleSystemText.Render("· " + msg.Content)
	}
	return msg.Content
}

// indentLines prepends pad to every line of s.
func indentLines(s, pad string) string {
	if pad == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n")
}

// renderMarkdown does lightweight markdown rendering with syntax highlighting.
func renderMarkdown(text string, width int) string {
	lines := strings.Split(text, "\n")
	var out strings.Builder
	inCode := false
	var codeBuf strings.Builder
	var codeLang string

	for _, line := range lines {
		if strings.HasPrefix(line, "```") {
			if inCode {
				code := strings.TrimRight(codeBuf.String(), "\n")
				out.WriteString(renderCodeBlock(code, codeLang, width))
				out.WriteByte('\n')
				codeBuf.Reset()
				codeLang = ""
				inCode = false
			} else {
				codeLang = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "```")))
				inCode = true
			}
			continue
		}
		if inCode {
			codeBuf.WriteString(line)
			codeBuf.WriteByte('\n')
			continue
		}
		out.WriteString(renderLine(line))
		out.WriteByte('\n')
	}
	if inCode && codeBuf.Len() > 0 {
		code := strings.TrimRight(codeBuf.String(), "\n")
		out.WriteString(renderCodeBlock(code, codeLang, width))
		out.WriteByte('\n')
	}
	return strings.TrimRight(out.String(), "\n")
}

// renderCodeBlock renders a fenced code block with rounded border and
// language-aware syntax highlighting.
// width is the available content width (already excluding outer padding).
// The border adds 2 cols (left+right), so the inner highlight width is width-2-2(padding).
func renderCodeBlock(code, lang string, width int) string {
	highlighted := highlightCode(code, lang)

	// styleCodeBorder has PaddingLeft(1)+PaddingRight(1) and a 1-col border each side.
	// Total horizontal chrome = 4. Inner content gets width-4 cols.
	innerW := width - 4
	if innerW < 10 {
		innerW = 10
	}

	// Render the block at the correct inner width so lipgloss doesn't wrap.
	block := styleCodeBorder.
		Width(innerW).
		Render(highlighted)

	// Splice language label into the top border after the corner glyph.
	// "╭──────" → "╭─ go ──────"
	if lang != "" {
		lines := strings.SplitN(block, "\n", 2)
		if len(lines) == 2 {
			top := []rune(lines[0])
			// top[0] is "╭", top[1] is "─". Insert " lang " after top[1].
			if len(top) > 2 {
				label := []rune(" " + lang + " ")
				// Keep corner + one dash, then label, then remaining dashes.
				rest := top[2:]
				if len(label) < len(rest) {
					rest = rest[len(label):]
				} else {
					rest = nil
				}
				newTop := string(top[:2]) + string(label) + string(rest)
				block = newTop + "\n" + lines[1]
			}
		}
	}
	return block
}

// highlightCode applies terminal color to code based on language.
// This is a hand-rolled highlighter covering the most common token classes —
// full Tree-sitter or Chroma integration lands in M5.
func highlightCode(code, lang string) string {
	lines := strings.Split(code, "\n")
	highlighted := make([]string, len(lines))
	for i, line := range lines {
		highlighted[i] = highlightLine(line, lang)
	}
	return strings.Join(highlighted, "\n")
}

// Token colors for syntax highlighting.
var (
	cKeyword  = lipgloss.NewStyle().Foreground(lipgloss.Color("#C792EA")) // purple
	cBuiltin  = lipgloss.NewStyle().Foreground(lipgloss.Color("#82AAFF")) // blue
	cString   = lipgloss.NewStyle().Foreground(lipgloss.Color("#C3E88D")) // green
	cComment  = lipgloss.NewStyle().Foreground(lipgloss.Color("#546E7A")).Italic(true)
	cNumber   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F78C6C")) // orange
	cOperator = lipgloss.NewStyle().Foreground(lipgloss.Color("#89DDFF")) // cyan
	cType     = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFCB6B")) // yellow
	cPlain    = lipgloss.NewStyle().Foreground(lipgloss.Color("#D4D8E0"))
)

// keywords by language.
var langKeywords = map[string][]string{
	"go": {
		"package", "import", "func", "var", "const", "type", "struct",
		"interface", "map", "chan", "go", "defer", "return", "if", "else",
		"for", "range", "switch", "case", "default", "break", "continue",
		"select", "fallthrough", "goto", "nil", "true", "false",
	},
	"python": {
		"def", "class", "import", "from", "return", "if", "elif", "else",
		"for", "while", "in", "not", "and", "or", "is", "lambda", "with",
		"as", "pass", "break", "continue", "try", "except", "finally",
		"raise", "yield", "global", "nonlocal", "True", "False", "None",
		"print", "range", "len", "type", "str", "int", "float", "list",
		"dict", "set", "tuple",
	},
	"javascript": {
		"const", "let", "var", "function", "return", "if", "else", "for",
		"while", "do", "switch", "case", "break", "continue", "class",
		"import", "export", "default", "from", "new", "this", "typeof",
		"instanceof", "void", "delete", "in", "of", "async", "await",
		"try", "catch", "finally", "throw", "null", "undefined", "true", "false",
	},
	"typescript": {
		"const", "let", "var", "function", "return", "if", "else", "for",
		"while", "class", "import", "export", "interface", "type", "enum",
		"extends", "implements", "new", "this", "async", "await", "null",
		"undefined", "true", "false", "string", "number", "boolean", "any",
		"void", "never", "readonly", "public", "private", "protected",
	},
	"rust": {
		"fn", "let", "mut", "const", "struct", "enum", "impl", "trait",
		"pub", "use", "mod", "crate", "super", "self", "return", "if",
		"else", "for", "while", "loop", "match", "in", "ref", "move",
		"async", "await", "dyn", "box", "where", "type", "unsafe",
		"true", "false", "None", "Some", "Ok", "Err",
	},
	"bash": {
		"if", "then", "else", "elif", "fi", "for", "do", "done", "while",
		"case", "esac", "function", "return", "export", "local", "echo",
		"source", "shift", "exit", "true", "false",
	},
	"sh": {
		"if", "then", "else", "elif", "fi", "for", "do", "done", "while",
		"case", "esac", "function", "return", "export", "local", "echo",
		"source", "shift", "exit", "true", "false",
	},
}

// commentPrefixes by language.
var langComments = map[string][]string{
	"go":         {"//", "/*"},
	"python":     {"#"},
	"javascript": {"//", "/*"},
	"typescript": {"//", "/*"},
	"rust":       {"//", "/*"},
	"bash":       {"#"},
	"sh":         {"#"},
}

// highlightLine colorizes a single line of code.
func highlightLine(line, lang string) string {
	// Whole-line comments
	if prefixes, ok := langComments[lang]; ok {
		trimmed := strings.TrimLeft(line, " \t")
		for _, p := range prefixes {
			if strings.HasPrefix(trimmed, p) {
				return cComment.Render(line)
			}
		}
	}

	// For languages we know, tokenize by word boundaries and colorize keywords.
	if _, ok := langKeywords[lang]; ok {
		return tokenizeLine(line, lang)
	}

	// Unknown language — render plain.
	return cPlain.Render(line)
}

// tokenizeLine splits a line into tokens and applies colors.
func tokenizeLine(line, lang string) string {
	keywords := langKeywords[lang]
	kwSet := make(map[string]bool, len(keywords))
	for _, k := range keywords {
		kwSet[k] = true
	}

	var out strings.Builder
	i := 0
	runes := []rune(line)
	n := len(runes)

	for i < n {
		ch := runes[i]

		// String literals: " or '
		if ch == '"' || ch == '\'' {
			quote := ch
			j := i + 1
			for j < n && runes[j] != quote {
				if runes[j] == '\\' {
					j++
				}
				j++
			}
			if j < n {
				j++
			}
			out.WriteString(cString.Render(string(runes[i:j])))
			i = j
			continue
		}

		// Backtick strings (JS/TS/Go)
		if ch == '`' {
			j := i + 1
			for j < n && runes[j] != '`' {
				j++
			}
			if j < n {
				j++
			}
			out.WriteString(cString.Render(string(runes[i:j])))
			i = j
			continue
		}

		// Numbers
		if ch >= '0' && ch <= '9' {
			j := i
			for j < n && (runes[j] >= '0' && runes[j] <= '9' || runes[j] == '.' || runes[j] == 'x' || runes[j] == 'X') {
				j++
			}
			out.WriteString(cNumber.Render(string(runes[i:j])))
			i = j
			continue
		}

		// Identifiers and keywords
		if isIdent(ch) {
			j := i
			for j < n && isIdent(runes[j]) {
				j++
			}
			word := string(runes[i:j])
			if kwSet[word] {
				out.WriteString(cKeyword.Render(word))
			} else if isType(word) {
				out.WriteString(cType.Render(word))
			} else {
				out.WriteString(cPlain.Render(word))
			}
			i = j
			continue
		}

		// Operators and punctuation
		if isOperator(ch) {
			out.WriteString(cOperator.Render(string(ch)))
		} else {
			out.WriteString(cPlain.Render(string(ch)))
		}
		i++
	}
	return out.String()
}

func isIdent(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_'
}

func isType(word string) bool {
	if len(word) == 0 {
		return false
	}
	r := rune(word[0])
	return r >= 'A' && r <= 'Z'
}

func isOperator(r rune) bool {
	return strings.ContainsRune("+-*/=<>!&|^~%", r)
}

// renderLine applies inline styling to a prose line.
func renderLine(line string) string {
	line = applyDelim(line, "**", lipgloss.NewStyle().Bold(true))
	line = applyDelim(line, "`", styleInlineCode)
	return styleAssistantText.Render(line)
}

// applyDelim finds delimiter-wrapped spans and applies style.
func applyDelim(line, delim string, style lipgloss.Style) string {
	var out strings.Builder
	for {
		i := strings.Index(line, delim)
		if i < 0 {
			out.WriteString(line)
			break
		}
		j := strings.Index(line[i+len(delim):], delim)
		if j < 0 {
			out.WriteString(line)
			break
		}
		j += i + len(delim)
		out.WriteString(line[:i])
		out.WriteString(style.Render(line[i+len(delim) : j]))
		line = line[j+len(delim):]
	}
	return out.String()
}

// separator returns a dim horizontal rule across the full width.
func separator(width int) string {
	if width < 1 {
		width = 1
	}
	return styleSep.Render(strings.Repeat("─", width))
}
