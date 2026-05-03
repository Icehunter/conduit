package commands

import (
	"strings"
	"testing"
)

func TestTerminalSetupAdvice_NativeCSIu(t *testing.T) {
	for term, name := range map[string]string{
		"ghostty":      "Ghostty",
		"kitty":        "Kitty",
		"iTerm.app":    "iTerm2",
		"WezTerm":      "WezTerm",
		"WarpTerminal": "Warp",
	} {
		got := terminalSetupAdvice(term)
		if !strings.Contains(got, name) {
			t.Errorf("%s: missing terminal display name; got %q", term, got)
		}
		if !strings.Contains(got, "Shift+Enter") || !strings.Contains(got, "No setup needed") {
			t.Errorf("%s: should report Shift+Enter works + no setup needed; got %q", term, got)
		}
	}
}

func TestTerminalSetupAdvice_AppleTerminal(t *testing.T) {
	got := terminalSetupAdvice("Apple_Terminal")
	for _, want := range []string{
		"Option-as-Meta",
		"defaults export com.apple.Terminal",
		"PlistBuddy",
		"useOptionAsMetaKey",
		"killall cfprefsd",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Apple Terminal recipe missing %q", want)
		}
	}
}

func TestTerminalSetupAdvice_VSCodeFamily(t *testing.T) {
	for _, term := range []string{"vscode", "cursor", "windsurf"} {
		got := terminalSetupAdvice(term)
		if !strings.Contains(got, "keybindings file") {
			t.Errorf("%s: missing keybindings.json reference; got %q", term, got)
		}
		if !strings.Contains(got, "shift+enter") {
			t.Errorf("%s: missing shift+enter binding", term)
		}
		if !strings.Contains(got, "\\u001b\\r") {
			t.Errorf("%s: missing escape+CR sequence", term)
		}
	}
}

func TestTerminalSetupAdvice_Alacritty(t *testing.T) {
	got := terminalSetupAdvice("alacritty")
	if !strings.Contains(got, "alacritty.toml") {
		t.Errorf("Alacritty recipe missing config path: %q", got)
	}
	if !strings.Contains(got, `mods = "Shift"`) {
		t.Errorf("Alacritty recipe missing Shift mod binding")
	}
}

func TestTerminalSetupAdvice_Zed(t *testing.T) {
	got := terminalSetupAdvice("zed")
	if !strings.Contains(got, "keymap.json") || !strings.Contains(got, "shift-enter") {
		t.Errorf("Zed recipe missing key elements: %q", got)
	}
}

func TestTerminalSetupAdvice_Unknown(t *testing.T) {
	got := terminalSetupAdvice("some-fancy-term")
	if !strings.Contains(got, "isn't recognized") {
		t.Errorf("unknown terminal should say so; got %q", got)
	}
	if !strings.Contains(got, "some-fancy-term") {
		t.Errorf("unknown advice should name the actual TERM_PROGRAM seen")
	}
}

func TestTerminalSetupAdvice_EmptyTermProgram(t *testing.T) {
	got := terminalSetupAdvice("")
	if !strings.Contains(got, "isn't recognized") {
		t.Errorf("empty TERM_PROGRAM should still produce a recognizable message; got %q", got)
	}
}
