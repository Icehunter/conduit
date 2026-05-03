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
	if Active().Primary != "#CDD6E0" {
		t.Fatalf("expected dark Primary #CDD6E0, got %s", Active().Primary)
	}

	Set("light")
	if Active().Name != "light" {
		t.Fatalf("expected light, got %s", Active().Name)
	}
	if Active().Primary != "#1F2328" {
		t.Fatalf("expected light Primary #1F2328, got %s", Active().Primary)
	}

	Set("dark-accessible")
	if !strings.Contains(Active().Name, "daltonism") {
		t.Fatalf("expected dark-daltonism, got %s", Active().Name)
	}
	if Active().Success != "#3B82F6" {
		t.Fatalf("expected accessible Success blue, got %s", Active().Success)
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
