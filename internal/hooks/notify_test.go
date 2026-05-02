package hooks

import "testing"

func TestEscapeAppleScript(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{`hello`, `hello`},
		{`say "hi"`, `say \"hi\"`},
		{`back\slash`, `back\\slash`},
		{`it's fine`, `it's fine`},
	}
	for _, tt := range tests {
		got := escapeAppleScript(tt.in)
		if got != tt.want {
			t.Errorf("escapeAppleScript(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func TestNotify_DoesNotPanic(t *testing.T) {
	// Notify should never panic even when osascript/notify-send are absent.
	Notify("test title", `test body with "quotes" and \ backslash`)
}
