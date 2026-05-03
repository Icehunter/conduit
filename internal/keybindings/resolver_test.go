package keybindings

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func keyMsg(code rune, mod tea.KeyMod) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Mod: mod}
}

func TestResolver_DefaultsResolveCtrlC(t *testing.T) {
	r := NewResolver(Defaults())
	res := r.Resolve(keyMsg('c', tea.ModCtrl), "Global")
	if !res.Matched || res.Action != "app:interrupt" {
		t.Errorf("ctrl+c in Global = %+v, want app:interrupt", res)
	}
}

func TestResolver_ChatEscapeCancels(t *testing.T) {
	r := NewResolver(Defaults())
	res := r.Resolve(tea.KeyPressMsg{Code: tea.KeyEsc}, "Chat", "Global")
	if !res.Matched || res.Action != "chat:cancel" {
		t.Errorf("escape in Chat = %+v, want chat:cancel", res)
	}
}

func TestResolver_ContextScoping(t *testing.T) {
	// 'y' is bound to confirm:yes in Confirmation, nothing in Chat.
	r := NewResolver(Defaults())
	gotChat := r.Resolve(keyMsg('y', 0), "Chat")
	if gotChat.Matched {
		t.Errorf("'y' should not match in Chat, got %+v", gotChat)
	}
	gotConfirm := r.Resolve(keyMsg('y', 0), "Confirmation")
	if !gotConfirm.Matched || gotConfirm.Action != "confirm:yes" {
		t.Errorf("'y' in Confirmation = %+v, want confirm:yes", gotConfirm)
	}
}

func TestResolver_UserOverrideWins(t *testing.T) {
	bindings := Defaults()
	// User remaps ctrl+c to a custom action.
	bindings = append(bindings, Binding{
		Keystroke: ParseKeystroke("ctrl+c"),
		Action:    "custom:override",
		Context:   "Global",
	})
	r := NewResolver(bindings)
	res := r.Resolve(keyMsg('c', tea.ModCtrl), "Global")
	if res.Action != "custom:override" {
		t.Errorf("user override should win, got %+v", res)
	}
}

func TestResolver_ExplicitUnbindReturnsUnbound(t *testing.T) {
	bindings := Defaults()
	bindings = append(bindings, Binding{
		Keystroke: ParseKeystroke("ctrl+c"),
		Context:   "Global",
		Unbind:    true,
	})
	r := NewResolver(bindings)
	res := r.Resolve(keyMsg('c', tea.ModCtrl), "Global")
	if !res.Matched {
		t.Errorf("unbind should report Matched=true so caller skips default, got %+v", res)
	}
	if !res.Unbound {
		t.Errorf("expected Unbound=true, got %+v", res)
	}
	if res.Action != "" {
		t.Errorf("unbind should have empty action, got %q", res.Action)
	}
}

func TestResolver_NoMatch(t *testing.T) {
	r := NewResolver(Defaults())
	res := r.Resolve(keyMsg('z', 0), "Chat")
	if res.Matched {
		t.Errorf("'z' should not match anywhere, got %+v", res)
	}
}

func TestResolver_ShiftLetterPreservesShift(t *testing.T) {
	// Shift+letter: bubbletea v2 carries Mod=Shift, Code is the lowercase
	// rune (printable). Make sure the shift bit is honored separately so
	// shift+a doesn't match a plain 'a' binding.
	bindings := []Binding{
		{Keystroke: ParseKeystroke("a"), Action: "plain:a", Context: "X"},
		{Keystroke: ParseKeystroke("shift+a"), Action: "shift:a", Context: "X"},
	}
	r := NewResolver(bindings)
	plain := r.Resolve(keyMsg('a', 0), "X")
	if plain.Action != "plain:a" {
		t.Errorf("plain a = %+v, want plain:a", plain)
	}
	shifted := r.Resolve(keyMsg('a', tea.ModShift), "X")
	if shifted.Action != "shift:a" {
		t.Errorf("shift+a = %+v, want shift:a", shifted)
	}
}

func TestResolver_NamedKeys(t *testing.T) {
	bindings := []Binding{
		{Keystroke: ParseKeystroke("up"), Action: "go:up", Context: "X"},
		{Keystroke: ParseKeystroke("space"), Action: "go:space", Context: "X"},
		{Keystroke: ParseKeystroke("tab"), Action: "go:tab", Context: "X"},
	}
	r := NewResolver(bindings)
	tests := []struct {
		code rune
		want string
	}{
		{tea.KeyUp, "go:up"},
		{tea.KeySpace, "go:space"},
		{tea.KeyTab, "go:tab"},
	}
	for _, tc := range tests {
		res := r.Resolve(tea.KeyPressMsg{Code: tc.code}, "X")
		if res.Action != tc.want {
			t.Errorf("Code=%v -> %+v, want action=%q", tc.code, res, tc.want)
		}
	}
}
