package tool

import (
	"context"
	"testing"
)

func TestResolvePath_Cleans(t *testing.T) {
	ctx := WithCwd(context.Background(), "/work/repo")
	tests := []struct{ in, want string }{
		{"", ""},
		{"src/x.go", "/work/repo/src/x.go"},
		{"g/../x", "/work/repo/x"},
		{"/abs/g/../x", "/abs/x"},
		{"http://host/x", "http://host/x"},
		{"issue:/x/y/2", "issue:/x/y/2"},
		{"./a//b", "/work/repo/a/b"},
	}
	for _, tc := range tests {
		if got := ResolvePath(ctx, tc.in); got != tc.want {
			t.Errorf("ResolvePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
