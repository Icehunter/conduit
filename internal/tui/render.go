package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

const (
	prefixYou    = "▶ You"
	prefixClaude = "◀ Claude"

	// outerPad is spaces on each side of all viewport content.
	outerPad = 2
)

// renderMessage renders one message for display.
// width is the full viewport width.
func renderMessage(msg Message, width int) string {
	if width < 20 {
		width = 80
	}
	inner := width - outerPad*2
	if inner < 10 {
		inner = 10
	}
	pad := strings.Repeat(" ", outerPad)

	switch msg.Role {
	case RoleUser:
		return pad + styleYouPrefix.Render(prefixYou) + "  " + styleUserText.Render(msg.Content)

	case RoleAssistant:
		body := renderMarkdown(msg.Content, inner)
		return pad + styleClaudePrefix.Render(prefixClaude) + "\n" + indentLines(body, pad)

	case RoleTool:
		return pad + "  " + styleToolBadge.Render("⚙ "+msg.ToolName) + "  " + styleToolContent.Render(msg.Content)

	case RoleError:
		return pad + styleErrorText.Render("✗ "+msg.Content)

	case RoleSystem:
		return pad + styleSystemText.Render("· "+msg.Content)
	}
	return msg.Content
}

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
// width is already the inner usable width (after outer padding is removed).
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

// renderCodeBlock renders a fenced code block.
// width is the usable inner width (outer padding already excluded).
// Language label appears as a dim line above the rounded box.
func renderCodeBlock(code, lang string, width int) string {
	highlighted := highlightCode(code, lang)
	// No border — styleCodeBorder is just a left-padding indent.
	// Width() here sets max content width to prevent long lines overflowing.
	block := styleCodeBorder.Width(width).Render(highlighted)

	if lang != "" {
		label := styleCodeLang.Render(lang)
		return label + "\n" + block
	}
	return block
}

// highlightCode colorizes code by language.
// All token styles include Background(colorCodeBg) so they don't reset
// the parent container's background between tokens.
func highlightCode(code, lang string) string {
	lines := strings.Split(code, "\n")
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = highlightLine(line, lang)
	}
	return strings.Join(out, "\n")
}

// Token color styles — foreground only, transparent background.
var (
	cKeyword  = lipgloss.NewStyle().Foreground(lipgloss.Color("#C792EA"))
	cString   = lipgloss.NewStyle().Foreground(lipgloss.Color("#C3E88D"))
	cComment  = lipgloss.NewStyle().Foreground(lipgloss.Color("#546E7A")).Italic(true)
	cNumber   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F78C6C"))
	cOperator = lipgloss.NewStyle().Foreground(lipgloss.Color("#89DDFF"))
	cType     = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFCB6B"))
	cPlain    = lipgloss.NewStyle().Foreground(lipgloss.Color("#D4D8E0"))
)

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
		"print", "range", "len", "type", "str", "int", "float",
	},
	"javascript": {
		"const", "let", "var", "function", "return", "if", "else", "for",
		"while", "switch", "case", "break", "class", "import", "export",
		"default", "from", "new", "this", "async", "await", "try", "catch",
		"throw", "null", "undefined", "true", "false", "typeof", "instanceof",
	},
	"typescript": {
		"const", "let", "var", "function", "return", "if", "else", "for",
		"while", "class", "import", "export", "interface", "type", "enum",
		"extends", "implements", "new", "async", "await", "null",
		"undefined", "true", "false", "string", "number", "boolean", "any",
		"void", "never", "readonly", "public", "private", "protected",
	},
	"rust": {
		"fn", "let", "mut", "const", "struct", "enum", "impl", "trait",
		"pub", "use", "mod", "return", "if", "else", "for", "while", "loop",
		"match", "in", "async", "await", "dyn", "where", "type", "unsafe",
		"true", "false", "None", "Some", "Ok", "Err", "self", "Self",
	},
	"kotlin": {
		"fun", "val", "var", "class", "object", "interface", "data", "sealed",
		"abstract", "open", "override", "return", "if", "else", "for", "while",
		"when", "is", "as", "in", "import", "package", "null", "true", "false",
		"this", "super", "companion", "by", "init", "constructor", "lateinit",
		"suspend", "coroutine", "launch", "async", "await", "try", "catch",
		"throw", "finally", "enum", "annotation",
	},
	"java": {
		"class", "interface", "extends", "implements", "public", "private",
		"protected", "static", "final", "void", "return", "if", "else", "for",
		"while", "new", "import", "package", "null", "true", "false", "this",
		"super", "try", "catch", "throw", "throws", "finally", "abstract",
		"synchronized", "instanceof",
	},
	"bash": {"if", "then", "else", "elif", "fi", "for", "do", "done", "while",
		"case", "esac", "function", "return", "export", "local", "echo", "exit",
	},
	"sh":   {"if", "then", "else", "elif", "fi", "for", "do", "done", "while", "case", "esac", "echo"},
	"yaml": {},
	"json": {},
	"toml": {},
	"sql":  {"SELECT", "FROM", "WHERE", "INSERT", "UPDATE", "DELETE", "CREATE", "TABLE", "JOIN", "ON", "AND", "OR", "NOT", "IN", "AS", "BY", "GROUP", "ORDER", "LIMIT", "OFFSET"},
}

var langComments = map[string][]string{
	"go": {"//", "/*"}, "python": {"#"}, "javascript": {"//", "/*"},
	"typescript": {"//", "/*"}, "rust": {"//", "/*"}, "kotlin": {"//", "/*"},
	"java": {"//", "/*"}, "bash": {"#"}, "sh": {"#"},
}

func highlightLine(line, lang string) string {
	// Whole-line comment check.
	if prefixes, ok := langComments[lang]; ok {
		trimmed := strings.TrimLeft(line, " \t")
		for _, p := range prefixes {
			if strings.HasPrefix(trimmed, p) {
				return cComment.Render(line)
			}
		}
	}

	// YAML/JSON/TOML and unknown — render plain but still colored.
	if _, ok := langKeywords[lang]; !ok || (lang != "" && len(langKeywords[lang]) == 0) {
		return cPlain.Render(line)
	}

	return tokenizeLine(line, lang)
}

func tokenizeLine(line, lang string) string {
	kwSet := make(map[string]bool)
	for _, k := range langKeywords[lang] {
		kwSet[k] = true
	}

	var out strings.Builder
	runes := []rune(line)
	n := len(runes)
	i := 0

	for i < n {
		ch := runes[i]

		// String: " or '
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

		// Backtick string
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

		// Number
		if ch >= '0' && ch <= '9' {
			j := i
			for j < n && (runes[j] >= '0' && runes[j] <= '9' || runes[j] == '.' ||
				runes[j] == 'x' || runes[j] == 'X' || runes[j] == '_') {
				j++
			}
			out.WriteString(cNumber.Render(string(runes[i:j])))
			i = j
			continue
		}

		// Word (identifier or keyword)
		if isIdent(ch) {
			j := i
			for j < n && isIdent(runes[j]) {
				j++
			}
			word := string(runes[i:j])
			switch {
			case kwSet[word]:
				out.WriteString(cKeyword.Render(word))
			case isTypeName(word):
				out.WriteString(cType.Render(word))
			default:
				out.WriteString(cPlain.Render(word))
			}
			i = j
			continue
		}

		// Operator
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

// isTypeName: PascalCase words are likely type names.
func isTypeName(word string) bool {
	if len(word) < 2 {
		return false
	}
	r := rune(word[0])
	r2 := rune(word[1])
	return r >= 'A' && r <= 'Z' && ((r2 >= 'a' && r2 <= 'z') || (r2 >= 'A' && r2 <= 'Z'))
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

// separator returns a full-width dim rule.
func separator(width int) string {
	if width < 1 {
		width = 1
	}
	return styleSep.Render(strings.Repeat("─", width))
}
