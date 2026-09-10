package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const minimalScript = `export const meta = {
  name: 'noop',
  description: 'does nothing',
  phases: [{ title: 'One', detail: 'first' }],
}
return 42
`

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		src     string
		wantErr error
		check   func(t *testing.T, s *Script)
	}{
		{
			name: "minimal",
			src:  minimalScript,
			check: func(t *testing.T, s *Script) {
				if s.Meta.Name != "noop" || s.Meta.Description != "does nothing" {
					t.Errorf("meta = %+v", s.Meta)
				}
				if len(s.Meta.Phases) != 1 || s.Meta.Phases[0].Title != "One" || s.Meta.Phases[0].Detail != "first" {
					t.Errorf("phases = %+v", s.Meta.Phases)
				}
				if s.Hash == "" || s.Source != minimalScript {
					t.Error("hash/source not recorded")
				}
			},
		},
		{
			name: "leading comment before meta",
			src:  "// a comment\n\n" + minimalScript,
		},
		{
			name: "top-level await and destructuring parse",
			src: `export const meta = { name: 'x', description: 'y' }
const { a } = await agent('p', { schema: { type: 'object' } })
const out = await parallel([1, 2].map((n) => () => agent(` + "`n=${n}`" + `)))
return out?.length ?? 0
`,
		},
		{
			name:    "missing meta",
			src:     "return 1\n",
			wantErr: ErrNoMeta,
		},
		{
			name:    "meta not first statement",
			src:     "const x = 1\nexport const meta = { name: 'x', description: 'y' }\n",
			wantErr: ErrNoMeta,
		},
		{
			name: "meta with interpolation is rejected",
			src:  "const v = 'x'\nexport const meta = { name: `${v}`, description: 'y' }\n",
			// `const v` precedes meta, so this trips the "first statement" rule first.
			wantErr: ErrNoMeta,
		},
		{
			name:    "meta with template interpolation",
			src:     "export const meta = { name: `${1}`, description: 'y' }\n",
			wantErr: errPureLiteral,
		},
		{
			name:    "meta with call",
			src:     "export const meta = { name: String(1), description: 'y' }\n",
			wantErr: errPureLiteral,
		},
		{
			name:    "meta with spread",
			src:     "export const meta = { ...{}, name: 'x', description: 'y' }\n",
			wantErr: errPureLiteral,
		},
		{
			name:    "meta with shorthand",
			src:     "export const meta = { name, description: 'y' }\n",
			wantErr: errPureLiteral,
		},
		{
			name:    "meta missing required fields",
			src:     "export const meta = { name: 'x' }\n",
			wantErr: errMetaRequired,
		},
		{
			name:    "other ESM syntax",
			src:     "import x from 'y'\nexport const meta = { name: 'x', description: 'y' }\n",
			wantErr: errESM,
		},
		{
			name:    "Date.now",
			src:     "export const meta = { name: 'x', description: 'y' }\nconst t = Date.now()\n",
			wantErr: ErrNondeterministic,
		},
		{
			name:    "Math.random",
			src:     "export const meta = { name: 'x', description: 'y' }\nconst r = [1,2].sort(() => Math.random() - 0.5)\n",
			wantErr: ErrNondeterministic,
		},
		{
			name:    "argless new Date",
			src:     "export const meta = { name: 'x', description: 'y' }\nconst d = new Date()\n",
			wantErr: ErrNondeterministic,
		},
		{
			name: "new Date with args is allowed",
			src:  "export const meta = { name: 'x', description: 'y' }\nconst d = new Date(args.ts)\nreturn d.getTime()\n",
		},
		{
			name:    "syntax error",
			src:     "export const meta = { name: 'x', description: 'y' }\nconst : string = 1\n",
			wantErr: errParse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := Load(tt.src)
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("Load() = ok, want error %v", tt.wantErr)
				}
				if !errors.Is(err, tt.wantErr) && !strings.Contains(err.Error(), tt.wantErr.Error()) {
					t.Fatalf("Load() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if tt.check != nil {
				tt.check(t, s)
			}
		})
	}
}

// Sentinels for substring matching in the table above; Load wraps these
// messages rather than exporting every one as a package-level error.
var (
	errPureLiteral  = errors.New("meta must be a pure literal")
	errMetaRequired = errors.New("meta.name and meta.description are required")
	errESM          = errors.New("not ES modules")
	errParse        = errors.New("workflow: parse")
)

// TestLoad_GraphEngScripts loads the real graph-eng workflows when the repo
// is checked out next to conduit; skipped otherwise.
func TestLoad_GraphEngScripts(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "graph-eng", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("graph-eng not available: %v", err)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".js") {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			s, err := Load(string(src))
			if err != nil {
				t.Fatalf("Load(%s) error = %v", e.Name(), err)
			}
			if s.Meta.Name == "" || len(s.Meta.Phases) == 0 {
				t.Errorf("meta = %+v", s.Meta)
			}
		})
	}
}

func FuzzLoad(f *testing.F) {
	f.Add(minimalScript)
	f.Add("export const meta = { name: 'x', description: 'y' }\nreturn args\n")
	f.Add("return 1")
	f.Add("")
	f.Fuzz(func(t *testing.T, src string) {
		_, _ = Load(src)
	})
}
