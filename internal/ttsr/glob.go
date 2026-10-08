package ttsr

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

func unmarshalJSON(raw string, v any) error { return json.Unmarshal([]byte(raw), v) }

// collectStrings writes every scalar in v to sb, one per line, objects in key
// order so matching is deterministic.
func collectStrings(sb *strings.Builder, v any) {
	switch t := v.(type) {
	case string:
		sb.WriteString(t)
		sb.WriteByte('\n')
	case []any:
		for _, e := range t {
			collectStrings(sb, e)
		}
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			collectStrings(sb, t[k])
		}
	case float64, bool:
		fmt.Fprintf(sb, "%v\n", t)
	}
}

// globAny reports whether name matches any glob (path.Match syntax). fold
// makes the comparison case-insensitive.
func globAny(globs []string, name string, fold bool) bool {
	if fold {
		name = strings.ToLower(name)
	}
	for _, g := range globs {
		if fold {
			g = strings.ToLower(g)
		}
		if ok, err := path.Match(g, name); err == nil && ok {
			return true
		}
	}
	return false
}

// pathGlobAny reports whether p matches any glob. "**" matches across
// directories, "*" within one. A glob matches at any depth of p, so "*.go"
// means any Go file and "src/*.go" any Go file directly under a src directory.
func pathGlobAny(globs []string, p string) bool {
	p = filepath.ToSlash(p)
	for _, g := range globs {
		if re, err := globRegexp(g); err == nil && re.MatchString(p) {
			return true
		}
	}
	return false
}

func globRegexp(g string) (*regexp.Regexp, error) {
	var sb strings.Builder
	sb.WriteString(`(?s)^(?:.*/)?`)
	g = strings.TrimPrefix(g, "./")
	for i := 0; i < len(g); i++ {
		switch c := g[i]; c {
		case '*':
			if i+1 < len(g) && g[i+1] == '*' {
				i++
				if i+1 < len(g) && g[i+1] == '/' {
					i++
					sb.WriteString(`(?:.*/)?`)
				} else {
					sb.WriteString(`.*`)
				}
			} else {
				sb.WriteString(`[^/]*`)
			}
		case '?':
			sb.WriteString(`[^/]`)
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	sb.WriteString(`$`)
	return regexp.Compile(sb.String())
}
