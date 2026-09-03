package tui

import "testing"

// TestPermModeStoredVal_AutoModeMapsToRealBypassMode is a regression test:
// "Auto Mode" used to store a literal "auto" string that no permissions.Mode
// case handled (cmd/conduit/mainrepl.go casts this straight to
// permissions.Mode(s.DefaultMode) with no translation), so picking it from
// the settings panel silently didn't behave like bypass mode at all.
func TestPermModeStoredVal_AutoModeMapsToRealBypassMode(t *testing.T) {
	tests := []struct {
		display string
		want    string
	}{
		{"Plan Mode", "plan"},
		{"Accept Edits", "acceptEdits"},
		{"Auto Mode", "bypassPermissions"},
		{"Don't Ask", "bypassPermissions"},
		{"Default", "default"},
		{"unrecognized", "default"},
	}
	for _, tt := range tests {
		t.Run(tt.display, func(t *testing.T) {
			if got := permModeStoredVal(tt.display); got != tt.want {
				t.Errorf("permModeStoredVal(%q) = %q, want %q", tt.display, got, tt.want)
			}
		})
	}
}

// TestPermModeDisplay_RoundTripsRealModeValues verifies every real
// permissions.Mode value that permModeStoredVal can now produce displays
// back sensibly, and that a legacy "auto" string already persisted from
// before the fix still displays as "Auto Mode" rather than falling through
// to "Default".
func TestPermModeDisplay_RoundTripsRealModeValues(t *testing.T) {
	tests := []struct {
		stored string
		want   string
	}{
		{"plan", "Plan Mode"},
		{"acceptEdits", "Accept Edits"},
		{"bypassPermissions", "Don't Ask"},
		{"auto", "Auto Mode"}, // legacy value, still handled defensively
		{"default", "Default"},
		{"", "Default"},
	}
	for _, tt := range tests {
		t.Run(tt.stored, func(t *testing.T) {
			if got := permModeDisplay(tt.stored); got != tt.want {
				t.Errorf("permModeDisplay(%q) = %q, want %q", tt.stored, got, tt.want)
			}
		})
	}
}
