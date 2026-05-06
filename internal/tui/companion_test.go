package tui

import "testing"

func TestUserAddressedCompanionLooksPastCurrentAssistant(t *testing.T) {
	t.Parallel()

	m := Model{
		messages: []Message{
			{Role: RoleUser, Content: "Narisa where are you?"},
			{Role: RoleAssistant, Content: "[Narisa: Right here.]"},
		},
	}

	if !m.userAddressedCompanion("Narisa") {
		t.Fatal("expected companion address check to find the user message before the current assistant response")
	}
}

func TestUserAddressedCompanionStopsAtOlderAssistantTurn(t *testing.T) {
	t.Parallel()

	m := Model{
		messages: []Message{
			{Role: RoleUser, Content: "Narisa where are you?"},
			{Role: RoleAssistant, Content: "[Narisa: Right here.]"},
			{Role: RoleUser, Content: "hello"},
			{Role: RoleAssistant, Content: "Hi."},
		},
	}

	if m.userAddressedCompanion("Narisa") {
		t.Fatal("expected companion address check to ignore older turns")
	}
}
