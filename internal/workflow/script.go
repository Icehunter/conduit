// Package workflow runs JavaScript workflow scripts that orchestrate many
// sub-agents deterministically. The script API (agent/parallel/pipeline/
// phase/log/budget/args/workflow) matches Claude Code's Workflow tool so
// saved workflows under ~/.claude/workflows/ run unchanged.
package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/dop251/goja"
	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/file"
	"github.com/dop251/goja/parser"
)

// Phase is one entry of meta.phases.
type Phase struct {
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Model  string `json:"model,omitempty"`
}

// Meta is the statically-extracted `export const meta = {...}` literal.
type Meta struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	WhenToUse   string  `json:"whenToUse,omitempty"`
	Phases      []Phase `json:"phases,omitempty"`
}

// Script is a parsed, validated workflow ready to run.
type Script struct {
	Meta Meta
	// Source is the original script text.
	Source string
	// Hash identifies the script version for journal keys.
	Hash string
	// program is the compiled async IIFE wrapping the body.
	program *goja.Program
}

var (
	reExportMeta = regexp.MustCompile(`(?m)^\s*export\s+const\s+meta\s*=`)
	reOtherESM   = regexp.MustCompile(`(?m)^\s*(export|import)\s`)
)

// ErrNoMeta is returned when the script does not start with `export const meta`.
var ErrNoMeta = errors.New("workflow: script must begin with `export const meta = {...}`")

// Load parses src, extracts and validates meta, rejects nondeterministic
// calls, and compiles the body.
func Load(src string) (*Script, error) {
	loc := reExportMeta.FindStringIndex(src)
	if loc == nil {
		return nil, ErrNoMeta
	}
	stripped := src[:loc[0]] + strings.Replace(src[loc[0]:loc[1]], "export ", "", 1) + src[loc[1]:]
	if m := reOtherESM.FindStringIndex(stripped); m != nil {
		return nil, fmt.Errorf("workflow: scripts are plain JavaScript, not ES modules: unexpected %q", strings.TrimSpace(stripped[m[0]:m[1]]))
	}

	wrapped := wrapBody(stripped)
	prog, err := parser.ParseFile(nil, "workflow.js", wrapped, 0)
	if err != nil {
		return nil, fmt.Errorf("workflow: parse: %w", err)
	}
	metaLit, err := findMetaLiteral(prog)
	if err != nil {
		return nil, err
	}
	if err := checkPureLiteral(metaLit); err != nil {
		return nil, fmt.Errorf("workflow: meta must be a pure literal (no variables, calls, spreads or interpolation): %w", err)
	}
	if err := checkDeterministic(prog); err != nil {
		return nil, err
	}

	metaSrc := wrapped[int(metaLit.Idx0())-1 : int(metaLit.Idx1())-1]
	meta, err := evalMeta(metaSrc)
	if err != nil {
		return nil, err
	}
	if meta.Name == "" || meta.Description == "" {
		return nil, errors.New("workflow: meta.name and meta.description are required")
	}

	compiled, err := goja.Compile("workflow.js", wrapped, true)
	if err != nil {
		return nil, fmt.Errorf("workflow: compile: %w", err)
	}
	sum := sha256.Sum256([]byte(src))
	return &Script{
		Meta:    meta,
		Source:  src,
		Hash:    hex.EncodeToString(sum[:]),
		program: compiled,
	}, nil
}

// wrapBody turns a top-level-await script into an expression that evaluates
// to a Promise of the script's return value.
func wrapBody(body string) string {
	return "(async () => {\n" + body + "\n})()"
}

// findMetaLiteral walks (async () => { const meta = {...}; ... })() and
// returns the object literal bound to meta, which must be the first statement.
func findMetaLiteral(prog *ast.Program) (*ast.ObjectLiteral, error) {
	if len(prog.Body) != 1 {
		return nil, ErrNoMeta
	}
	es, ok := prog.Body[0].(*ast.ExpressionStatement)
	if !ok {
		return nil, ErrNoMeta
	}
	call, ok := es.Expression.(*ast.CallExpression)
	if !ok {
		return nil, ErrNoMeta
	}
	fn, ok := call.Callee.(*ast.ArrowFunctionLiteral)
	if !ok {
		return nil, ErrNoMeta
	}
	block, ok := fn.Body.(*ast.BlockStatement)
	if !ok || len(block.List) == 0 {
		return nil, ErrNoMeta
	}
	decl, ok := block.List[0].(*ast.LexicalDeclaration)
	if !ok || len(decl.List) != 1 {
		return nil, ErrNoMeta
	}
	id, ok := decl.List[0].Target.(*ast.Identifier)
	if !ok || id.Name != "meta" {
		return nil, ErrNoMeta
	}
	lit, ok := decl.List[0].Initializer.(*ast.ObjectLiteral)
	if !ok {
		return nil, errors.New("workflow: meta must be an object literal")
	}
	return lit, nil
}

func checkPureLiteral(e ast.Expression) error {
	switch n := e.(type) {
	case *ast.StringLiteral, *ast.NumberLiteral, *ast.BooleanLiteral, *ast.NullLiteral:
		return nil
	case *ast.TemplateLiteral:
		if n.Tag != nil || len(n.Expressions) > 0 {
			return errors.New("template interpolation")
		}
		return nil
	case *ast.UnaryExpression:
		if _, ok := n.Operand.(*ast.NumberLiteral); ok && (n.Operator.String() == "-" || n.Operator.String() == "+") {
			return nil
		}
		return fmt.Errorf("unary %s", n.Operator)
	case *ast.ArrayLiteral:
		for _, v := range n.Value {
			if err := checkPureLiteral(v); err != nil {
				return err
			}
		}
		return nil
	case *ast.ObjectLiteral:
		for _, p := range n.Value {
			kv, ok := p.(*ast.PropertyKeyed)
			if !ok {
				return errors.New("shorthand or spread property")
			}
			if kv.Computed || kv.Kind != ast.PropertyKindValue {
				return errors.New("computed key or accessor")
			}
			switch kv.Key.(type) {
			case *ast.StringLiteral, *ast.Identifier, *ast.NumberLiteral:
			default:
				return errors.New("non-literal key")
			}
			if err := checkPureLiteral(kv.Value); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("%T", e)
	}
}

// evalMeta evaluates the literal in a bare VM and decodes it.
func evalMeta(src string) (Meta, error) {
	vm := goja.New()
	v, err := vm.RunString("(" + src + ")")
	if err != nil {
		return Meta{}, fmt.Errorf("workflow: meta: %w", err)
	}
	raw, err := json.Marshal(v.Export())
	if err != nil {
		return Meta{}, fmt.Errorf("workflow: meta: %w", err)
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return Meta{}, fmt.Errorf("workflow: meta: %w", err)
	}
	return m, nil
}

// ErrNondeterministic is wrapped by Load when the script calls Date.now(),
// Math.random() or argless new Date().
var ErrNondeterministic = errors.New("workflow: scripts must be deterministic: Date.now()/Math.random()/new Date() are unavailable (they break resume); pass timestamps via args or stamp results after the workflow returns")

func checkDeterministic(prog *ast.Program) error {
	var found string
	walk(prog, func(n ast.Node) bool {
		if found != "" {
			return false
		}
		switch x := n.(type) {
		case *ast.CallExpression:
			if dot, ok := x.Callee.(*ast.DotExpression); ok {
				if id, ok := dot.Left.(*ast.Identifier); ok {
					if id.Name == "Date" && dot.Identifier.Name == "now" {
						found = "Date.now()"
					}
					if id.Name == "Math" && dot.Identifier.Name == "random" {
						found = "Math.random()"
					}
				}
			}
		case *ast.NewExpression:
			if id, ok := x.Callee.(*ast.Identifier); ok && id.Name == "Date" && len(x.ArgumentList) == 0 {
				found = "new Date()"
			}
		}
		return true
	})
	if found != "" {
		return fmt.Errorf("%w: found %s", ErrNondeterministic, found)
	}
	return nil
}

// idxSpan is implemented by every ast node with a source range.
type idxSpan interface {
	Idx0() file.Idx
	Idx1() file.Idx
}

var _ idxSpan = (*ast.ObjectLiteral)(nil)
