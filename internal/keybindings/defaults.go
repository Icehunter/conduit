package keybindings

// DefaultBlocks ports the subset of src/keybindings/defaultBindings.ts
// that conduit's TUI actually has handlers for. The set is intentionally
// narrower than CC: features descoped in the parity plan (KAIROS Brief,
// teammate preview, voice, message actions, transcript pager, attachments,
// diff dialog, scroll context) don't appear here. We add them back as
// those features land.
//
// Order matters: later blocks shadow earlier ones for the same keystroke
// in the same context. Within a block, map iteration order is undefined,
// but each block's bindings are disjoint by construction so it doesn't
// matter.
func DefaultBlocks() []Block {
	return []Block{
		{
			Context: "Global",
			Bindings: map[string]string{
				"ctrl+c": "app:interrupt",
				"ctrl+d": "app:exit",
				"ctrl+l": "app:redraw",
				"ctrl+t": "app:toggleTodos",
				"ctrl+o": "app:toggleTranscript",
				"ctrl+r": "history:search",
			},
		},
		{
			Context: "Chat",
			Bindings: map[string]string{
				"escape":    "chat:cancel",
				"shift+tab": "chat:cycleMode",
				"ctrl+p":    "chat:commandPicker",
				"ctrl+m":    "chat:modelPicker",
				"meta+p":    "chat:modelPicker",
				"meta+o":    "chat:fastMode",
				"meta+t":    "chat:thinkingToggle",
				"enter":     "chat:submit",
				"up":        "history:previous",
				"down":      "history:next",
				"ctrl+s":    "chat:stash",
				"ctrl+v":    "chat:imagePaste",
			},
		},
		{
			Context: "Select",
			Bindings: map[string]string{
				"up":     "select:previous",
				"down":   "select:next",
				"j":      "select:next",
				"k":      "select:previous",
				"ctrl+n": "select:next",
				"ctrl+p": "select:previous",
				"enter":  "select:accept",
				"escape": "select:cancel",
			},
		},
		{
			Context: "Confirmation",
			Bindings: map[string]string{
				"y":         "confirm:yes",
				"n":         "confirm:no",
				"enter":     "confirm:yes",
				"escape":    "confirm:no",
				"up":        "confirm:previous",
				"down":      "confirm:next",
				"tab":       "confirm:nextField",
				"space":     "confirm:toggle",
				"shift+tab": "confirm:cycleMode",
			},
		},
		{
			Context: "Settings",
			Bindings: map[string]string{
				"escape": "confirm:no",
				"up":     "select:previous",
				"down":   "select:next",
				"k":      "select:previous",
				"j":      "select:next",
				"space":  "select:accept",
				"enter":  "settings:close",
				"/":      "settings:search",
			},
		},
		{
			Context: "Plugin",
			Bindings: map[string]string{
				"space": "plugin:toggle",
				"i":     "plugin:install",
			},
		},
		{
			Context: "Help",
			Bindings: map[string]string{
				"escape": "help:dismiss",
				"q":      "help:dismiss",
			},
		},
	}
}

// Defaults is a convenience that returns the parsed default bindings.
func Defaults() []Binding {
	return ParseBlocks(DefaultBlocks())
}
