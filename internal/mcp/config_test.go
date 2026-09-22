package mcp

import (
	"reflect"
	"testing"
)

func TestExpandEnvVars(t *testing.T) {
	t.Setenv("RTK_TEST_VAR", "value")
	t.Setenv("RTK_TEST_EMPTY", "")

	tests := []struct {
		name        string
		in          string
		want        string
		wantMissing []string
	}{
		{name: "bare var set", in: "$RTK_TEST_VAR", want: "value"},
		{name: "braced var set", in: "${RTK_TEST_VAR}", want: "value"},
		{name: "braced var set ignores default", in: "${RTK_TEST_VAR:-fallback}", want: "value"},
		{name: "braced var unset with default", in: "${RTK_TEST_UNSET:-fallback}", want: "fallback"},
		{name: "braced var empty but set uses empty, not default", in: "${RTK_TEST_EMPTY:-fallback}", want: ""},
		{name: "default containing colon-dash", in: "${RTK_TEST_UNSET:-a:-b}", want: "a:-b"},
		{name: "empty default", in: "${RTK_TEST_UNSET:-}", want: ""},
		{name: "mixed text around", in: "prefix-${RTK_TEST_UNSET:-fallback}-suffix", want: "prefix-fallback-suffix"},
		{name: "no expansion needed", in: "plain string", want: "plain string"},

		// Unresolved references stay verbatim and are reported.
		{
			name:        "braced var unset no default",
			in:          "${RTK_TEST_UNSET}",
			want:        "${RTK_TEST_UNSET}",
			wantMissing: []string{"RTK_TEST_UNSET"},
		},
		{
			name:        "bare var unset",
			in:          "--token=$RTK_TEST_UNSET",
			want:        "--token=$RTK_TEST_UNSET",
			wantMissing: []string{"RTK_TEST_UNSET"},
		},
		{
			name:        "several unset are all reported",
			in:          "${RTK_A} ${RTK_B}",
			want:        "${RTK_A} ${RTK_B}",
			wantMissing: []string{"RTK_A", "RTK_B"},
		},
		{
			name:        "resolved and unresolved mixed",
			in:          "${RTK_TEST_VAR}:${RTK_TEST_UNSET}",
			want:        "value:${RTK_TEST_UNSET}",
			wantMissing: []string{"RTK_TEST_UNSET"},
		},

		// Things that look like references but aren't.
		{name: "trailing dollar", in: "cost$", want: "cost$"},
		{name: "double dollar", in: "$$", want: "$$"},
		{name: "dollar before punctuation", in: "$-x", want: "$-x"},
		{name: "empty braces", in: "${}", want: "${}"},
		{name: "unterminated brace", in: "${RTK_TEST_VAR", want: "${RTK_TEST_VAR"},
		{name: "bare name stops at punctuation", in: "$RTK_TEST_VAR/sub", want: "value/sub"},
		{name: "digits allowed after first char", in: "${RTK_TEST_VAR}2", want: "value2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, missing := expandEnvVars(tt.in)
			if got != tt.want {
				t.Errorf("expandEnvVars(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if len(missing) == 0 && len(tt.wantMissing) == 0 {
				return
			}
			if !reflect.DeepEqual(missing, tt.wantMissing) {
				t.Errorf("expandEnvVars(%q) missing = %v, want %v", tt.in, missing, tt.wantMissing)
			}
		})
	}
}

func TestExpandEnvDiscardsMissing(t *testing.T) {
	t.Setenv("RTK_TEST_VAR", "value")
	if got := expandEnv("${RTK_TEST_VAR}/${RTK_TEST_UNSET}"); got != "value/${RTK_TEST_UNSET}" {
		t.Errorf("expandEnv = %q, want %q", got, "value/${RTK_TEST_UNSET}")
	}
}

func FuzzExpandEnvVars(f *testing.F) {
	for _, seed := range []string{
		"", "$", "$$", "${}", "${A}", "${A:-b}", "$A", "plain", "${A", "a$b${c:-d}e",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// No panics, and every reported name must be non-empty.
		_, missing := expandEnvVars(s)
		for _, name := range missing {
			if name == "" {
				t.Fatalf("expandEnvVars(%q) reported an empty variable name", s)
			}
		}
	})
}
