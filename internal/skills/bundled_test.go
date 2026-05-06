package skills

import (
	"strings"
	"testing"
)

func TestBundled_ContainsExpectedSkills(t *testing.T) {
	skills := Bundled()
	if len(skills) == 0 {
		t.Fatal("Bundled() returned empty list")
	}

	names := map[string]bool{}
	for _, s := range skills {
		names[s.QualifiedName] = true
		if s.Description == "" {
			t.Errorf("skill %q has empty description", s.QualifiedName)
		}
		if strings.TrimSpace(s.Body) == "" {
			t.Errorf("skill %q has empty body", s.QualifiedName)
		}
	}

	for _, want := range []string{"simplify", "remember"} {
		if !names[want] {
			t.Errorf("built-in skill %q not found in Bundled()", want)
		}
	}
}
