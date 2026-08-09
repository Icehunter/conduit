package shellsafe

import (
	"reflect"
	"testing"
)

func TestSplitForPermissions(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
		want []string
		ok   bool
	}{
		{"simple", "curl https://evil.example", []string{"curl https://evil.example"}, true},
		{"leading whitespace normalized", "   curl   https://evil.example", []string{"curl https://evil.example"}, true},
		{"and", "echo hi && curl https://evil.example", []string{"echo hi", "curl https://evil.example"}, true},
		{"or", "true || curl x", []string{"true", "curl x"}, true},
		{"semicolon", "echo hi; curl x", []string{"echo hi", "curl x"}, true},
		{"pipe", "echo hi | curl x", []string{"echo hi", "curl x"}, true},
		{"pipe-all", "echo hi |& curl x", []string{"echo hi", "curl x"}, true},
		{"background", "curl x &", []string{"curl x"}, true},
		{"three stages", "a | b && c", []string{"a", "b", "c"}, true},
		{"command substitution is a segment", "echo $(curl x)", []string{"echo $(curl x)", "curl x"}, true},
		// Backticks are canonicalized to $(...) by the printer. That is wanted: a
		// rule written either way matches both spellings.
		{"backtick substitution", "echo `curl x`", []string{"echo $(curl x)", "curl x"}, true},
		{"process substitution", "diff <(curl a) <(curl b)", []string{"diff <(curl a) <(curl b)", "curl a", "curl b"}, true},
		{"subshell", "(curl x)", []string{"curl x"}, true},
		{"braces", "{ curl x; }", []string{"curl x"}, true},
		{"if body", "if true; then curl x; fi", []string{"true", "curl x"}, true},
		{"for body", "for f in a b; do curl $f; done", []string{"curl $f"}, true},
		{"while body", "while true; do curl x; done", []string{"true", "curl x"}, true},
		{"redirect is not a segment", "curl x > /tmp/out", []string{"curl x"}, true},
		{"quotes preserved", `git commit -m "hello  world"`, []string{`git commit -m "hello  world"`}, true},
		{"parse failure", "curl 'unterminated", nil, false},
		{"empty", "", nil, false},
		{"whitespace only", "   ", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := SplitForPermissions(tt.cmd)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v (got %q)", ok, tt.ok, got)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SplitForPermissions(%q)\n got = %q\nwant = %q", tt.cmd, got, tt.want)
			}
		})
	}
}

func FuzzSplitForPermissions(f *testing.F) {
	for _, s := range []string{"a && b", "echo $(x)", "curl 'x", "", "a|b|c"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		// Must never panic, and must never return ok with an empty segment.
		segs, ok := SplitForPermissions(cmd)
		if !ok {
			return
		}
		for _, s := range segs {
			if s == "" {
				t.Fatalf("SplitForPermissions(%q) returned an empty segment", cmd)
			}
		}
	})
}
