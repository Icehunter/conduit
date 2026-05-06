package commands

// terminalSetup automated apply logic.
// Mirrors src/commands/terminalSetup/terminalSetup.tsx:
//   enableOptionAsMetaForTerminal / installBindingsForVSCodeTerminal /
//   installBindingsForAlacritty / installBindingsForZed
//
// Each apply function:
//   1. Backs up the target file (or plist) before modifying.
//   2. Makes the minimal idempotent change.
//   3. Returns a human-readable result string.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// applyTerminalSetup runs the automated write for the detected terminal.
// Returns a result string (success, already-done, or error description).
func applyTerminalSetup(term string) string {
	// Native CSI-u terminals need no setup at all.
	if name, ok := nativeCSIuTerminals[term]; ok {
		return name + " supports the Kitty keyboard protocol natively — no setup needed.\nShift+Enter already inserts a newline."
	}
	switch term {
	case "Apple_Terminal":
		if runtime.GOOS != "darwin" {
			return "Apple Terminal setup is macOS-only."
		}
		return applyAppleTerminal()
	case "vscode":
		return applyVSCode("VSCode", "Code")
	case "cursor":
		return applyVSCode("Cursor", "Cursor")
	case "windsurf":
		return applyVSCode("Windsurf", "Windsurf")
	case "alacritty":
		return applyAlacritty()
	case "zed":
		return applyZed()
	default:
		hint := "your terminal"
		if term != "" {
			hint = term
		}
		return "No automated setup available for " + hint + ".\nRun /terminalsetup (without --apply) for manual instructions."
	}
}

// --- Apple Terminal ---

func applyAppleTerminal() string {
	// Read active profiles.
	defaultProfile, err := runCmd("defaults", "read", "com.apple.Terminal", "Default Window Settings")
	if err != nil || strings.TrimSpace(defaultProfile) == "" {
		return "Could not read Terminal.app default profile: " + err.Error()
	}
	startupProfile, err := runCmd("defaults", "read", "com.apple.Terminal", "Startup Window Settings")
	if err != nil {
		startupProfile = defaultProfile
	}
	defaultProfile = strings.TrimSpace(defaultProfile)
	startupProfile = strings.TrimSpace(startupProfile)

	// Backup the plist first.
	plistPath := filepath.Join(os.Getenv("HOME"), "Library", "Preferences", "com.apple.Terminal.plist")
	backupPath := plistPath + "." + randHex(4) + ".bak"
	if _, err := runCmd("defaults", "export", "com.apple.Terminal", backupPath); err != nil {
		return "Could not back up Terminal.app preferences: " + err.Error()
	}

	applied := false
	profiles := []string{defaultProfile}
	if startupProfile != defaultProfile {
		profiles = append(profiles, startupProfile)
	}
	for _, profile := range profiles {
		if err := enableOptionAsMetaForProfile(profile, plistPath); err == nil {
			applied = true
		}
	}
	if !applied {
		return "Could not enable Option as Meta for any Terminal.app profile.\nRestore with: defaults import com.apple.Terminal " + backupPath
	}

	// Flush prefs cache.
	_, _ = runCmd("killall", "cfprefsd")

	return "Terminal.app configured:\n" +
		"  ✓ Enabled \"Use Option as Meta key\"\n\n" +
		"Restart Terminal.app for changes to take effect.\n" +
		"Option+Enter will now insert a newline.\n\n" +
		"Backup saved to: " + backupPath
}

func enableOptionAsMetaForProfile(profile, plistPath string) error {
	key := fmt.Sprintf("'Window Settings':%s:useOptionAsMetaKey", profile)
	_, err := runCmd("/usr/libexec/PlistBuddy", "-c", "Set :"+key+" true", plistPath)
	if err != nil {
		// Key may not exist — try Add instead.
		_, err = runCmd("/usr/libexec/PlistBuddy", "-c", "Add :"+key+" bool true", plistPath)
	}
	return err
}

// --- VSCode / Cursor / Windsurf ---

const vscodeBinding = `{
    "key": "shift+enter",
    "command": "workbench.action.terminal.sendSequence",
    "args": { "text": "\r" },
    "when": "terminalFocus"
  }`

func applyVSCode(name, dirName string) string {
	home, _ := os.UserHomeDir()
	var userDir string
	switch runtime.GOOS {
	case "darwin":
		userDir = filepath.Join(home, "Library", "Application Support", dirName, "User")
	case "windows":
		userDir = filepath.Join(os.Getenv("APPDATA"), dirName, "User")
	default:
		userDir = filepath.Join(home, ".config", dirName, "User")
	}
	kbPath := filepath.Join(userDir, "keybindings.json")

	// Ensure directory exists.
	if err := os.MkdirAll(userDir, 0o700); err != nil {
		return fmt.Sprintf("Could not create %s user directory: %v", name, err)
	}

	// Read existing file or start with empty array.
	content := []byte("[]")
	fileExists := false
	if data, err := os.ReadFile(kbPath); err == nil {
		content = data
		fileExists = true
	}

	// Check for existing binding.
	if strings.Contains(string(content), "shift+enter") &&
		strings.Contains(string(content), "sendSequence") {
		return fmt.Sprintf("%s already has a Shift+Enter terminal binding.\nSee: %s", name, kbPath)
	}

	// Parse JSON array and append entry.
	bindings := make([]json.RawMessage, 0, 1)
	if err := json.Unmarshal(stripJSONCComments(content), &bindings); err != nil {
		return fmt.Sprintf("Could not parse %s keybindings, so it was left unchanged: %v", name, err)
	}

	// Backup if file existed.
	if fileExists {
		backupPath := kbPath + "." + randHex(4) + ".bak"
		if err := os.WriteFile(backupPath, content, 0o600); err != nil {
			return fmt.Sprintf("Could not back up %s keybindings: %v", name, err)
		}
	}
	bindings = append(bindings, json.RawMessage(vscodeBinding))
	out, err := json.MarshalIndent(bindings, "", "  ")
	if err != nil {
		return fmt.Sprintf("Could not serialize %s keybindings: %v", name, err)
	}
	if err := os.WriteFile(kbPath, out, 0o600); err != nil {
		return fmt.Sprintf("Could not write %s keybindings: %v", name, err)
	}
	return fmt.Sprintf("%s Shift+Enter keybinding installed.\nSee: %s\n\nReload %s after saving (Cmd+Shift+P → Reload Window).", name, kbPath, name)
}

// --- Alacritty ---

const alacrittyBinding = "\n[[keyboard.bindings]]\nkey = \"Return\"\nmods = \"Shift\"\nchars = \"\\u001B\\r\"\n"

func applyAlacritty() string {
	home, _ := os.UserHomeDir()
	candidates := []string{filepath.Join(home, ".config", "alacritty", "alacritty.toml")}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		candidates = append([]string{filepath.Join(xdg, "alacritty", "alacritty.toml")}, candidates...)
	}

	// Find existing config or use first candidate.
	configPath := candidates[0]
	existing := []byte{}
	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil {
			existing = data
			configPath = p
			break
		}
	}

	if strings.Contains(string(existing), "[[keyboard.bindings]]") &&
		strings.Contains(string(existing), "Shift") {
		return "Alacritty already has a keyboard binding configured.\nSee: " + configPath
	}

	// Backup if file existed.
	if len(existing) > 0 {
		backupPath := configPath + "." + randHex(4) + ".bak"
		if err := os.WriteFile(backupPath, existing, 0o600); err != nil {
			return "Could not back up Alacritty config: " + err.Error()
		}
	}

	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		return "Could not create Alacritty config directory: " + err.Error()
	}

	newContent := string(existing) + alacrittyBinding
	if err := os.WriteFile(configPath, []byte(newContent), 0o600); err != nil {
		return "Could not write Alacritty config: " + err.Error()
	}
	return "Alacritty Shift+Enter keybinding installed.\nSee: " + configPath + "\n\nRestart Alacritty for changes to take effect."
}

// --- Zed ---

func applyZed() string {
	home, _ := os.UserHomeDir()
	zedDir := filepath.Join(home, ".config", "zed")
	keymapPath := filepath.Join(zedDir, "keymap.json")

	if err := os.MkdirAll(zedDir, 0o700); err != nil {
		return "Could not create Zed config directory: " + err.Error()
	}

	content := []byte("[]")
	fileExists := false
	if data, err := os.ReadFile(keymapPath); err == nil {
		content = data
		fileExists = true
	}

	if strings.Contains(string(content), "shift-enter") {
		return "Zed already has a Shift+Enter binding.\nSee: " + keymapPath
	}

	const zedEntry = `{
    "context": "Terminal",
    "bindings": {
      "shift-enter": ["terminal::SendText", "\r"]
    }
  }`

	keymap := make([]json.RawMessage, 0, 1)
	if err := json.Unmarshal(content, &keymap); err != nil {
		return "Could not parse Zed keymap, so it was left unchanged: " + err.Error()
	}

	if fileExists {
		backupPath := keymapPath + "." + randHex(4) + ".bak"
		if err := os.WriteFile(backupPath, content, 0o600); err != nil {
			return "Could not back up Zed keymap: " + err.Error()
		}
	}
	keymap = append(keymap, json.RawMessage(zedEntry))
	out, err := json.MarshalIndent(keymap, "", "  ")
	if err != nil {
		return "Could not serialize Zed keymap: " + err.Error()
	}
	if err := os.WriteFile(keymapPath, out, 0o600); err != nil {
		return "Could not write Zed keymap: " + err.Error()
	}
	return "Zed Shift+Enter keybinding installed.\nSee: " + keymapPath + "\n\nReload Zed (Cmd+Shift+P → reload keymap)."
}

// --- helpers ---

func runCmd(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput() //nolint:noctx
	return string(out), err
}

func randHex(_ int) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// stripJSONCComments removes single-line // comments so standard json.Unmarshal
// can parse JSONC files like VSCode's keybindings.json.
func stripJSONCComments(data []byte) []byte {
	var out strings.Builder
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if i := strings.Index(line, "//"); i >= 0 {
			// Only strip if not inside a string (simple heuristic: count unescaped quotes before //).
			prefix := line[:i]
			if strings.Count(prefix, `"`)%2 == 0 {
				line = prefix
			}
		}
		out.WriteString(line + "\n")
	}
	return []byte(out.String())
}
