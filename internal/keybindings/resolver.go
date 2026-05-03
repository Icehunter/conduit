package keybindings

import tea "charm.land/bubbletea/v2"

// Resolver matches incoming KeyPressMsg events against parsed bindings,
// scoped by active contexts (e.g. ["Chat", "Global"]).
type Resolver struct {
	bindings []Binding
}

// NewResolver wraps a flat binding list. Bindings later in the slice
// shadow earlier ones for the same keystroke+context — that's how user
// overrides win over defaults.
func NewResolver(bindings []Binding) *Resolver {
	return &Resolver{bindings: bindings}
}

// Result is the outcome of resolving a key press.
type Result struct {
	// Action is set when a non-unbind binding matched. Empty otherwise.
	Action string
	// Unbound is true when the matching binding was an explicit null —
	// the caller should NOT fall through to default behavior. This is how
	// a user disables a built-in shortcut.
	Unbound bool
	// Matched is true when any binding (action or unbind) matched.
	Matched bool
}

// Resolve looks up the action bound to msg in the given contexts.
// activeContexts is processed as a set; order doesn't matter.
//
// Returns the LAST matching binding so user-supplied overrides shadow
// the defaults, mirroring src/keybindings/resolver.ts.
func (r *Resolver) Resolve(msg tea.KeyPressMsg, activeContexts ...string) Result {
	target := keystrokeFromMsg(msg)
	if target.Key == "" {
		return Result{}
	}
	ctxOK := func(c string) bool {
		for _, a := range activeContexts {
			if a == c {
				return true
			}
		}
		return false
	}

	var match *Binding
	for i := range r.bindings {
		b := &r.bindings[i]
		if !ctxOK(b.Context) {
			continue
		}
		if !keystrokeMatches(b.Keystroke, target) {
			continue
		}
		match = b
	}
	if match == nil {
		return Result{}
	}
	if match.Unbind {
		return Result{Matched: true, Unbound: true}
	}
	return Result{Matched: true, Action: match.Action}
}

// keystrokeFromMsg extracts the canonical Keystroke from a bubbletea v2
// KeyPressMsg. Bubbletea v2's Key has a structured Code (rune or named
// special key) plus Mod bitfield, so we don't have to parse strings.
func keystrokeFromMsg(msg tea.KeyPressMsg) Keystroke {
	k := tea.Key(msg)
	ks := Keystroke{
		Ctrl:  k.Mod.Contains(tea.ModCtrl),
		Alt:   k.Mod.Contains(tea.ModAlt),
		Shift: k.Mod.Contains(tea.ModShift),
		Super: k.Mod.Contains(tea.ModSuper),
	}
	switch k.Code {
	case tea.KeyEnter:
		ks.Key = "enter"
	case tea.KeyEsc:
		ks.Key = "escape"
	case tea.KeyTab:
		ks.Key = "tab"
	case tea.KeyBackspace:
		ks.Key = "backspace"
	case tea.KeyDelete:
		ks.Key = "delete"
	case tea.KeyUp:
		ks.Key = "up"
	case tea.KeyDown:
		ks.Key = "down"
	case tea.KeyLeft:
		ks.Key = "left"
	case tea.KeyRight:
		ks.Key = "right"
	case tea.KeyPgUp:
		ks.Key = "pageup"
	case tea.KeyPgDown:
		ks.Key = "pagedown"
	case tea.KeyHome:
		ks.Key = "home"
	case tea.KeyEnd:
		ks.Key = "end"
	case tea.KeySpace:
		ks.Key = "space"
	default:
		// Printable runes — lowercase to match parser's normalization.
		// Shift+letter still carries Mod.Contains(ModShift), so the
		// Shift bit is preserved separately even though the rune is
		// already lowercase.
		if k.Code >= 0x20 && k.Code < 0x7f {
			r := k.Code
			if r >= 'A' && r <= 'Z' {
				r += 32
			}
			ks.Key = string(r)
		}
	}
	return ks
}

// keystrokeMatches compares a parsed binding against the keystroke
// observed at runtime. Alt and Meta in the parser collapse to Alt; on
// legacy terminals that's all we get.
func keystrokeMatches(want, got Keystroke) bool {
	return want.Key == got.Key &&
		want.Ctrl == got.Ctrl &&
		want.Shift == got.Shift &&
		want.Alt == got.Alt &&
		want.Super == got.Super
}
