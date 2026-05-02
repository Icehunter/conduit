// Package buddy implements the companion (tamagotchi) system.
//
// Mirrors src/buddy/ from the Claude Code TS source:
//   - Mulberry32 PRNG seeded from FNV-1a hash of user ID
//   - 18 species with 5 rarity tiers
//   - 5 stats (DEBUGGING, PATIENCE, CHAOS, WISDOM, SNARK)
//   - ASCII sprite frames with eye/hat substitution
//   - /buddy command for display
package buddy

import (
	"fmt"
	"os"
	"strings"
)

// --- PRNG ---

// mulberry32 returns a Mulberry32 PRNG closure seeded by seed.
// Mirrors the TS mulberry32() function exactly.
func mulberry32(seed uint32) func() float64 {
	a := seed
	return func() float64 {
		a += 0x6d2b79f5
		t := uint32(int32(a^(a>>15)) * int32(1|a))
		t = uint32(int32(t^(t>>7))*int32(61|t)) ^ t
		return float64(t^(t>>14)) / 4294967296.0
	}
}

// fnv1a computes the FNV-1a hash of s. Mirrors the TS fallback hash.
func fnv1a(s string) uint32 {
	const (
		offset uint32 = 2166136261
		prime  uint32 = 16777619
	)
	h := offset
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime
	}
	return h
}

// --- Types ---

// AllSpecies is the complete list of 18 companion species (mirrors buddy/types.ts).
var AllSpecies = []string{
	"duck", "goose", "blob", "cat", "dragon", "octopus",
	"owl", "penguin", "turtle", "snail", "ghost", "axolotl",
	"capybara", "cactus", "robot", "rabbit", "mushroom", "chonk",
}

var allEyes = []string{"·", "✦", "×", "◉", "@", "°"}
var allHats = []string{"none", "crown", "tophat", "propeller", "halo", "wizard", "beanie", "tinyduck"}

var rarityWeights = []struct {
	name   string
	weight int
}{
	{"common", 60},
	{"uncommon", 25},
	{"rare", 10},
	{"epic", 4},
	{"legendary", 1},
}

var rarityFloor = map[string]int{
	"common":    5,
	"uncommon":  15,
	"rare":      25,
	"epic":      35,
	"legendary": 50,
}

var statNames = []string{"DEBUGGING", "PATIENCE", "CHAOS", "WISDOM", "SNARK"}

// Bones are the deterministically generated companion attributes.
type Bones struct {
	Species string
	Rarity  string
	Eye     string
	Hat     string
	Shiny   bool
	Stats   map[string]int // 1-100 per stat
}

// buddySalt matches the TS salt constant.
const buddySalt = "friend-2026-401"

// GenerateBones generates deterministic companion bones for the given userID.
// Mirrors companion.ts generateBones().
//
// forcedRarity overrides the weighted rarity roll (use "" for normal roll).
// The env var CLAUDE_BUDDY_FORCE_RARITY takes precedence over forcedRarity.
func GenerateBones(userID string, forcedRarity ...string) Bones {
	seed := fnv1a(userID + buddySalt)
	rng := mulberry32(seed)

	// Rarity roll — env overrides stored value overrides weighted roll.
	rarity := rollRarity(rng)
	override := os.Getenv("CLAUDE_BUDDY_FORCE_RARITY")
	if override == "" && len(forcedRarity) > 0 {
		override = forcedRarity[0]
	}
	if _, ok := rarityFloor[override]; ok {
		rarity = override
	}

	// Species, eye, hat (uniform picks).
	species := AllSpecies[int(rng()*float64(len(AllSpecies)))%len(AllSpecies)]
	eye := allEyes[int(rng()*float64(len(allEyes)))%len(allEyes)]
	hat := allHats[int(rng()*float64(len(allHats)))%len(allHats)]

	// Shiny: 1% chance.
	shiny := rng() < 0.01

	// Stats.
	floor := rarityFloor[rarity]
	stats := rollStats(rng, floor)

	return Bones{
		Species: species,
		Rarity:  rarity,
		Eye:     eye,
		Hat:     hat,
		Shiny:   shiny,
		Stats:   stats,
	}
}

func rollRarity(rng func() float64) string {
	total := 0
	for _, w := range rarityWeights {
		total += w.weight
	}
	r := int(rng() * float64(total))
	cum := 0
	for _, w := range rarityWeights {
		cum += w.weight
		if r < cum {
			return w.name
		}
	}
	return "common"
}

func rollStats(rng func() float64, floor int) map[string]int {
	// Pick one peak stat and one dump stat.
	peakIdx := int(rng() * float64(len(statNames)))
	dumpIdx := int(rng() * float64(len(statNames)))
	if dumpIdx == peakIdx {
		dumpIdx = (peakIdx + 1) % len(statNames)
	}

	stats := make(map[string]int, len(statNames))
	for i, name := range statNames {
		var val int
		switch i {
		case peakIdx:
			val = floor + 50 + int(rng()*30) // floor+50 to floor+79
		case dumpIdx:
			val = floor - 10 + int(rng()*15) // floor-10 to floor+4
		default:
			val = floor + int(rng()*40) // floor+0 to floor+39
		}
		if val < 1 {
			val = 1
		}
		if val > 100 {
			val = 100
		}
		stats[name] = val
	}
	return stats
}

// --- Sprites ---

// spriteFrames maps species → list of frame strings.
// Each frame uses {E} for the eye placeholder.
// Mirrors buddy/sprites.ts BODIES (simplified 3-line ASCII art).
var spriteFrames = map[string][][]string{
	"duck": {
		{"  (>  ", " (  ) ", " /  \\ "},
		{"  (>  ", " (  ) ", "  /\\ "},
		{"  (>  ", " (` ) ", " /  \\ "},
	},
	"goose": {
		{" ({E}  ", "( (  ) ", " (  /  "},
		{" ({E}  ", "( (  ) ", "  \\/  "},
	},
	"blob": {
		{" ({E}{E}) ", "(      )", " \\____/ "},
		{" ({E}{E}) ", "(  ~~  )", " \\____/ "},
	},
	"cat": {
		{"/\\  /\\  ", "({E}  {E})", "( >ω< )"},
		{"/\\  /\\  ", "({E}  {E})", "( >∀< )"},
		{"/\\  /\\  ", "({E}  {E})", "(  -.- )"},
	},
	"dragon": {
		{"  //\\   ", " /({E})\\ ", "/  ><  \\"},
		{"  //\\   ", " /({E})\\ ", "/  <>  \\"},
	},
	"octopus": {
		{" ({E}{E}) ", "(~~~~~~)", "/\\  /\\  "},
		{" ({E}{E}) ", "(~~~~~~)", " \\/  \\/ "},
	},
	"owl": {
		{"  /\\_/\\  ", " ({E}  {E}) ", " (  ω  ) "},
		{"  /\\_/\\  ", " ({E}  {E}) ", " (  v  ) "},
	},
	"penguin": {
		{"  (  )  ", " ({E}{E}) ", "  \\  /  "},
		{"  (  )  ", " ({E}o{E}) ", "  \\  /  "},
	},
	"turtle": {
		{" _____  ", "({E}   {E})", "\\~~~~~/ "},
		{" _____  ", "({E}   {E})", " \\~~~/ "},
	},
	"snail": {
		{"   @@   ", "  @{E}@  ", " (____)  "},
		{"   @@   ", "  @{E}@  ", " (____)) "},
	},
	"ghost": {
		{"  .-.   ", " ({E}{E})  ", " | U U | "},
		{"  .-.   ", " ({E}{E})  ", " | u u | "},
	},
	"axolotl": {
		{" \\(  )/ ", "  ({E}{E})  ", "  ~~~~~  "},
		{" \\(  )/ ", "  ({E}^{E})  ", "  ~~~~~  "},
	},
	"capybara": {
		{"  ____  ", " ({E}  {E}) ", " /||||\\  "},
		{"  ____  ", " ({E}  {E}) ", " /====\\  "},
	},
	"cactus": {
		{" \\|/|/  ", "  |{E}|   ", "  |||   "},
		{" /|\\|\\  ", "  |{E}|   ", "  |||   "},
	},
	"robot": {
		{"  [===]  ", " [{E}  {E}] ", " [_____] "},
		{"  [===]  ", " [{E}  {E}] ", " [#####] "},
	},
	"rabbit": {
		{" /\\ /\\  ", "({E}   {E})", " (_____)  "},
		{" /\\ /\\  ", "({E}   {E})", " (_ . _)  "},
	},
	"mushroom": {
		{"  /~~~\\  ", " ({E}   {E}) ", "  |___|  "},
		{"  /~~~\\  ", " ({E} _ {E}) ", "  |___|  "},
	},
	"chonk": {
		{"  .~~~.  ", " ({E}   {E}) ", "  ~~~~~  "},
		{"  .~~~.  ", " ({E} . {E}) ", "  ~~~~~  "},
		{"  .~~~.  ", " ({E}   {E}) ", "  ~~zzz  "},
	},
}

var hatLines = map[string]string{
	"none":      "",
	"crown":     "\\^^^/",
	"tophat":    "[___]",
	"propeller": " -@- ",
	"halo":      " ~O~ ",
	"wizard":    " /|\\  ",
	"beanie":    "(___)  ",
	"tinyduck":  "(>)  ",
}

// SpriteFrameCount returns the number of animation frames for a species.
func SpriteFrameCount(species string) int {
	frames, ok := spriteFrames[species]
	if !ok || len(frames) == 0 {
		return 1
	}
	return len(frames)
}

// RenderSprite renders one animation frame of the companion.
// frame is taken modulo the frame count. Eye placeholder {E} is substituted.
func RenderSprite(b Bones, frame int) string {
	frames, ok := spriteFrames[b.Species]
	if !ok || len(frames) == 0 {
		return fmt.Sprintf("(%s)", b.Eye)
	}
	f := frames[frame%len(frames)]
	var sb strings.Builder
	for i, line := range f {
		l := strings.ReplaceAll(line, "{E}", b.Eye)
		// Inject hat on first line if it's short/blank and hat is set.
		if i == 0 && b.Hat != "none" {
			if hat, ok := hatLines[b.Hat]; ok && hat != "" {
				l = hat
			}
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(l)
	}
	return sb.String()
}

// RenderFace returns a compact inline representation of the companion.
func RenderFace(b Bones) string {
	faces := map[string]string{
		"duck":     "(>",
		"goose":    "(>",
		"blob":     "(··)",
		"cat":      "(=^·^=)",
		"dragon":   "(>=<)",
		"octopus":  "(~·~)",
		"owl":      "(O,O)",
		"penguin":  "(·▿·)",
		"turtle":   "(·͜·)",
		"snail":    "(@·@)",
		"ghost":    "(·o·)",
		"axolotl":  "(·ᗣ·)",
		"capybara": "(·_·)",
		"cactus":   "(·|·)",
		"robot":    "[·_·]",
		"rabbit":   "(·▾·)",
		"mushroom": "(·ω·)",
		"chonk":    "(·ᴥ·)",
	}
	if face, ok := faces[b.Species]; ok {
		return strings.ReplaceAll(face, "·", b.Eye)
	}
	return fmt.Sprintf("(%s)", b.Eye)
}

// --- Display ---

// Summary returns a multi-line text summary of the companion for /buddy.
var rarityBadge = map[string]string{
	"common":    "⬜ Common",
	"uncommon":  "🟩 Uncommon",
	"rare":      "🟦 Rare",
	"epic":      "🟪 Epic",
	"legendary": "🟨 Legendary",
}

func Summary(b Bones, name string) string {
	var sb strings.Builder
	sprite := RenderSprite(b, 0)
	sb.WriteString(sprite)
	sb.WriteByte('\n')
	shinyMark := ""
	if b.Shiny {
		shinyMark = " ✨ SHINY"
	}
	badge := rarityBadge[b.Rarity]
	if badge == "" {
		badge = b.Rarity
	}
	sb.WriteString(fmt.Sprintf("\n%s the %s  %s%s\n", name, b.Species, badge, shinyMark))
	sb.WriteString("\nStats:\n")
	for _, stat := range statNames {
		val := b.Stats[stat]
		filled := val / 10
		bar := strings.Repeat("█", filled) + strings.Repeat("░", 10-filled)
		sb.WriteString(fmt.Sprintf("  %-12s %s %3d\n", stat, bar, val))
	}
	return strings.TrimRight(sb.String(), "\n")
}
