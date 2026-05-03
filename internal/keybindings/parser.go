package keybindings

import "strings"

// ParseKeystroke parses "ctrl+shift+k" into a Keystroke. Modifier aliases:
//   - ctrl, control
//   - alt, opt, option, meta   (all collapse to Alt — terminals can't
//     distinguish alt from meta on legacy keymaps)
//   - shift
//   - cmd, command, super, win (collapse to Super — only fires on
//     terminals that emit kitty keyboard protocol)
//
// Special key names: esc → escape, return → enter, space → " " (the
// canonical key name for spacebar in bubbletea v2 is "space", but we
// accept both).
func ParseKeystroke(input string) Keystroke {
	parts := strings.Split(input, "+")
	ks := Keystroke{}
	for _, part := range parts {
		switch strings.ToLower(part) {
		case "ctrl", "control":
			ks.Ctrl = true
		case "alt", "opt", "option", "meta":
			ks.Alt = true
		case "shift":
			ks.Shift = true
		case "cmd", "command", "super", "win":
			ks.Super = true
		case "esc":
			ks.Key = "escape"
		case "return":
			ks.Key = "enter"
		case "space":
			ks.Key = "space"
		case "↑":
			ks.Key = "up"
		case "↓":
			ks.Key = "down"
		case "←":
			ks.Key = "left"
		case "→":
			ks.Key = "right"
		default:
			ks.Key = strings.ToLower(part)
		}
	}
	return ks
}

// String renders a Keystroke back to its canonical string form, suitable
// for round-tripping through ParseKeystroke. Used in tests and for
// "current binding for action X" display in /help-style output.
func (k Keystroke) String() string {
	var parts []string
	if k.Ctrl {
		parts = append(parts, "ctrl")
	}
	if k.Alt {
		parts = append(parts, "alt")
	}
	if k.Shift {
		parts = append(parts, "shift")
	}
	if k.Super {
		parts = append(parts, "cmd")
	}
	parts = append(parts, k.Key)
	return strings.Join(parts, "+")
}

// ParseBlocks converts loaded JSON blocks into a flat []Binding list.
// The order is preserved, which matters: when multiple bindings match
// (e.g., user overrides default), the resolver picks the last one.
func ParseBlocks(blocks []Block) []Binding {
	out := make([]Binding, 0, len(blocks)*4)
	for _, blk := range blocks {
		for keystr, action := range blk.Bindings {
			out = append(out, Binding{
				Keystroke: ParseKeystroke(keystr),
				Action:    action,
				Context:   blk.Context,
			})
		}
		for _, keystr := range blk.Unbinds {
			out = append(out, Binding{
				Keystroke: ParseKeystroke(keystr),
				Context:   blk.Context,
				Unbind:    true,
			})
		}
	}
	return out
}
