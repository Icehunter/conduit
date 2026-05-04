package commands

import (
	"fmt"
	"os"
	"runtime"
	"strings"
)

// nativeCSIuTerminals are terminals that natively support the Kitty
// Keyboard Protocol / CSI-u sequence for Shift+Enter, which conduit
// already parses. No setup needed for these. Mirrors the table in
// src/commands/terminalSetup/terminalSetup.tsx NATIVE_CSIU_TERMINALS.
var nativeCSIuTerminals = map[string]string{
	"ghostty":     "Ghostty",
	"kitty":       "Kitty",
	"iTerm.app":   "iTerm2",
	"WezTerm":     "WezTerm",
	"WarpTerminal": "Warp",
}

// RegisterTerminalSetupCommand adds /terminalSetup, which detects the
// user's terminal via TERM_PROGRAM and prints either:
//   - "no setup needed" for native CSI-u terminals, or
//   - the exact manual recipe for terminals that need configuration
//     (Apple Terminal, VSCode, Cursor, Windsurf, Alacritty, Zed).
//
// Mirrors src/commands/terminalSetup/terminalSetup.tsx — currently
// info-only (the user runs the recipe themselves). The plist/keybindings.
// json automation in CC's source is a future commit; doing it
// info-only first keeps user dotfiles untouched until we ship a tested
// backup/restore path.
func RegisterTerminalSetupCommand(r *Registry) {
	r.Register(Command{
		// Stored lowercase — Dispatch lowercases input before lookup, so a
		// camelCase Name would never match. CC's /help shows it as
		// "/terminalSetup"; we mirror that visually only via Description.
		Name:        "terminalsetup",
		Description: "Detect your terminal and show how to enable Shift+Enter for newlines",
		Handler: func(string) Result {
			term := os.Getenv("TERM_PROGRAM")
			return Result{Type: "text", Text: terminalSetupAdvice(term)}
		},
	})
}

func terminalSetupAdvice(term string) string {
	if name, ok := nativeCSIuTerminals[term]; ok {
		return fmt.Sprintf(
			"%s supports the Kitty keyboard protocol natively, and conduit (bubbletea v2) decodes those sequences. Shift+Enter inserts a newline. No setup needed.\n\n"+
				"Mouse / scroll notes:\n"+
				"  • Trackpad / scroll wheel scrolls the chat viewport\n"+
				"  • UP/DOWN arrows navigate input history\n"+
				"  • Text selection: conduit enables mouse mode for scroll — to select text hold Option (⌥) while dragging (macOS) or Shift+drag on Linux",
			name,
		)
	}

	switch term {
	case "Apple_Terminal":
		return appleTerminalRecipe()
	case "vscode":
		return vscodeRecipe("VSCode", "Library/Application Support/Code/User/keybindings.json")
	case "cursor":
		return vscodeRecipe("Cursor", "Library/Application Support/Cursor/User/keybindings.json")
	case "windsurf":
		return vscodeRecipe("Windsurf", "Library/Application Support/Windsurf/User/keybindings.json")
	case "alacritty":
		return alacrittyRecipe()
	case "zed":
		return zedRecipe()
	default:
		// Unknown terminal — give the generic CSI-u guidance.
		hint := "Your terminal isn't recognized."
		if term != "" {
			hint = fmt.Sprintf("Your terminal (%s) isn't recognized.", term)
		}
		return hint + " If Shift+Enter doesn't insert a newline, check whether your terminal supports the Kitty keyboard protocol or CSI-u sequences. Most modern terminals (iTerm2, Kitty, Ghostty, WezTerm, Warp) work out of the box."
	}
}

func appleTerminalRecipe() string {
	if runtime.GOOS != "darwin" {
		return "Apple Terminal is macOS-only; this command shouldn't have matched on " + runtime.GOOS + "."
	}
	return strings.Join([]string{
		"Apple Terminal needs Option-as-Meta enabled for Shift+Enter to send a newline.",
		"",
		"  1. Back up your Terminal preferences:",
		"       defaults export com.apple.Terminal ~/Desktop/Terminal-backup.plist",
		"",
		"  2. Find your active profile name:",
		"       defaults read com.apple.Terminal 'Default Window Settings'",
		"",
		"  3. Enable Option-as-Meta on that profile (replace <PROFILE> below):",
		"       /usr/libexec/PlistBuddy -c \\",
		"         \"Set :'Window Settings':<PROFILE>:useOptionAsMetaKey true\" \\",
		"         ~/Library/Preferences/com.apple.Terminal.plist",
		"",
		"  4. Flush the preferences cache:",
		"       killall cfprefsd",
		"",
		"  5. Quit and reopen Terminal.app.",
		"",
		"After this, Option+Enter inserts a newline in conduit.",
	}, "\n")
}

func vscodeRecipe(name, relPath string) string {
	home, _ := os.UserHomeDir()
	full := home + "/" + relPath
	return strings.Join([]string{
		fmt.Sprintf("%s's integrated terminal needs a keybinding so Shift+Enter sends a newline.", name),
		"",
		fmt.Sprintf("Add this to your %s keybindings file (%s):", name, full),
		"",
		"  [",
		"    {",
		"      \"key\": \"shift+enter\",",
		"      \"command\": \"workbench.action.terminal.sendSequence\",",
		"      \"args\": { \"text\": \"\\u001b\\r\" },",
		fmt.Sprintf("      \"when\": \"terminalFocus && terminalProcessSupported\""),
		"    }",
		"  ]",
		"",
		fmt.Sprintf("If the file already has bindings, merge the entry into the existing array. Reload %s after saving.", name),
	}, "\n")
}

func alacrittyRecipe() string {
	return strings.Join([]string{
		"Alacritty needs a keybinding for Shift+Enter.",
		"",
		"Add this to your Alacritty config (~/.config/alacritty/alacritty.toml):",
		"",
		"  [[keyboard.bindings]]",
		"  key = \"Return\"",
		"  mods = \"Shift\"",
		"  chars = \"\\u001b\\r\"",
		"",
		"Restart Alacritty after saving.",
	}, "\n")
}

func zedRecipe() string {
	return strings.Join([]string{
		"Zed's terminal needs a keybinding for Shift+Enter.",
		"",
		"Add this to your Zed keymap (~/.config/zed/keymap.json):",
		"",
		"  [",
		"    {",
		"      \"context\": \"Terminal\",",
		"      \"bindings\": {",
		"        \"shift-enter\": [\"terminal::SendText\", \"\\u001b\\r\"]",
		"      }",
		"    }",
		"  ]",
		"",
		"Reload Zed after saving (Cmd-Shift-P → reload).",
	}, "\n")
}
