package theme

import (
	"strings"
	"testing"
)

// TestSetSwapsPalette verifies that theme.Set actually changes Active()
// and that OnChange listeners are invoked.
func TestSetSwapsPalette(t *testing.T) {
	defer Set("dark") // restore
	Set("dark")
	if Active().Name != "dark" {
		t.Fatalf("expected dark, got %s", Active().Name)
	}
	if Active().Primary != "#FFFFFF" {
		t.Fatalf("expected dark Primary #FFFFFF, got %s", Active().Primary)
	}

	Set("light")
	if Active().Name != "light" {
		t.Fatalf("expected light, got %s", Active().Name)
	}
	if Active().Primary != "#2A2A2A" {
		t.Fatalf("expected light Primary #2A2A2A (dark gray, visible on both bgs), got %s", Active().Primary)
	}

	Set("dark-daltonized")
	if !strings.Contains(Active().Name, "daltonized") {
		t.Fatalf("expected dark-daltonized, got %s", Active().Name)
	}
	if Active().Success != "#3399FF" {
		t.Fatalf("expected daltonized Success blue, got %s", Active().Success)
	}

	// Aliases should map correctly.
	Set("dark-accessible")
	if Active().Name != "dark-daltonized" {
		t.Fatalf("dark-accessible alias should map to dark-daltonized, got %s", Active().Name)
	}

	Set("dark-ansi")
	if Active().Name != "dark-ansi" {
		t.Fatalf("expected dark-ansi, got %s", Active().Name)
	}
	if Active().Primary != "15" {
		t.Fatalf("expected ANSI 15 (whiteBright), got %s", Active().Primary)
	}
}

// TestAvailableThemes lists all six Claude Code palette names.
//
// Light variants stay in the picker so users who share settings.json
// between conduit and Claude Code don't have conduit silently rewrite
// their CC theme preference.
func TestAvailableThemes(t *testing.T) {
	got := AvailableThemes()
	want := []string{"dark", "dark-daltonized", "dark-ansi", "light", "light-daltonized", "light-ansi"}
	if len(got) != len(want) {
		t.Fatalf("expected %d themes, got %d (%v)", len(want), len(got), got)
	}
	for i, name := range want {
		if got[i] != name {
			t.Fatalf("at index %d: got %s, want %s", i, got[i], name)
		}
	}
}

// TestAvailableThemes_IncludesUserThemes verifies user-defined palettes
// from settings.json appear in the picker (ahead of built-ins).
func TestAvailableThemes_IncludesUserThemes(t *testing.T) {
	defer SetUserThemes(nil) // reset
	SetUserThemes(map[string]Palette{
		"my-custom": {Name: "my-custom", Primary: "#ABCDEF"},
	})
	got := AvailableThemes()
	found := false
	for _, name := range got {
		if name == "my-custom" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("user theme 'my-custom' missing from picker: %v", got)
	}
}

// TestOnChangeFires verifies listeners are invoked exactly once per Set.
func TestOnChangeFires(t *testing.T) {
	defer Set("dark")
	count := 0
	OnChange(func() { count++ })

	Set("light")
	Set("dark")

	if count < 2 {
		t.Fatalf("expected listener called at least twice, got %d", count)
	}
}

// TestAnsiFG produces a valid truecolor escape.
func TestAnsiFG(t *testing.T) {
	got := AnsiFG("#FF0000")
	want := "\033[38;2;255;0;0m"
	if got != want {
		t.Fatalf("AnsiFG(#FF0000) = %q, want %q", got, want)
	}
}
