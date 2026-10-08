package skilllint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

const goodSkill = "---\nname: my-skill\ndescription: Use when doing the thing.\n---\n## When to Use\nAlways.\n"

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"valid", goodSkill, ""},
		{"valid bom and crlf", "\ufeff---\r\nname: a\r\ndescription: d\r\n---\r\nbody\r\n", ""},
		{"quoted values", "---\nname: \"a\"\ndescription: 'd'\n---\nbody", ""},
		{"block scalar description", "---\nname: a\ndescription: >\n  folded\n  text\n---\nbody", ""},
		{"empty", "  \n", "empty"},
		{"no frontmatter", "# Title\nbody", "frontmatter"},
		{"unclosed", "---\nname: a\ndescription: d\nbody", "frontmatter"},
		{"missing name", "---\ndescription: d\n---\nbody", "'name'"},
		{"missing description", "---\nname: a\n---\nbody", "'description'"},
		{"empty description", "---\nname: a\ndescription:\n---\nbody", "'description'"},
		{"bad name", "---\nname: Bad Name\ndescription: d\n---\nbody", "name"},
		{"long name", "---\nname: " + strings.Repeat("a", 65) + "\ndescription: d\n---\nbody", "exceeds"},
		{"long description", "---\nname: a\ndescription: " + strings.Repeat("d", 1025) + "\n---\nbody", "limit 1024"},
		{"description at limit", "---\nname: a\ndescription: " + strings.Repeat("d", 1024) + "\n---\nbody", ""},
		{"empty body", "---\nname: a\ndescription: d\n---\n\n", "instructions"},
		{"oversized", "---\nname: a\ndescription: d\n---\n" + strings.Repeat("x", MaxSkillChars), "limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.content)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestParse_BlockScalarFolds(t *testing.T) {
	fm, body, ok := Parse("---\nname: a\ndescription: >\n  folded\n  text\nmetadata:\n  k: v\n---\nbody")
	if !ok || fm["description"] != "folded text" || fm["name"] != "a" || body != "body" {
		t.Fatalf("fm=%v body=%q ok=%v", fm, body, ok)
	}
}

func TestValidSupportPath(t *testing.T) {
	tests := []struct {
		rel string
		ok  bool
	}{
		{"references/a.md", true},
		{"references/sub/a.md", true},
		{"", false},
		{"SKILL.md", false},
		{"scripts/a.sh", false},
		{"references/../SKILL.md", false},
		{"../references/a.md", false},
		{"/references/a.md", false},
		{"references/", false},
		{"references//a.md", false},
		{"./references/a.md", true},
		{"references\\a.md", false},
		{"references/a\x00.md", false},
	}
	for _, tt := range tests {
		t.Run(tt.rel, func(t *testing.T) {
			if err := ValidSupportPath(tt.rel); (err == nil) != tt.ok {
				t.Fatalf("ValidSupportPath(%q) = %v, want ok=%v", tt.rel, err, tt.ok)
			}
		})
	}
}

func rules(fs []Finding) map[string]bool {
	m := map[string]bool{}
	for _, f := range fs {
		m[f.Rule] = true
	}
	return m
}

func TestLint(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-skill")
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "real.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	hdr := "---\nname: my-skill\ndescription: Use when X.\n---\n## When to Use\n"
	tests := []struct {
		name    string
		content string
		dir     string
		want    []string
		notWant []string
	}{
		{"clean", goodSkill, "", nil, []string{"missing-section", "dangling-reference"}},
		{"not frontmatter yields nothing", "# x", "", nil, []string{"missing-section"}},
		{"missing section", "---\nname: a\ndescription: d\n---\nbody", "", []string{"missing-section"}, nil},
		{"long description", "---\nname: a\ndescription: " + strings.Repeat("d", 300) + "\n---\nbody", "", []string{"description-length"}, nil},
		{"marketing", "---\nname: a\ndescription: A powerful and robust tool.\n---\nbody", "", []string{"description-marketing"}, nil},
		{"oversized body", hdr + strings.Repeat("x", BodySoftBudgetChars), "", []string{"oversized-body"}, nil},
		{"incident log", hdr + "See #1234, #2345, #3456, PR 4567 and issue 5678 for why.", "", []string{"incident-log-shape"}, nil},
		{"incident sparse", hdr + "See #1234 once. " + strings.Repeat("prose ", 2000), "", nil, []string{"incident-log-shape"}},
		{"dangling reference", hdr + "See references/missing.md and references/real.md.", dir, []string{"dangling-reference"}, nil},
		{"existing reference ok", hdr + "See references/real.md.", dir, nil, []string{"dangling-reference"}},
		{"reference with trailing period", hdr + "See references/real.md.", dir, nil, []string{"dangling-reference"}},
		{"name mismatch", "---\nname: other\ndescription: d\n---\n## When to Use\n", dir, []string{"name-dir-mismatch"}, nil},
		{"forbidden file", hdr + "x", dir, []string{"forbidden-file"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rules(Lint(tt.content, tt.dir))
			for _, r := range tt.want {
				if !got[r] {
					t.Errorf("missing finding %q; got %v", r, got)
				}
			}
			for _, r := range tt.notWant {
				if got[r] {
					t.Errorf("unexpected finding %q", r)
				}
			}
		})
	}
}

func TestLint_ReferencesSprawl(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	refs := filepath.Join(dir, "references")
	if err := os.MkdirAll(refs, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= MaxReferenceFiles; i++ {
		if err := os.WriteFile(filepath.Join(refs, strings.Repeat("a", i+1)+".md"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !rules(Lint(goodSkill, dir))["references-sprawl"] {
		t.Fatal("expected references-sprawl")
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{goodSkill, "---\n", "---\n---\n", "---\nname:\n---\nb", "\ufeff---\r\na: b\r\n---", "---\nk: |\n  x\n---\n", ": :\n---"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		fm, body, ok := Parse(s)
		if !ok && fm != nil {
			t.Fatal("fm set without ok")
		}
		if ok && fm == nil {
			t.Fatal("ok without fm map")
		}
		if utf8.ValidString(s) && !utf8.ValidString(body) {
			t.Fatalf("body not valid UTF-8 for valid input")
		}
		_ = Validate(s)
		_ = Lint(s, "")
	})
}
