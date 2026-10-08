package tui

import (
	"sort"

	"github.com/icehunter/conduit/internal/api"
	"github.com/icehunter/conduit/internal/auth"
	"github.com/icehunter/conduit/internal/settings"
	"github.com/icehunter/conduit/internal/transcript"
)

// summarizeMaxChars bounds SummarizeMessages output so a few huge messages
// cannot blow up the extraction sub-agent's prompt.
const summarizeMaxChars = 48_000

// SummarizeMessages renders the last n model-visible messages as a
// plain-text transcript for /memory extract. Tool blocks are flattened so
// the sub-agent sees a readable conversation, not raw JSON. Exported
// because cmd/conduit/main.go's auto-extract path also needs it.
func SummarizeMessages(history []api.Message, n int) string {
	return transcript.Render(history, transcript.Options{LastN: n, MaxChars: summarizeMaxChars})
}

func configuredAccountProviders() []settings.ActiveProviderSettings {
	email := auth.ActiveEmail()
	store, err := auth.ListAccounts()
	if err != nil {
		return nil
	}
	var providers []settings.ActiveProviderSettings
	seen := map[string]bool{}
	addProvider := func(entry auth.AccountEntry) {
		kind := accountProviderKind(entry.Kind)
		if kind == "" || entry.Email == "" {
			return
		}
		key := kind + "\x00" + entry.Email
		if seen[key] {
			return
		}
		providers = append(providers, settings.ActiveProviderSettings{
			Kind:    kind,
			Account: entry.Email,
		})
		seen[key] = true
	}
	if entry, ok := store.Accounts[email]; ok {
		addProvider(entry)
	}
	var entries []auth.AccountEntry
	for _, entry := range store.Accounts {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		left := accountProviderKind(entries[i].Kind) + "\x00" + entries[i].Email
		right := accountProviderKind(entries[j].Kind) + "\x00" + entries[j].Email
		return left < right
	})
	for _, entry := range entries {
		addProvider(entry)
	}
	return providers
}

func accountProviderKind(accountKind string) string {
	if accountKind == auth.AccountKindAnthropicConsole {
		return settings.ProviderKindAnthropicAPI
	}
	if accountKind == "" || accountKind == auth.AccountKindClaudeAI {
		return settings.ProviderKindClaudeSubscription
	}
	return ""
}
