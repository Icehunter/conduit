package shellsafe

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// SplitForPermissions decomposes cmd into the individual commands it would
// actually run, so a permission rule can be matched against each one instead of
// against the raw string.
//
// This exists because prefix matching on the whole command string is not a
// security boundary: `echo hi && curl evil` starts with "echo", so a deny rule
// for curl never fires and an allow rule for echo approves the whole thing.
// Matching per segment closes both directions.
//
// Segments are rendered from the AST, which also normalizes whitespace — so
// leading spaces can no longer slip a command past a prefix rule.
//
// Commands nested inside substitutions ($(...), `...`, <(...)) are returned in
// addition to their containing command, because they execute too. Redirection
// targets are not segments; they are not executed.
//
// ok is false when cmd is empty or does not parse. Callers must treat that
// conservatively and fall back to matching the raw string rather than
// concluding there is nothing to check.
func SplitForPermissions(cmd string) ([]string, bool) {
	f, ok := parse(cmd)
	if !ok {
		return nil, false
	}

	var segs []string
	seen := make(map[string]struct{})
	add := func(n syntax.Node) {
		s := strings.TrimSpace(render(n))
		if s == "" {
			return
		}
		if _, dup := seen[s]; dup {
			return
		}
		seen[s] = struct{}{}
		segs = append(segs, s)
	}

	syntax.Walk(f, func(n syntax.Node) bool {
		if call, isCall := n.(*syntax.CallExpr); isCall && len(call.Args) > 0 {
			add(call)
		}
		return true
	})

	if len(segs) == 0 {
		return nil, false
	}
	return segs, true
}

// render prints an AST node back to shell source. The printer emits normalized
// spacing, which is what makes whitespace-padded input match a prefix rule.
func render(n syntax.Node) string {
	var sb strings.Builder
	if err := syntax.NewPrinter().Print(&sb, n); err != nil {
		return ""
	}
	return sb.String()
}
