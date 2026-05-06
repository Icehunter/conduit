package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/icehunter/conduit/internal/theme"
)

// codePalette returns syntax highlight colors appropriate for the active
// theme. Light themes get darker token colors so they remain readable on
// light bg; dark themes use the original Material Theme palette.
type codePaletteSet struct {
	keyword, str, comment, number, operator, typ, plain color.Color
}

var (
	codePaletteDark = codePaletteSet{
		keyword:  lipgloss.Color("#C792EA"), // purple
		str:      lipgloss.Color("#C3E88D"), // pale green
		comment:  lipgloss.Color("#546E7A"), // slate
		number:   lipgloss.Color("#F78C6C"), // orange
		operator: lipgloss.Color("#89DDFF"), // cyan
		typ:      lipgloss.Color("#FFCB6B"), // amber
		plain:    lipgloss.Color("#D4D8E0"), // off-white
	}
	codePaletteLight = codePaletteSet{
		keyword:  lipgloss.Color("#7B2D88"), // dark purple
		str:      lipgloss.Color("#1A7F37"), // dark green
		comment:  lipgloss.Color("#6E7681"), // mid gray
		number:   lipgloss.Color("#B65A1A"), // dark orange
		operator: lipgloss.Color("#0550AE"), // dark blue
		typ:      lipgloss.Color("#9A6700"), // dark amber
		plain:    lipgloss.Color("#1F2328"), // near-black
	}
)

// codeStyle returns a syntax style for one category, picking the palette set
// based on the active theme. Keep the background on the shared window surface
// so styled code tokens do not punch black cells through chat.
func codeStyle(get func(codePaletteSet) color.Color) lipgloss.Style {
	set := codePaletteDark
	if isLightTheme() {
		set = codePaletteLight
	}
	return lipgloss.NewStyle().Foreground(get(set)).Background(colorWindowBg)
}

// isLightTheme returns true when the active palette uses dark foregrounds
// (i.e. is meant for light terminal backgrounds).
func isLightTheme() bool {
	name := theme.Active().Name
	return strings.HasPrefix(name, "light")
}

// Token style accessors — call at render time so theme switches apply.
func cKeywordStyle() lipgloss.Style {
	return codeStyle(func(s codePaletteSet) color.Color { return s.keyword })
}
func cStringStyle() lipgloss.Style {
	return codeStyle(func(s codePaletteSet) color.Color { return s.str })
}
func cCommentStyle() lipgloss.Style {
	return codeStyle(func(s codePaletteSet) color.Color { return s.comment }).Italic(true)
}
func cNumberStyle() lipgloss.Style {
	return codeStyle(func(s codePaletteSet) color.Color { return s.number })
}
func cOperatorStyle() lipgloss.Style {
	return codeStyle(func(s codePaletteSet) color.Color { return s.operator })
}
func cTypeStyle() lipgloss.Style {
	return codeStyle(func(s codePaletteSet) color.Color { return s.typ })
}
func cPlainStyle() lipgloss.Style {
	return codeStyle(func(s codePaletteSet) color.Color { return s.plain })
}

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

var (
	cDiffAdd    lipgloss.Style
	cDiffDel    lipgloss.Style
	cDiffHunk   lipgloss.Style
	cDiffHeader lipgloss.Style
)

func highlightLine(line, lang string) string {
	// Diff language: color by line prefix.
	if lang == "diff" || lang == "patch" {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			return cDiffHeader.Render(line)
		case strings.HasPrefix(line, "+"):
			return cDiffAdd.Render(line)
		case strings.HasPrefix(line, "-"):
			return cDiffDel.Render(line)
		case strings.HasPrefix(line, "@@"):
			return cDiffHunk.Render(line)
		default:
			return cPlainStyle().Render(line)
		}
	}

	// Whole-line comment check.
	if prefixes, ok := langComments[lang]; ok {
		trimmed := strings.TrimLeft(line, " \t")
		for _, p := range prefixes {
			if strings.HasPrefix(trimmed, p) {
				return cCommentStyle().Render(line)
			}
		}
	}

	// YAML/JSON/TOML and unknown — render plain but still colored.
	if _, ok := langKeywords[lang]; !ok || (lang != "" && len(langKeywords[lang]) == 0) {
		return cPlainStyle().Render(line)
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
			out.WriteString(cStringStyle().Render(string(runes[i:j])))
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
			out.WriteString(cStringStyle().Render(string(runes[i:j])))
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
			out.WriteString(cNumberStyle().Render(string(runes[i:j])))
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
				out.WriteString(cKeywordStyle().Render(word))
			case isTypeName(word):
				out.WriteString(cTypeStyle().Render(word))
			default:
				out.WriteString(cPlainStyle().Render(word))
			}
			i = j
			continue
		}

		// Operator
		if isOperator(ch) {
			out.WriteString(cOperatorStyle().Render(string(ch)))
		} else {
			out.WriteString(cPlainStyle().Render(string(ch)))
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
