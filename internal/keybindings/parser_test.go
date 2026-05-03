package keybindings

import "testing"

func TestParseKeystroke_PlainKey(t *testing.T) {
	got := ParseKeystroke("a")
	want := Keystroke{Key: "a"}
	if got != want {
		t.Errorf("ParseKeystroke(\"a\") = %+v, want %+v", got, want)
	}
}

func TestParseKeystroke_CtrlAlphabet(t *testing.T) {
	got := ParseKeystroke("ctrl+c")
	want := Keystroke{Key: "c", Ctrl: true}
	if got != want {
		t.Errorf("ParseKeystroke(\"ctrl+c\") = %+v, want %+v", got, want)
	}
}

func TestParseKeystroke_ModifierAliases(t *testing.T) {
	cases := []struct {
		in   string
		want Keystroke
	}{
		{"ctrl+x", Keystroke{Key: "x", Ctrl: true}},
		{"control+x", Keystroke{Key: "x", Ctrl: true}},
		{"alt+x", Keystroke{Key: "x", Alt: true}},
		{"opt+x", Keystroke{Key: "x", Alt: true}},
		{"option+x", Keystroke{Key: "x", Alt: true}},
		{"meta+x", Keystroke{Key: "x", Alt: true}},
		{"shift+x", Keystroke{Key: "x", Shift: true}},
		{"cmd+x", Keystroke{Key: "x", Super: true}},
		{"command+x", Keystroke{Key: "x", Super: true}},
		{"super+x", Keystroke{Key: "x", Super: true}},
	}
	for _, c := range cases {
		got := ParseKeystroke(c.in)
		if got != c.want {
			t.Errorf("ParseKeystroke(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseKeystroke_SpecialNames(t *testing.T) {
	cases := map[string]string{
		"esc":    "escape",
		"return": "enter",
		"space":  "space",
		"↑":      "up",
		"↓":      "down",
		"←":      "left",
		"→":      "right",
	}
	for in, wantKey := range cases {
		got := ParseKeystroke(in)
		if got.Key != wantKey {
			t.Errorf("ParseKeystroke(%q).Key = %q, want %q", in, got.Key, wantKey)
		}
	}
}

func TestParseKeystroke_MultipleModifiers(t *testing.T) {
	got := ParseKeystroke("ctrl+shift+k")
	want := Keystroke{Key: "k", Ctrl: true, Shift: true}
	if got != want {
		t.Errorf("ParseKeystroke(\"ctrl+shift+k\") = %+v, want %+v", got, want)
	}
}

func TestKeystrokeString_RoundTrips(t *testing.T) {
	inputs := []string{
		"ctrl+c",
		"ctrl+shift+k",
		"alt+enter",
		"escape",
		"a",
	}
	for _, in := range inputs {
		ks := ParseKeystroke(in)
		s := ks.String()
		ks2 := ParseKeystroke(s)
		if ks != ks2 {
			t.Errorf("round-trip %q: %+v -> %q -> %+v", in, ks, s, ks2)
		}
	}
}

func TestParseBlocks_FlattensAndPreservesOrder(t *testing.T) {
	blocks := []Block{
		{
			Context:  "Global",
			Bindings: map[string]string{"ctrl+c": "app:interrupt"},
		},
		{
			Context:  "Chat",
			Bindings: map[string]string{"enter": "chat:submit"},
		},
	}
	got := ParseBlocks(blocks)
	if len(got) != 2 {
		t.Fatalf("expected 2 bindings, got %d", len(got))
	}
	// Order between blocks is preserved; within a block map iteration is random,
	// but each block here only has one entry so we can assert.
	if got[0].Context != "Global" || got[0].Action != "app:interrupt" {
		t.Errorf("got[0] = %+v", got[0])
	}
	if got[1].Context != "Chat" || got[1].Action != "chat:submit" {
		t.Errorf("got[1] = %+v", got[1])
	}
}

func TestParseBlocks_HandlesUnbinds(t *testing.T) {
	blocks := []Block{
		{Context: "Global", Unbinds: []string{"ctrl+c"}},
	}
	got := ParseBlocks(blocks)
	if len(got) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(got))
	}
	if !got[0].Unbind {
		t.Errorf("expected Unbind=true, got %+v", got[0])
	}
	if got[0].Action != "" {
		t.Errorf("unbind should have empty action, got %q", got[0].Action)
	}
}
