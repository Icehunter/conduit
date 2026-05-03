// Package theme is the single source of truth for conduit's color palette.
//
// Other packages (tui/styles.go, commands/style.go) derive their concrete
// styling from theme.Active(). Switching themes at startup just means
// reading settings.json and calling theme.Set(name) before any package
// reads its derived constants.
//
// Themes mirror the names used by Claude Code's settings.json:
//
//	dark               — default dark theme
//	light              — default light theme
//	dark-daltonism     — dark theme with deuteranopia-friendly red/green
//	light-daltonism    — light variant of the above
//
// Settings file uses these names verbatim; we accept the shorter
// "dark-accessible" / "light-accessible" as aliases.
package theme

import (
	"fmt"
	"strconv"
	"sync"
)

// Palette is the semantic color set. Every UI element picks from these
// roles instead of hardcoding hex values, so retheming is a single switch.
type Palette struct {
	Name string

	// Foreground hierarchy
	Primary   string // main text (#RRGGBB)
	Secondary string // labels, secondary chrome
	Tertiary  string // separators, very dim

	// Brand
	Accent string // Claude orange / brand color

	// Semantic
	Success string // green ✓
	Danger  string // red ✗
	Warning string // yellow
	Info    string // blue

	// Surfaces
	Background   string // app surface — painted across entire TUI region
	ModalBg      string // panels/modals — slightly distinct from Background
	CodeBg       string // fenced code blocks — slightly distinct from Background
	Border       string
	BorderActive string

	// Permission-mode accents
	ModeAcceptEdits string
	ModePlan        string
	ModeAuto        string
}

// Built-in palettes — names match Claude Code's THEME_NAMES exactly so
// settings.json values written by either tool work in both:
//   dark, light, dark-daltonized, light-daltonized, dark-ansi, light-ansi
//
// All themes assume the user's terminal background matches their preference
// (dark themes for dark terminals, light themes for light terminals). We
// don't paint backgrounds — fg-only.

var Dark = Palette{
	Name:            "dark",
	Primary:         "#CDD6E0",
	Secondary:       "#8B92A0",
	Tertiary:        "#4A5160",
	Accent:          "#DA7756",
	Success:         "#4ADE80",
	Danger:          "#F87171",
	Warning:         "#FDE047",
	Info:            "#60A5FA",
	Background:      "#16181D", // app surface
	ModalBg:         "#1E2128", // panels — slightly lighter
	CodeBg:          "#0D1117", // code blocks — slightly darker
	Border:          "#30363D",
	BorderActive:    "#DA7756",
	ModeAcceptEdits: "#C084FC",
	ModePlan:        "#22D3EE",
	ModeAuto:        "#FDE047",
}

var Light = Palette{
	Name:            "light",
	Primary:         "#1F2328",
	Secondary:       "#4D5560",
	Tertiary:        "#9198A1",
	Accent:          "#D77757",
	Success:         "#1A7F37",
	Danger:          "#CF222E",
	Warning:         "#9A6700",
	Info:            "#0969DA",
	Background:      "#FBFBFA", // app surface — off-white
	ModalBg:         "#F1F3F5", // panels — slightly grayer
	CodeBg:          "#EAEDF0", // code blocks — more grayer for contrast
	Border:          "#D0D7DE",
	BorderActive:    "#D77757",
	ModeAcceptEdits: "#8250DF",
	ModePlan:        "#0969DA",
	ModeAuto:        "#9A6700",
}

var DarkDaltonized = Palette{
	Name:            "dark-daltonized",
	Primary:         "#CDD6E0",
	Secondary:       "#8B92A0",
	Tertiary:        "#4A5160",
	Accent:          "#DA7756",
	Success:         "#3B82F6",
	Danger:          "#F59E0B",
	Warning:         "#FDE047",
	Info:            "#A78BFA",
	Background:      "#16181D",
	ModalBg:         "#1E2128",
	CodeBg:          "#0D1117",
	Border:          "#30363D",
	BorderActive:    "#DA7756",
	ModeAcceptEdits: "#C084FC",
	ModePlan:        "#22D3EE",
	ModeAuto:        "#FDE047",
}

var LightDaltonized = Palette{
	Name:            "light-daltonized",
	Primary:         "#1F2328",
	Secondary:       "#4D5560",
	Tertiary:        "#9198A1",
	Accent:          "#D77757",
	Success:         "#0969DA",
	Danger:          "#9A6700",
	Warning:         "#7C2D12",
	Info:            "#6F42C1",
	Background:      "#FBFBFA",
	ModalBg:         "#F1F3F5",
	CodeBg:          "#EAEDF0",
	Border:          "#D0D7DE",
	BorderActive:    "#D77757",
	ModeAcceptEdits: "#8250DF",
	ModePlan:        "#0969DA",
	ModeAuto:        "#9A6700",
}

// ANSI variants — use only the 16 standard ANSI color names for terminals
// without truecolor support. lipgloss accepts "1".."15" as ANSI color codes:
//   0=black 1=red 2=green 3=yellow 4=blue 5=magenta 6=cyan 7=white
//   8..15 = bright versions
// Mirrors src/utils/theme.ts darkAnsiTheme / lightAnsiTheme intent.

var DarkAnsi = Palette{
	Name:            "dark-ansi",
	Primary:         "15", // whiteBright
	Secondary:       "7",  // white
	Tertiary:        "8",  // brightBlack (gray)
	Accent:          "9",  // redBright (Claude's red)
	Success:         "10", // greenBright
	Danger:          "9",  // redBright
	Warning:         "11", // yellowBright
	Info:            "12", // blueBright
	Background:      "0",  // black
	ModalBg:         "8",  // brightBlack (slightly lighter)
	CodeBg:          "0",  // black
	Border:          "8",  // brightBlack
	BorderActive:    "9",  // redBright
	ModeAcceptEdits: "13", // magentaBright
	ModePlan:        "14", // cyanBright
	ModeAuto:        "11", // yellowBright
}

var LightAnsi = Palette{
	Name:            "light-ansi",
	Primary:         "0",  // black
	Secondary:       "8",  // brightBlack (gray)
	Tertiary:        "7",  // white (lighter gray)
	Accent:          "1",  // red
	Success:         "2",  // green
	Danger:          "1",  // red
	Warning:         "3",  // yellow
	Info:            "4",  // blue
	Background:      "15", // whiteBright
	ModalBg:         "7",  // white (slightly grayer)
	CodeBg:          "15", // whiteBright
	Border:          "7",  // white
	BorderActive:    "1",  // red
	ModeAcceptEdits: "5",  // magenta
	ModePlan:        "6",  // cyan
	ModeAuto:        "3",  // yellow
}

var (
	mu      sync.RWMutex
	current = Dark
	// overrides applies per-field color tweaks on top of the named palette,
	// loaded from settings.json's themeOverrides object at startup.
	overrides map[string]string
	// listeners are called after each Set so dependent packages can rebuild
	// derived state (lipgloss styles, ANSI escape constants).
	listeners []func()
)

// Active returns the currently active palette.
func Active() Palette {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// Set switches the active palette by name. Returns true if the name was
// recognized, false otherwise (current palette unchanged on false).
//
// Accepted names match Claude Code's THEME_NAMES + common aliases:
//   dark, light
//   dark-daltonized, light-daltonized (CC official)
//   dark-daltonism, light-daltonism   (alias — older spelling)
//   dark-accessible, light-accessible (alias — friendlier name)
//   dark-ansi, light-ansi             (16-color terminals)
//   auto                              (TODO: detect system, falls back to dark)
//
// Calls registered OnChange listeners after a successful swap.
func Set(name string) bool {
	mu.Lock()
	var picked Palette
	switch name {
	case "dark":
		picked = Dark
	case "light":
		picked = Light
	case "dark-daltonized", "dark-daltonism", "dark-accessible":
		picked = DarkDaltonized
	case "light-daltonized", "light-daltonism", "light-accessible":
		picked = LightDaltonized
	case "dark-ansi":
		picked = DarkAnsi
	case "light-ansi":
		picked = LightAnsi
	case "auto":
		// TODO: detect system dark/light via env. Default to dark for now.
		picked = Dark
	default:
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

// SetOverrides applies per-field color overrides on top of the active palette.
// Used to honor settings.json's themeOverrides object — keys are lowercase
// Palette field names (e.g. "accent", "success", "primary") and values are
// #RRGGBB hex strings or ANSI 0-15 codes.
//
// Unknown keys are silently ignored. Pass nil to clear overrides.
// Triggers OnChange listeners.
func SetOverrides(o map[string]string) {
	mu.Lock()
	overrides = o
	// Reapply overrides to the currently-named palette.
	base := paletteByName(current.Name)
	current = applyOverrides(base, overrides)
	cbs := append([]func(){}, listeners...)
	mu.Unlock()
	for _, cb := range cbs {
		cb()
	}
}

func paletteByName(name string) Palette {
	switch name {
	case "light":
		return Light
	case "dark-daltonized":
		return DarkDaltonized
	case "light-daltonized":
		return LightDaltonized
	case "dark-ansi":
		return DarkAnsi
	case "light-ansi":
		return LightAnsi
	default:
		return Dark
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

// AvailableThemes returns the list of theme names accepted by Set, in
// the canonical order matching Claude Code's THEME_NAMES.
func AvailableThemes() []string {
	return []string{
		"dark",
		"light",
		"dark-daltonized",
		"light-daltonized",
		"dark-ansi",
		"light-ansi",
	}
}

// OnChange registers a callback invoked after each theme switch.
// Used by tui/styles.go and commands/style.go to rebuild their derived
// constants without restarting the process.
func OnChange(cb func()) {
	mu.Lock()
	listeners = append(listeners, cb)
	mu.Unlock()
}

// AnsiFG returns an ANSI escape that sets the foreground to a #RRGGBB hex.
// Used by command output that embeds raw escape sequences (because the TUI
// render layer can't apply lipgloss styles to command result text).
func AnsiFG(hex string) string {
	r, g, b := parseHex(hex)
	return fmt.Sprintf("\033[38;2;%d;%d;%dm", r, g, b)
}

// ANSI escape sequences shared across themes.
const (
	AnsiBold  = "\033[1m"
	AnsiDim   = "\033[2m"
	AnsiReset = "\033[0m"
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
