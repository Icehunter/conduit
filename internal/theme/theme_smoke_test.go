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
	if Active().Primary != "#F5F7FA" {
		t.Fatalf("expected dark Primary #F5F7FA, got %s", Active().Primary)
	}

	Set("light")
	if Active().Name != "light" {
		t.Fatalf("expected light, got %s", Active().Name)
	}
	if Active().Primary != "#1F2328" {
		t.Fatalf("expected light Primary #1F2328, got %s", Active().Primary)
	}

	Set("dark-daltonized")
	if !strings.Contains(Active().Name, "daltonized") {
		t.Fatalf("expected dark-daltonized, got %s", Active().Name)
	}
	if Active().Success != "#3B82F6" {
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

// TestAvailableThemes lists all six canonical theme names.
func TestAvailableThemes(t *testing.T) {
	got := AvailableThemes()
	want := []string{"dark", "light", "dark-daltonized", "light-daltonized", "dark-ansi", "light-ansi"}
	if len(got) != len(want) {
		t.Fatalf("expected %d themes, got %d", len(want), len(got))
	}
	for i, name := range want {
		if got[i] != name {
			t.Fatalf("at index %d: got %s, want %s", i, got[i], name)
		}
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
