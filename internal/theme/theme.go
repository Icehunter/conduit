// Package theme is the single source of truth for conduit's color palette.
//
// Built-in themes are direct ports of Claude Code's THEME_NAMES with the
// same RGB values, so settings.json values written by either tool render
// identically.
//
// Users can also define custom themes via settings.json's "themes" field
// (a map of name to Palette), or tweak individual fields via "themeOverrides".
package theme

import (
	"fmt"
	"strconv"
	"sync"
)

// Palette is the semantic color set. Maps to a subset of Claude Code's
// Theme struct fields:
//
//	Primary   ← text
//	Secondary ← inactive
//	Tertiary  ← subtle
//	Accent    ← claude
//	Success   ← success
//	Danger    ← error
//	Warning   ← warning
//	Info      ← suggestion / permission
//	Background  ← (intentionally empty for terminal-bg passthrough)
//	ModalBg     ← userMessageBackground
//	CodeBg      ← bashMessageBackgroundColor
//	Border      ← promptBorder
//	BorderActive ← claude
//	ModeAcceptEdits ← autoAccept
//	ModePlan        ← planMode
//	ModeAuto        ← warning
type Palette struct {
	Name string

	Primary   string
	Secondary string
	Tertiary  string

	Accent string

	Success string
	Danger  string
	Warning string
	Info    string

	Background   string
	ModalBg      string
	CodeBg       string
	Border       string
	BorderActive string

	ModeAcceptEdits string
	ModePlan        string
	ModeAuto        string
}

// ----- Built-in palettes (ported from src/utils/theme.ts) -------------------

// Dark — direct port of darkTheme from CC.
var Dark = Palette{
	Name:            "dark",
	Primary:         "#FFFFFF", // text
	Secondary:       "#999999", // inactive
	Tertiary:        "#505050", // subtle
	Accent:          "#D77757", // claude
	Success:         "#4EBA65", // success
	Danger:          "#FF6B80", // error
	Warning:         "#FFC107", // warning
	Info:            "#B1B9F9", // suggestion
	Background:      "",        // empty — terminal bg shows through
	ModalBg:         "#373737", // userMessageBackground
	CodeBg:          "#413C41", // bashMessageBackgroundColor
	Border:          "#888888", // promptBorder
	BorderActive:    "#D77757", // claude
	ModeAcceptEdits: "#AF87FF", // autoAccept
	ModePlan:        "#48968C", // planMode
	ModeAuto:        "#FFC107", // warning
}

// Light — direct port of lightTheme from CC.
var Light = Palette{
	Name:            "light",
	Primary:         "#555555",
	Secondary:       "#8C8C8C",
	Tertiary:        "#AFAFAF",
	Accent:          "#D77757",
	Success:         "#2C7A39",
	Danger:          "#AB2B3F",
	Warning:         "#966C1E",
	Info:            "#5769F7",
	Background:      "", // empty — light themes only meaningful on light terminals
	ModalBg:         "#F0F0F0",
	CodeBg:          "#FAF5FA",
	Border:          "#999999",
	BorderActive:    "#D77757",
	ModeAcceptEdits: "#8700FF",
	ModePlan:        "#006666",
	ModeAuto:        "#966C1E",
}

// DarkDaltonized — direct port of darkDaltonizedTheme from CC.
var DarkDaltonized = Palette{
	Name:            "dark-daltonized",
	Primary:         "#FFFFFF",
	Secondary:       "#999999",
	Tertiary:        "#505050",
	Accent:          "#FF9933",
	Success:         "#3399FF",
	Danger:          "#FF6666",
	Warning:         "#FFCC00",
	Info:            "#99CCFF",
	Background:      "",
	ModalBg:         "#373737",
	CodeBg:          "#413C41",
	Border:          "#888888",
	BorderActive:    "#FF9933",
	ModeAcceptEdits: "#AF87FF",
	ModePlan:        "#669999",
	ModeAuto:        "#FFCC00",
}

// LightDaltonized — direct port of lightDaltonizedTheme from CC.
var LightDaltonized = Palette{
	Name:            "light-daltonized",
	Primary:         "#555555",
	Secondary:       "#8C8C8C",
	Tertiary:        "#AFAFAF",
	Accent:          "#FF9933",
	Success:         "#006699",
	Danger:          "#CC0000",
	Warning:         "#FF9900",
	Info:            "#3366FF",
	Background:      "",
	ModalBg:         "#DCDCDC",
	CodeBg:          "#FAF5FA",
	Border:          "#999999",
	BorderActive:    "#FF9933",
	ModeAcceptEdits: "#8700FF",
	ModePlan:        "#336666",
	ModeAuto:        "#FF9900",
}

// DarkAnsi — direct port of darkAnsiTheme. Uses 16-color ANSI codes.
var DarkAnsi = Palette{
	Name:            "dark-ansi",
	Primary:         "15", // whiteBright
	Secondary:       "7",  // white
	Tertiary:        "7",  // white
	Accent:          "9",  // redBright (claude)
	Success:         "10", // greenBright
	Danger:          "9",  // redBright
	Warning:         "11", // yellowBright
	Info:            "12", // blueBright (suggestion)
	Background:      "",
	ModalBg:         "8", // brightBlack (userMessageBackground)
	CodeBg:          "0", // black
	Border:          "7", // white (promptBorder)
	BorderActive:    "9",
	ModeAcceptEdits: "13", // magentaBright (autoAccept)
	ModePlan:        "14", // cyanBright
	ModeAuto:        "11", // yellowBright (warning)
}

// LightAnsi — based on lightAnsiTheme. CC's text="ansi:black" assumes a
// light terminal; we use 7 (white/light gray) instead so Primary remains
// visible on dark terminals too. Mirrors dark-ansi's Primary=15 one notch
// dimmer to keep the "light theme" feel.
var LightAnsi = Palette{
	Name:            "light-ansi",
	Primary:         "7", // white (light gray) — readable on both bgs
	Secondary:       "8", // blackBright (inactive)
	Tertiary:        "8", // blackBright (subtle)
	Accent:          "9", // redBright (claude)
	Success:         "2", // green
	Danger:          "1", // red
	Warning:         "3", // yellow
	Info:            "4", // blue (suggestion)
	Background:      "",
	ModalBg:         "7", // white (userMessageBackground)
	CodeBg:          "15",
	Border:          "7",
	BorderActive:    "9",
	ModeAcceptEdits: "5", // magenta
	ModePlan:        "6", // cyan
	ModeAuto:        "3", // yellow
}

// ----- Active palette + listeners -------------------------------------------

var (
	mu      sync.RWMutex
	current = Dark

	// userThemes is populated by SetUserThemes — custom themes from
	// settings.json. Custom names take precedence over built-ins of the
	// same name so users can override.
	userThemes map[string]Palette

	// overrides applies per-field tweaks on top of the named palette
	// (settings.json's themeOverrides field).
	overrides map[string]string

	listeners []func()
)

// Active returns the currently active palette.
func Active() Palette {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Set switches the active palette by name. Returns true if recognised.
//
// Resolution order:
//  1. settings.json custom themes (userThemes)
//  2. Built-in CC-named themes
//  3. Aliases (dark-accessible → dark-daltonized, etc.)
//  4. "auto" → Dark (TODO: detect system preference)
//
// Unknown names return false and leave the current palette unchanged.
func Set(name string) bool {
	mu.Lock()
	picked, ok := resolve(name)
	if !ok {
		mu.Unlock()
		return false
	}
	current = applyOverrides(picked, overrides)
	cbs := append([]func(){}, listeners...)
	mu.Unlock()
	for _, cb := range cbs {
		cb()
	}
	return true
}

// resolve picks a palette by name. Caller must hold mu.
func resolve(name string) (Palette, bool) {
	if p, ok := userThemes[name]; ok {
		return p, true
	}
	switch name {
	case "dark":
		return Dark, true
	case "light":
		return Light, true
	case "dark-daltonized", "dark-daltonism", "dark-accessible":
		return DarkDaltonized, true
	case "light-daltonized", "light-daltonism", "light-accessible":
		return LightDaltonized, true
	case "dark-ansi":
		return DarkAnsi, true
	case "light-ansi":
		return LightAnsi, true
	case "auto":
		return Dark, true
	}
	return Palette{}, false
}

// SetUserThemes registers custom palettes loaded from settings.json. Pass
// nil to clear. Triggers OnChange listeners so the active palette can be
// re-resolved if it now matches a user override.
func SetUserThemes(themes map[string]Palette) {
	mu.Lock()
	userThemes = themes
	if p, ok := resolve(current.Name); ok {
		current = applyOverrides(p, overrides)
	}
	cbs := append([]func(){}, listeners...)
	mu.Unlock()
	for _, cb := range cbs {
		cb()
	}
}

// SetOverrides applies per-field colour tweaks on top of the active palette.
// Pass nil to clear.
func SetOverrides(o map[string]string) {
	mu.Lock()
	overrides = o
	if p, ok := resolve(current.Name); ok {
		current = applyOverrides(p, overrides)
	}
	cbs := append([]func(){}, listeners...)
	mu.Unlock()
	for _, cb := range cbs {
		cb()
	}
}

func applyOverrides(base Palette, o map[string]string) Palette {
	if len(o) == 0 {
		return base
	}
	get := func(key, fallback string) string {
		if v, ok := o[key]; ok && v != "" {
			return v
		}
		return fallback
	}
	return Palette{
		Name:            base.Name,
		Primary:         get("primary", base.Primary),
		Secondary:       get("secondary", base.Secondary),
		Tertiary:        get("tertiary", base.Tertiary),
		Accent:          get("accent", base.Accent),
		Success:         get("success", base.Success),
		Danger:          get("danger", base.Danger),
		Warning:         get("warning", base.Warning),
		Info:            get("info", base.Info),
		Background:      get("background", base.Background),
		ModalBg:         get("modalbg", base.ModalBg),
		CodeBg:          get("codebg", base.CodeBg),
		Border:          get("border", base.Border),
		BorderActive:    get("borderactive", base.BorderActive),
		ModeAcceptEdits: get("modeacceptedits", base.ModeAcceptEdits),
		ModePlan:        get("modeplan", base.ModePlan),
		ModeAuto:        get("modeauto", base.ModeAuto),
	}
}

// AvailableThemes returns the list of names selectable in the /theme picker.
//
// All Claude Code built-in palettes are listed (including light variants)
// so a user who shares settings.json between conduit and Claude Code can
// pick light themes here without conduit silently rewriting their config to
// a different palette. Light text on dark terminals is the user's call.
//
// Order: user-defined themes first, then built-in dark, then built-in light.
func AvailableThemes() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(userThemes)+6)
	for name := range userThemes {
		out = append(out, name)
	}
	out = append(out,
		"dark", "dark-daltonized", "dark-ansi",
		"light", "light-daltonized", "light-ansi",
	)
	return out
}

// OnChange registers a callback fired after each successful Set.
func OnChange(cb func()) {
	mu.Lock()
	listeners = append(listeners, cb)
	mu.Unlock()
}

// ----- ANSI helpers ---------------------------------------------------------

// AnsiFG returns an ANSI escape that sets the foreground.
// Hex must be #RRGGBB or a string-encoded 0..255 ANSI code.
func AnsiFG(value string) string {
	if isAnsiCode(value) {
		return fmt.Sprintf("\033[38;5;%sm", value)
	}
	r, g, b := parseHex(value)
	return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
}

// AnsiBG returns an ANSI escape that sets the background.
func AnsiBG(value string) string {
	if isAnsiCode(value) {
		return fmt.Sprintf("\033[48;5;%sm", value)
	}
	r, g, b := parseHex(value)
	return fmt.Sprintf("\033[48;2;%d;%d;%dm", r, g, b)
}

func isAnsiCode(s string) bool {
	if s == "" || len(s) > 3 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

const (
	AnsiBold      = "\033[1m"
	AnsiDim       = "\033[2m"
	AnsiReset     = "\033[0m"
	AnsiResetSoft = "\033[22;23;39m"
)

func parseHex(hex string) (r, g, b uint8) {
	if len(hex) == 7 && hex[0] == '#' {
		hex = hex[1:]
	}
	if len(hex) != 6 {
		return
	}
	rv, _ := strconv.ParseUint(hex[0:2], 16, 8)
	gv, _ := strconv.ParseUint(hex[2:4], 16, 8)
	bv, _ := strconv.ParseUint(hex[4:6], 16, 8)
	return uint8(rv), uint8(gv), uint8(bv)
}
