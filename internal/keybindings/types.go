// Package keybindings ports the user-customizable keybinding layer from
// src/keybindings/ in CC. MVP: single-keystroke bindings, JSON config in
// ~/.claude/keybindings.json, resolved against bubbletea v2 KeyPressMsg
// (which exposes structured Code + Mod after the M-P upgrade).
//
// Chord support (multi-keystroke like "ctrl+x ctrl+s") is intentionally
// deferred — CC source has it but the vast majority of CC's own
// defaultBindings.ts entries are single-keystroke, and chord state machines
// are easy to get wrong. We can add it later behind the same Resolver API.
package keybindings

// Keystroke is one parsed key chord — a key name plus modifiers.
// The key field is the lowercased canonical name: "a"-"z", "0"-"9",
// "enter", "escape", "tab", "space", "backspace", "delete", "up", "down",
// "left", "right", "pageup", "pagedown", "home", "end", or single
// printable characters like "/", "?".
type Keystroke struct {
	Key   string
	Ctrl  bool
	Alt   bool
	Shift bool
	Super bool
}

// Binding pairs a Keystroke with the action it triggers, scoped to a
// context. Action is empty when the user explicitly unbinds a default
// (Unbind == true).
type Binding struct {
	Keystroke Keystroke
	Action    string
	Context   string
	Unbind    bool
}

// Block is one entry in keybindings.json — a context plus a map of
// keystroke string → action (or null to unbind).
type Block struct {
	Context  string            `json:"context"`
	Bindings map[string]string `json:"bindings"`
	// Unbinds tracks keys explicitly set to null in JSON; we can't put
	// `null` in a map[string]string, so the loader splits these out.
	Unbinds []string `json:"-"`
}

// File is the top-level keybindings.json shape.
type File struct {
	Schema   string  `json:"$schema,omitempty"`
	Docs     string  `json:"$docs,omitempty"`
	Bindings []Block `json:"bindings"`
}
