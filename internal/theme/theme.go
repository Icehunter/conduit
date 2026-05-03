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
	CodeBg       string
	Border       string
	BorderActive string

	// Permission-mode accents
	ModeAcceptEdits string
	ModePlan        string
	ModeAuto        string
}

// Built-in palettes.

var Dark = Palette{
	Name:            "dark",
	Primary:         "#CDD6E0",
	Secondary:       "#636D7E",
	Tertiary:        "#3D4554",
	Accent:          "#DA7756",
	Success:         "#4ADE80",
	Danger:          "#F87171",
	Warning:         "#FDE047",
	Info:            "#60A5FA",
	CodeBg:          "#0D1117",
	Border:          "#30363D",
	BorderActive:    "#DA7756",
	ModeAcceptEdits: "#C084FC",
	ModePlan:        "#22D3EE",
	ModeAuto:        "#FDE047",
}

var Light = Palette{
	Name:            "light",
	Primary:         "#1F2328",
	Secondary:       "#656D76",
	Tertiary:        "#9198A1",
	Accent:          "#D77757",
	Success:         "#1A7F37",
	Danger:          "#CF222E",
	Warning:         "#9A6700",
	Info:            "#0969DA",
	CodeBg:          "#F6F8FA",
	Border:          "#D0D7DE",
	BorderActive:    "#D77757",
	ModeAcceptEdits: "#8250DF",
	ModePlan:        "#0969DA",
	ModeAuto:        "#9A6700",
}

var DarkAccessible = Palette{
	Name:            "dark-daltonism",
	Primary:         "#CDD6E0",
	Secondary:       "#636D7E",
	Tertiary:        "#3D4554",
	Accent:          "#DA7756",
	Success:         "#3B82F6", // blue instead of green for deuteranopia
	Danger:          "#F59E0B", // amber instead of red
	Warning:         "#FDE047",
	Info:            "#A78BFA",
	CodeBg:          "#0D1117",
	Border:          "#30363D",
	BorderActive:    "#DA7756",
	ModeAcceptEdits: "#C084FC",
	ModePlan:        "#22D3EE",
	ModeAuto:        "#FDE047",
}

var LightAccessible = Palette{
	Name:            "light-daltonism",
	Primary:         "#1F2328",
	Secondary:       "#656D76",
	Tertiary:        "#9198A1",
	Accent:          "#D77757",
	Success:         "#0969DA", // blue instead of green
	Danger:          "#9A6700", // amber instead of red
	Warning:         "#7C2D12",
	Info:            "#6F42C1",
	CodeBg:          "#F6F8FA",
	Border:          "#D0D7DE",
	BorderActive:    "#D77757",
	ModeAcceptEdits: "#8250DF",
	ModePlan:        "#0969DA",
	ModeAuto:        "#9A6700",
}

var (
	mu      sync.RWMutex
	current = Dark
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

// Set switches the active palette by name. Unknown names map to Dark.
// Aliases: "dark-accessible" → "dark-daltonism", same for light.
// Calls registered OnChange listeners after the swap.
func Set(name string) {
	mu.Lock()
	switch name {
	case "light":
		current = Light
	case "dark-daltonism", "dark-daltonized", "dark-accessible":
		current = DarkAccessible
	case "light-daltonism", "light-daltonized", "light-accessible":
		current = LightAccessible
	default:
		current = Dark
	}
	cbs := append([]func(){}, listeners...)
	mu.Unlock()
	for _, cb := range cbs {
		cb()
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
