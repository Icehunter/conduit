package session

import (
	"strings"
	"testing"
)

func TestTitleFromText_StripsSummaryTag(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "summary tag with newlines and trailing text",
			in:   "<summary>\nfoo bar baz\n</summary>\n\nAbove is a summary",
			want: "foo bar baz",
		},
		{
			name: "summary tag inline short",
			in:   "<summary>short</summary>",
			want: "short",
		},
		{
			name: "summary tag not closed",
			in:   "<summary>no close",
			want: "no close",
		},
		{
			name: "normal user message passes through",
			in:   "normal user message",
			want: "normal user message",
		},
		{
			name: "title tag stripped, trailing text returned",
			in:   "<title>some title</title>\nextra",
			want: "extra",
		},
		{
			name: "analysis tag stripped",
			in:   "<analysis>details</analysis>\nreal content",
			want: "real content",
		},
		{
			name: "context tag stripped",
			in:   "<context>ctx</context>\nactual message",
			want: "actual message",
		},
		{
			name: "empty string stays empty",
			in:   "",
			want: "",
		},
		{
			name: "summary wrapping real content uses inner text as title candidate",
			in:   "<summary>\nWe discussed Go error handling patterns\n</summary>\n\nAbove is a summary of our conversation so far.",
			want: "We discussed Go error handling patterns",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := titleFromText(tt.in)
			if tt.want == "" {
				if got != "" {
					t.Errorf("titleFromText(%q) = %q, want empty", tt.in, got)
				}
				return
			}
			if !strings.Contains(got, tt.want) && got != tt.want {
				t.Errorf("titleFromText(%q) = %q, want it to contain or equal %q", tt.in, got, tt.want)
			}
		})
	}
}
